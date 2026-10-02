// Package session 编排 agent 的完整生命周期：状态机 + PTY + 适配器 + 事件 + 存储。
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	AgentID   string      `json:"agent_id"`
	Name      string      `json:"name"`
	Vendor    string      `json:"vendor"`
	State     agent.State `json:"state"`
	PID       int         `json:"pid,omitempty"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
	LastError string      `json:"last_error,omitempty"`
}

// StartRequest 描述启动一个新 agent 会话的参数。
type StartRequest struct {
	// Vendor 是厂商标识（"claude"/"codex"/"generic"）；空则 generic。
	Vendor string
	// Name 是显示名；空则自动生成。
	Name string
	// Command 覆盖默认命令（generic 时必填）。
	Command string
	// Args 附加参数。
	Args []string
	// Dir 工作目录；空则继承 daemon 目录。
	Dir string
}

type createdPayload struct {
	Version int    `json:"version"`
	Name    string `json:"name"`
	Vendor  string `json:"vendor"`
}

// Manager 是会话编排入口。
type Manager struct {
	reg   *adapter.Registry
	hub   *event.Hub
	store *store.Store

	mu       sync.RWMutex
	agents   map[agent.ID]*agent.Agent
	sessions map[agent.ID]*pty.Session
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

// Start 启动一个 agent 会话。
func (m *Manager) Start(ctx context.Context, req StartRequest) (*Status, error) {
	if req.Vendor == "" {
		req.Vendor = "generic"
	}
	entry := m.reg.For(req.Vendor)

	// 1. 在创建持久化会话前解析并校验命令。
	cmdName, cmdArgs := entry.Runner.Command()
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
		agent.WithStateChangeHook(m.onStateChange),
	)
	payload, err := json.Marshal(createdPayload{
		Version: 1,
		Name:    req.Name,
		Vendor:  req.Vendor,
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

	// 3. 创建 PTY 会话。
	sess, err := pty.Start(pty.Config{
		Command: cmdName,
		Args:    cmdArgs,
		Dir:     req.Dir,
	})
	if err != nil {
		startErr := fmt.Errorf("session: start pty: %w", err)
		a.SetError(startErr.Error())
		m.persistAndPublish(event.NewError(0, string(id), string(id), startErr.Error()))
		_ = a.Transition(agent.StateStopped, "startup failed")
		return nil, startErr
	}

	sess.OnOutput = func(line string) { m.onOutput(id, line, entry) }
	sess.OnExit = func(info pty.ExitInfo) { m.onExit(id, info) }

	m.mu.Lock()
	m.sessions[id] = sess
	m.mu.Unlock()

	// 4. 状态推进：进程活着 -> Working。
	if err := a.Transition(agent.StateWorking, "process started"); err != nil {
		_ = sess.Close()
		m.cleanup(id)
		return nil, err
	}

	return m.Status(id)
}

// Stop 停止一个 agent 会话（幂等）。
func (m *Manager) Stop(id agent.ID) error {
	m.mu.RLock()
	sess, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session: unknown agent %q", id)
	}
	if err := sess.Close(); err != nil && !errors.Is(err, pty.ErrClosed) {
		return err
	}
	a, ok := m.agent(id)
	if ok && a.State() != agent.StateStopped {
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
	defer m.mu.RUnlock()

	out := make([]*Status, 0, len(m.agents))
	for id := range m.agents {
		st, err := m.Status(id)
		if err == nil {
			out = append(out, st)
		}
	}
	return out
}

// Replay 返回某会话的事件流（来自 store，按 seq 升序）。
func (m *Manager) Replay(sessionID string) ([]store.EventRow, error) {
	return m.store.Replay(sessionID)
}

// Write 向某 agent 注入输入。
func (m *Manager) Write(id agent.ID, data []byte) error {
	m.mu.RLock()
	sess, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session: unknown agent %q", id)
	}
	if _, err := sess.Write(data); err != nil {
		return err
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

// cleanup 移除未完全启动的会话残留。
func (m *Manager) cleanup(id agent.ID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.agents, id)
	delete(m.sessions, id)
}
