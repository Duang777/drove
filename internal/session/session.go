// Package session 编排 agent 的完整生命周期：状态机 + PTY + 适配器 + 事件 + 存储。
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/pty"
	"github.com/Duang777/drove/internal/store"
)

// Status 是对外暴露的会话视图（供 daemon/api/CLI 使用）。
type Status struct {
	AgentID   string        `json:"agent_id"`
	Name      string        `json:"name"`
	Vendor    string        `json:"vendor"`
	Mode      agent.RunMode `json:"mode"`
	State     agent.State   `json:"state"`
	PID       int           `json:"pid,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	LastError string        `json:"last_error,omitempty"`
}

// StartRequest 描述启动一个新 agent 会话的参数。
type StartRequest struct {
	// Vendor 是厂商标识（"claude"/"codex"/"generic"）；空则 generic。
	Vendor string `json:"vendor"`
	// Name 是显示名；空则自动生成。
	Name string `json:"name,omitempty"`
	// Command 覆盖默认命令（generic 时必填）。
	Command string `json:"command,omitempty"`
	// Args 附加参数。
	Args []string `json:"args,omitempty"`
	// Dir 工作目录；空则继承 daemon 目录。
	Dir string `json:"dir,omitempty"`
	// Mode 是运行模式；空则 interactive。
	Mode agent.RunMode `json:"mode,omitempty"`
}

type createdPayload struct {
	Version int            `json:"version"`
	Name    string         `json:"name"`
	Vendor  string         `json:"vendor"`
	Mode    *agent.RunMode `json:"mode,omitempty"`
}

var (
	// ErrNotAttached 表示会话存在，但当前 daemon 没有它的 PTY。
	ErrNotAttached = errors.New("session: agent is not attached to a PTY")
	// ErrManagerClosed 表示 Manager 已开始关闭，不再接受新会话。
	ErrManagerClosed = errors.New("session: manager closed")
	// ErrInvalidMode 表示启动请求包含不支持的运行模式。
	ErrInvalidMode = errors.New("session: invalid mode")
)

// Manager 是会话编排入口。
type Manager struct {
	reg   *adapter.Registry
	hub   *event.Hub
	store *store.Store

	mu       sync.RWMutex
	agents   map[agent.ID]*agent.Agent
	sessions map[agent.ID]*pty.Session
	closed   bool

	starts    sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

// BootstrapResult 包含恢复后的运行时组件和启动报告。
type BootstrapResult struct {
	Manager  *Manager
	Hub      *event.Hub
	Recovery RecoveryReport
}

// NewManager 创建 Manager。
func NewManager(reg *adapter.Registry, hub *event.Hub, st *store.Store) *Manager {
	return &Manager{
		reg:      reg,
		hub:      hub,
		store:    st,
		agents:   make(map[agent.ID]*agent.Agent),
		sessions: make(map[agent.ID]*pty.Session),
	}
}

// Bootstrap 重建会话投影，并收口当前 daemon 没有 PTY 的历史会话。
func Bootstrap(ctx context.Context, reg *adapter.Registry, st *store.Store) (*BootstrapResult, error) {
	projector := newRecoveryProjector()
	lastSeq, err := st.ScanEvents(ctx, projector.Apply)
	if err != nil {
		return nil, fmt.Errorf("session: bootstrap scan: %w", err)
	}
	plan, err := projector.Finish(time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("session: bootstrap projection: %w", err)
	}

	manager := NewManager(reg, nil, st)
	for _, snapshot := range plan.Snapshots {
		restored, err := agent.Restore(snapshot, agent.WithStateChangeHook(manager.onStateChange))
		if err != nil {
			return nil, fmt.Errorf("session: bootstrap restore agent %q: %w", snapshot.ID, err)
		}
		manager.agents[snapshot.ID] = restored
	}

	committedLastSeq, err := st.AppendEvents(ctx, lastSeq, plan.Reconciliation)
	if err != nil {
		return nil, fmt.Errorf("session: bootstrap reconciliation: %w", err)
	}
	hub := event.NewHub(committedLastSeq)
	manager.hub = hub
	plan.Report.LastSeq = committedLastSeq
	return &BootstrapResult{
		Manager:  manager,
		Hub:      hub,
		Recovery: plan.Report,
	}, nil
}

// Start 启动一个 agent 会话。
func (m *Manager) Start(ctx context.Context, req StartRequest) (*Status, error) {
	if beginErr := m.beginStart(); beginErr != nil {
		return nil, fmt.Errorf("session: start: %w", beginErr)
	}
	defer m.endStart()

	if req.Vendor == "" {
		req.Vendor = "generic"
	}
	mode, err := normalizeRunMode(req.Mode)
	if err != nil {
		return nil, err
	}
	req.Mode = mode
	entry := m.reg.For(req.Vendor)

	// 1. 在创建持久化会话前解析并校验命令。
	cmdName, cmdArgs := entry.Runner.Command(req.Mode)
	if req.Command != "" {
		cmdName = req.Command
		cmdArgs = req.Args
	}
	if cmdName == "" {
		return nil, errors.New("session: generic vendor requires explicit command")
	}

	// 2. 构造 agent 并先持久化会话元数据。
	id := agent.ID(uuid.NewString())
	if req.Name == "" {
		req.Name = req.Vendor + "-" + string(id)[:8]
	}
	a := agent.New(id,
		agent.WithName(req.Name),
		agent.WithVendor(req.Vendor),
		agent.WithRunMode(req.Mode),
		agent.WithStateChangeHook(m.onStateChange),
	)
	persistedMode := req.Mode
	payload, err := json.Marshal(createdPayload{
		Version: 1,
		Name:    req.Name,
		Vendor:  req.Vendor,
		Mode:    &persistedMode,
	})
	if err != nil {
		return nil, fmt.Errorf("session: encode creation metadata: %w", err)
	}
	if err := m.persistAndPublish(event.NewSessionLifecycle(
		0,
		string(id),
		string(id),
		"created",
		string(payload),
	)); err != nil {
		return nil, fmt.Errorf("session: persist creation: %w", err)
	}

	m.mu.Lock()
	m.agents[id] = a
	m.mu.Unlock()
	if err := a.Transition(agent.StateStarting, "session start"); err != nil {
		return nil, err
	}

	// 3. 创建 PTY 会话。回调等待状态和会话登记完成后再进入 Manager。
	callbacksReady := make(chan struct{})
	sess, err := pty.Start(pty.Config{
		Command: cmdName,
		Args:    cmdArgs,
		Dir:     req.Dir,
		OnOutput: func(line string) {
			<-callbacksReady
			m.onOutput(id, line, entry)
		},
		OnExit: func(info pty.ExitInfo) {
			<-callbacksReady
			m.onExit(id, info)
		},
	})
	if err != nil {
		startErr := fmt.Errorf("session: start pty: %w", err)
		a.SetError(startErr.Error())
		m.persistAndPublish(event.NewError(0, string(id), string(id), startErr.Error()))
		_ = a.Transition(agent.StateStopped, "startup failed")
		return nil, startErr
	}

	m.mu.Lock()
	m.sessions[id] = sess
	m.mu.Unlock()

	// 4. 状态推进：进程活着 -> Working。
	if err := a.Transition(agent.StateWorking, "process started"); err != nil {
		close(callbacksReady)
		_ = sess.Close()
		m.cleanup(id)
		return nil, err
	}
	close(callbacksReady)

	return m.Status(id)
}

// Close 停止全部已连接会话并等待 PTY 回调结束。
func (m *Manager) Close() error {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.mu.Unlock()

		m.starts.Wait()

		type attachedSession struct {
			id      agent.ID
			session *pty.Session
		}
		m.mu.RLock()
		attached := make([]attachedSession, 0, len(m.sessions))
		for id, sess := range m.sessions {
			attached = append(attached, attachedSession{id: id, session: sess})
		}
		m.mu.RUnlock()
		sort.Slice(attached, func(i, j int) bool {
			return attached[i].id < attached[j].id
		})

		var closeErrors []error
		for _, item := range attached {
			if err := item.session.Close(); err != nil {
				closeErrors = append(
					closeErrors,
					fmt.Errorf("session: close agent %q: %w", item.id, err),
				)
			}
		}

		m.mu.Lock()
		for _, item := range attached {
			if m.sessions[item.id] == item.session {
				delete(m.sessions, item.id)
			}
		}
		m.mu.Unlock()
		m.closeErr = errors.Join(closeErrors...)
	})
	return m.closeErr
}

// Stop 停止一个 agent 会话（幂等）。
func (m *Manager) Stop(id agent.ID) error {
	a, ok := m.agent(id)
	if !ok {
		return fmt.Errorf("session: unknown agent %q", id)
	}

	m.mu.RLock()
	sess, attached := m.sessions[id]
	m.mu.RUnlock()
	if !attached {
		if a.State() == agent.StateStopped {
			return nil
		}
		return fmt.Errorf("session: agent %q is %s without a PTY", id, a.State())
	}
	if err := sess.Close(); err != nil && !errors.Is(err, pty.ErrClosed) {
		return fmt.Errorf("session: stop agent %q: %w", id, err)
	}
	if a.State() != agent.StateStopped {
		// 进程退出回调可能已迁移到 Stopped，这里仅在未停止时迁移，保证幂等。
		_ = a.Transition(agent.StateStopped, "user stop")
	}
	return nil
}

// Status 返回某 agent 的当前状态。
func (m *Manager) Status(id agent.ID) (*Status, error) {
	a, ok := m.agent(id)
	if !ok {
		return nil, fmt.Errorf("session: unknown agent %q", id)
	}
	m.mu.RLock()
	sess, _ := m.sessions[id]
	m.mu.RUnlock()

	st := &Status{
		AgentID:   string(a.ID()),
		Name:      a.Name(),
		Vendor:    a.Vendor(),
		Mode:      a.RunMode(),
		State:     a.State(),
		CreatedAt: a.CreatedAt(),
		UpdatedAt: a.UpdatedAt(),
		LastError: a.LastError(),
	}
	if sess != nil {
		st.PID = sess.PID()
	}
	return st, nil
}

// List 返回全部会话状态（按创建时间排序）。
func (m *Manager) List() []*Status {
	m.mu.RLock()
	agents := make([]*agent.Agent, 0, len(m.agents))
	for _, a := range m.agents {
		agents = append(agents, a)
	}
	m.mu.RUnlock()

	out := make([]*Status, 0, len(agents))
	for _, a := range agents {
		st, err := m.Status(a.ID())
		if err == nil {
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].AgentID < out[j].AgentID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// Replay 返回某会话的事件流（来自 store，按 seq 升序）。
func (m *Manager) Replay(sessionID string) ([]store.EventRow, error) {
	return m.store.Replay(sessionID)
}

// Write 向某 agent 注入输入。
func (m *Manager) Write(id agent.ID, data []byte) error {
	if _, ok := m.agent(id); !ok {
		return fmt.Errorf("session: unknown agent %q", id)
	}
	m.mu.RLock()
	sess, attached := m.sessions[id]
	m.mu.RUnlock()
	if !attached {
		return fmt.Errorf("%w: %q", ErrNotAttached, id)
	}
	if _, err := sess.Write(data); err != nil {
		return fmt.Errorf("session: write agent %q: %w", id, err)
	}
	return nil
}

// -- 内部回调 --

// onStateChange 把状态迁移落库并发布事件。
func (m *Manager) onStateChange(id agent.ID, from, to agent.State, reason string) {
	m.persistAndPublish(event.NewStateChanged(0, string(id), string(id), string(from), string(to), reason))
}

// onOutput 按行发布输出事件；同时把适配器 hint 融入状态决策。
func (m *Manager) onOutput(id agent.ID, line string, entry adapter.Entry) {
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return
	}
	m.persistAndPublish(event.NewOutput(0, string(id), string(id), line))

	if entry.Heuristic == nil {
		return
	}
	hint, ok := entry.Heuristic.Classify(line)
	if !ok {
		return
	}
	a, ok := m.agent(id)
	if !ok {
		return
	}
	switch hint.State {
	case agent.StateBlocked:
		// 仅当当前在 Working/Idle 时采纳 Blocked 提示。
		cur := a.State()
		if cur == agent.StateWorking || cur == agent.StateIdle {
			_ = a.Transition(agent.StateBlocked, hint.Reason)
		}
	case agent.StateDone:
		if a.State() == agent.StateWorking {
			_ = a.Transition(agent.StateDone, hint.Reason)
		}
	}
}

// onExit 进程退出：迁移 Stopped（若仍存活状态）。
func (m *Manager) onExit(id agent.ID, info pty.ExitInfo) {
	a, ok := m.agent(id)
	if !ok {
		return
	}
	cur := a.State()
	if cur != agent.StateStopped {
		reason := fmt.Sprintf("process exited code=%d", info.Code)
		if info.Err != nil {
			a.SetError(info.Err.Error())
		}
		_ = a.Transition(agent.StateStopped, reason)
	}
}

// persistAndPublish 先分配全局序号、落库（保证回放一致），再发布到 Hub。
func (m *Manager) persistAndPublish(ev event.Event) error {
	ev.Seq = m.hub.NextSeq()
	if err := m.store.AppendEvent(store.EventRow{
		Seq:       ev.Seq,
		Timestamp: ev.Timestamp,
		Type:      string(ev.Type),
		SessionID: ev.SessionID,
		AgentID:   ev.AgentID,
		From:      ev.From,
		To:        ev.To,
		Reason:    ev.Reason,
		Payload:   ev.Payload,
	}); err != nil {
		// 落库失败仍发布，但记录错误事件便于审计。
		m.hub.Publish(event.NewError(0, ev.SessionID, ev.AgentID, "persist failed: "+err.Error()))
		return fmt.Errorf("persist event seq %d: %w", ev.Seq, err)
	}
	m.hub.Publish(ev)
	return nil
}

// agent 返回 agent 实例。
func (m *Manager) agent(id agent.ID) (*agent.Agent, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.agents[id]
	return a, ok
}

func (m *Manager) beginStart() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrManagerClosed
	}
	m.starts.Add(1)
	return nil
}

func (m *Manager) endStart() {
	m.starts.Done()
}

func normalizeRunMode(mode agent.RunMode) (agent.RunMode, error) {
	if mode == "" {
		return agent.RunModeInteractive, nil
	}
	if !agent.ValidRunMode(mode) {
		return "", fmt.Errorf("%w: %q", ErrInvalidMode, mode)
	}
	return mode, nil
}

// cleanup 移除未完全启动的会话残留。
func (m *Manager) cleanup(id agent.ID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.agents, id)
	delete(m.sessions, id)
}
