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
	"unicode/utf8"

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

type inputAuditPayload struct {
	Version int `json:"version"`
	Bytes   int `json:"bytes"`
}

// MaxInputBytes 是一次输入操作允许的最大 UTF-8 字节数。
const MaxInputBytes = 64 * 1024

var (
	// ErrUnknownAgent 表示目标 Agent 不存在。
	ErrUnknownAgent = errors.New("session: unknown agent")
	// ErrNotAttached 表示会话存在，但当前 daemon 没有它的 PTY。
	ErrNotAttached = errors.New("session: agent is not attached to a PTY")
	// ErrManagerClosed 表示 Manager 已开始关闭，不再接受新会话。
	ErrManagerClosed = errors.New("session: manager closed")
	// ErrInvalidMode 表示启动请求包含不支持的运行模式。
	ErrInvalidMode = errors.New("session: invalid mode")
	// ErrInputEmpty 表示输入为空。
	ErrInputEmpty = errors.New("session: input is empty")
	// ErrInputTooLarge 表示输入超过单次操作上限。
	ErrInputTooLarge = errors.New("session: input exceeds maximum size")
	// ErrInputNotUTF8 表示输入不是合法 UTF-8 文本。
	ErrInputNotUTF8 = errors.New("session: input is not valid UTF-8")
	// ErrInputWrite 表示 PTY 未完整接受输入。
	ErrInputWrite = errors.New("session: input write failed")
	// ErrInputAudit 表示输入已送达，但审计事件持久化失败。
	ErrInputAudit = errors.New("session: input delivered but audit failed")
)

type stopCause uint8

const (
	stopCauseNone stopCause = iota
	stopCauseUser
	stopCauseShutdown
)

type processSession interface {
	Write([]byte) (int, error)
	Close() error
	PID() int
}

type runningSession struct {
	inputMu     sync.Mutex
	process     processSession
	stopCause   stopCause
	exitClaimed bool
}

// InputResult 描述一次输入操作已经写入 PTY 的字节数。
type InputResult struct {
	BytesWritten int
}

// Manager 是会话编排入口。
type Manager struct {
	reg   *adapter.Registry
	hub   *event.Hub
	store *store.Store

	mu       sync.RWMutex
	agents   map[agent.ID]*agent.Agent
	sessions map[agent.ID]*runningSession
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
		sessions: make(map[agent.ID]*runningSession),
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
	running := &runningSession{}
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
			m.onExit(id, running, info)
		},
	})
	if err != nil {
		startErr := fmt.Errorf("session: start pty: %w", err)
		a.SetError(startErr.Error())
		m.persistAndPublish(event.NewError(0, string(id), string(id), startErr.Error()))
		_ = a.Transition(agent.StateStopped, "startup failed")
		return nil, startErr
	}

	running.process = sess
	m.mu.Lock()
	m.sessions[id] = running
	m.mu.Unlock()

	// 4. 状态推进：进程活着 -> Working。
	if err := a.Transition(agent.StateWorking, "process started"); err != nil {
		m.requestStop(id, running, stopCauseShutdown)
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
			session *runningSession
		}
		m.mu.Lock()
		attached := make([]attachedSession, 0, len(m.sessions))
		for id, sess := range m.sessions {
			if !sess.exitClaimed && sess.stopCause == stopCauseNone {
				sess.stopCause = stopCauseShutdown
			}
			attached = append(attached, attachedSession{id: id, session: sess})
		}
		m.mu.Unlock()
		sort.Slice(attached, func(i, j int) bool {
			return attached[i].id < attached[j].id
		})

		var closeErrors []error
		for _, item := range attached {
			if err := item.session.process.Close(); err != nil {
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
		if state := a.State(); state == agent.StateDone || state == agent.StateStopped {
			return nil
		}
		return fmt.Errorf("session: agent %q is %s without a PTY", id, a.State())
	}
	m.requestStop(id, sess, stopCauseUser)
	if err := sess.process.Close(); err != nil && !errors.Is(err, pty.ErrClosed) {
		return fmt.Errorf("session: stop agent %q: %w", id, err)
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
		st.PID = sess.process.PID()
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

// SendInput 向已连接的 Agent 写入完整输入，并记录脱敏审计事件。
func (m *Manager) SendInput(id agent.ID, data []byte) (InputResult, error) {
	if len(data) == 0 {
		return InputResult{}, ErrInputEmpty
	}
	if len(data) > MaxInputBytes {
		return InputResult{}, fmt.Errorf("%w: %d bytes", ErrInputTooLarge, len(data))
	}
	if !utf8.Valid(data) {
		return InputResult{}, ErrInputNotUTF8
	}
	payload, err := json.Marshal(inputAuditPayload{Version: 1, Bytes: len(data)})
	if err != nil {
		return InputResult{}, fmt.Errorf("session: encode input audit: %w", err)
	}

	m.mu.RLock()
	closed := m.closed
	_, known := m.agents[id]
	running, attached := m.sessions[id]
	m.mu.RUnlock()
	if closed {
		return InputResult{}, ErrManagerClosed
	}
	if !known {
		return InputResult{}, fmt.Errorf("%w: %q", ErrUnknownAgent, id)
	}
	if !attached {
		return InputResult{}, fmt.Errorf("%w: %q", ErrNotAttached, id)
	}

	running.inputMu.Lock()
	defer running.inputMu.Unlock()

	m.mu.RLock()
	current, stillAttached := m.sessions[id]
	closed = m.closed
	exitClaimed := running.exitClaimed
	m.mu.RUnlock()
	if closed {
		return InputResult{}, ErrManagerClosed
	}
	if !stillAttached || current != running || exitClaimed {
		return InputResult{}, fmt.Errorf("%w: %q", ErrNotAttached, id)
	}

	written, err := running.process.Write(data)
	result := InputResult{BytesWritten: written}
	if err != nil {
		if errors.Is(err, pty.ErrClosed) {
			return result, fmt.Errorf("%w: %q", ErrNotAttached, id)
		}
		return result, fmt.Errorf(
			"%w: agent %q wrote %d/%d bytes: %w",
			ErrInputWrite,
			id,
			written,
			len(data),
			err,
		)
	}
	if written != len(data) {
		return result, fmt.Errorf(
			"%w: agent %q wrote %d/%d bytes",
			ErrInputWrite,
			id,
			written,
			len(data),
		)
	}
	if err := m.persistAndPublish(event.NewAgentInput(
		0,
		string(id),
		string(id),
		string(payload),
	)); err != nil {
		return result, fmt.Errorf(
			"%w: agent %q received %d bytes; do not retry: %w",
			ErrInputAudit,
			id,
			written,
			err,
		)
	}
	return result, nil
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
		if a.RunMode() == agent.RunModeOneshot && a.State() == agent.StateWorking {
			_ = a.Transition(agent.StateDone, hint.Reason)
		}
	}
}

type exitDecision struct {
	target       agent.State
	reason       string
	errorMessage string
}

func decideExit(mode agent.RunMode, cause stopCause, info pty.ExitInfo) exitDecision {
	switch cause {
	case stopCauseUser:
		return exitDecision{target: agent.StateStopped, reason: "user stop"}
	case stopCauseShutdown:
		return exitDecision{target: agent.StateStopped, reason: "manager shutdown"}
	}

	reason := fmt.Sprintf("process exited code=%d", info.Code)
	if mode == agent.RunModeOneshot && info.Code == 0 && info.Err == nil {
		return exitDecision{target: agent.StateDone, reason: reason}
	}

	decision := exitDecision{target: agent.StateStopped, reason: reason}
	if info.Err != nil {
		decision.errorMessage = info.Err.Error()
	} else if info.Code != 0 {
		decision.errorMessage = reason
	}
	return decision
}

// onExit 根据运行模式和停止原因记录终态，再移除 PTY。
func (m *Manager) onExit(id agent.ID, running *runningSession, info pty.ExitInfo) {
	running.inputMu.Lock()
	defer running.inputMu.Unlock()

	cause, ok := m.claimExit(id, running)
	if !ok {
		return
	}
	defer m.detach(id, running)

	a, ok := m.agent(id)
	if !ok {
		return
	}

	decision := decideExit(a.RunMode(), cause, info)
	if decision.errorMessage != "" {
		a.SetError(decision.errorMessage)
		_ = m.persistAndPublish(event.NewError(
			0,
			string(id),
			string(id),
			decision.errorMessage,
		))
	}
	if a.State() != decision.target {
		_ = a.Transition(decision.target, decision.reason)
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

func (m *Manager) requestStop(id agent.ID, running *runningSession, cause stopCause) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.sessions[id]; ok &&
		current == running &&
		!running.exitClaimed &&
		running.stopCause == stopCauseNone {
		running.stopCause = cause
	}
}

func (m *Manager) claimExit(id agent.ID, running *runningSession) (stopCause, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.sessions[id]
	if !ok || current != running || running.exitClaimed {
		return stopCauseNone, false
	}
	running.exitClaimed = true
	return running.stopCause, true
}

func (m *Manager) detach(id agent.ID, running *runningSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[id] == running {
		delete(m.sessions, id)
	}
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
