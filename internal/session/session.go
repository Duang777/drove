// Package session 编排 agent 的完整生命周期：状态机 + PTY + 适配器 + 事件 + 存储。
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/pty"
	"github.com/Duang777/drove/internal/store"
	"github.com/Duang777/drove/internal/term"
)

// Status 是对外暴露的会话视图（供 daemon/api/CLI 使用）。
type Status struct {
	AgentID               string                      `json:"agent_id"`
	Name                  string                      `json:"name"`
	Vendor                string                      `json:"vendor"`
	Mode                  agent.RunMode               `json:"mode"`
	State                 agent.State                 `json:"state"`
	PID                   int                         `json:"pid,omitempty"`
	CreatedAt             time.Time                   `json:"created_at"`
	UpdatedAt             time.Time                   `json:"updated_at"`
	LastError             string                      `json:"last_error,omitempty"`
	HookPolicy            agent.HookPolicy            `json:"hook_policy"`
	HookStatus            detect.HookStatus           `json:"hook_status"`
	SignalInjection       agent.SignalInjectionMode   `json:"signal_injection"`
	SignalInjectionStatus agent.SignalInjectionStatus `json:"signal_injection_status"`
	SignalInjectionReason agent.SignalInjectionReason `json:"signal_injection_reason"`
	LastTransition        *agent.Evidence             `json:"last_transition,omitempty"`
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
	// Hooks controls hook authority; empty selects a capability-based default.
	Hooks agent.HookPolicy `json:"hooks,omitempty"`
}

type createdPayload struct {
	Version               int                          `json:"version"`
	Name                  string                       `json:"name"`
	Vendor                string                       `json:"vendor"`
	Mode                  *agent.RunMode               `json:"mode,omitempty"`
	HookPolicy            *agent.HookPolicy            `json:"hook_policy,omitempty"`
	SignalInjection       *agent.SignalInjectionMode   `json:"signal_injection,omitempty"`
	SignalInjectionStatus *agent.SignalInjectionStatus `json:"signal_injection_status,omitempty"`
	SignalInjectionReason *agent.SignalInjectionReason `json:"signal_injection_reason,omitempty"`
}

type inputAuditPayload struct {
	Version int `json:"version"`
	Bytes   int `json:"bytes"`
}

// MaxInputBytes 是一次输入操作允许的最大 UTF-8 字节数。
const MaxInputBytes = 64 * 1024

const (
	initialTerminalRows    = 40
	initialTerminalColumns = 120
)

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
	inputMu        sync.Mutex
	process        processSession
	observer       *observationActor
	output         *outputProcessor
	callbacksReady chan struct{}
	signalReady    chan struct{}
	signalDigest   signalTokenDigest
	hasSignalToken bool
	vendor         string
	injectionDir   string
	stopCause      stopCause
	exitClaimed    bool
}

// InputResult 描述一次输入操作已经写入 PTY 的字节数。
type InputResult struct {
	BytesWritten int
}

// Manager 是会话编排入口。
type Manager struct {
	reg       *adapter.Registry
	hub       *event.Hub
	store     *store.Store
	committer *committer

	signalOrigin     string
	originConfigured bool
	detectConfig     detect.Config
	clock            observationClock
	newCredential    signalCredentialSource
	injectionEnabled bool
	injectionDataDir string
	injectionRelay   string
	injectionModes   map[string]agent.SignalInjectionMode
	injectionFS      signalInjectionFS

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

// NewManager 创建从 initialSeq 继续提交事件的 Manager。
func NewManager(
	reg *adapter.Registry,
	hub *event.Hub,
	st *store.Store,
	initialSeq uint64,
	options ...ManagerOption,
) *Manager {
	manager := &Manager{
		reg:           reg,
		hub:           hub,
		store:         st,
		committer:     newCommitter(initialSeq, st, hub),
		agents:        make(map[agent.ID]*agent.Agent),
		sessions:      make(map[agent.ID]*runningSession),
		detectConfig:  detect.DefaultConfig(),
		clock:         systemObservationClock{},
		newCredential: generateSignalCredential,
		injectionFS:   defaultSignalInjectionFS(),
	}
	for _, option := range options {
		option(manager)
	}
	return manager
}

// Bootstrap 重建会话投影，并收口当前 daemon 没有 PTY 的历史会话。
func Bootstrap(
	ctx context.Context,
	reg *adapter.Registry,
	st *store.Store,
	options ...ManagerOption,
) (*BootstrapResult, error) {
	projector := newRecoveryProjector()
	lastSeq, err := st.ScanEvents(ctx, projector.Apply)
	if err != nil {
		return nil, fmt.Errorf("session: bootstrap scan: %w", err)
	}
	plan, err := projector.Finish(time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("session: bootstrap projection: %w", err)
	}

	restoredAgents := make(map[agent.ID]*agent.Agent, len(plan.Snapshots))
	for _, snapshot := range plan.Snapshots {
		restored, err := agent.Restore(snapshot)
		if err != nil {
			return nil, fmt.Errorf("session: bootstrap restore agent %q: %w", snapshot.ID, err)
		}
		restoredAgents[snapshot.ID] = restored
	}

	committedLastSeq, err := st.AppendEvents(ctx, lastSeq, plan.Reconciliation)
	if err != nil {
		return nil, fmt.Errorf("session: bootstrap reconciliation: %w", err)
	}
	hub := event.NewHub(committedLastSeq)
	manager := NewManager(reg, hub, st, committedLastSeq, options...)
	manager.agents = restoredAgents
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
	hookPolicy, err := normalizeHookPolicy(req.Hooks, entry.SupportsHooks())
	if err != nil {
		return nil, err
	}
	req.Hooks = hookPolicy

	// 1. 在创建持久化会话前解析并校验命令。
	cmdName, baseArgs := entry.Runner.Command(req.Mode)
	if req.Command != "" {
		cmdName = req.Command
		baseArgs = nil
	}
	if cmdName == "" {
		return nil, errors.New("session: generic vendor requires explicit command")
	}
	terminalSize, err := initialTerminalSize()
	if err != nil {
		return nil, fmt.Errorf("session: initial terminal size: %w", err)
	}
	initialPTYSize, err := toPTYSize(terminalSize)
	if err != nil {
		return nil, fmt.Errorf("session: initial PTY size: %w", err)
	}

	// 2. 生成会话专属配置，再持久化会话元数据。
	id := agent.ID(uuid.NewString())
	if req.Name == "" {
		req.Name = req.Vendor + "-" + string(id)[:8]
	}
	injection, err := m.prepareSignalInjection(
		id,
		entry,
		req.Vendor,
		req.Hooks,
		req.Mode,
		baseArgs,
		req.Args,
	)
	if err != nil {
		return nil, err
	}
	a := agent.New(id,
		agent.WithName(req.Name),
		agent.WithVendor(req.Vendor),
		agent.WithRunMode(req.Mode),
		agent.WithHookPolicy(req.Hooks),
		agent.WithSignalInjection(
			injection.mode,
			injection.status,
			injection.reason,
		),
	)
	persistedMode := req.Mode
	persistedPolicy := req.Hooks
	persistedInjection := injection.mode
	persistedInjectionStatus := injection.status
	persistedInjectionReason := injection.reason
	payload, err := json.Marshal(createdPayload{
		Version:               2,
		Name:                  req.Name,
		Vendor:                req.Vendor,
		Mode:                  &persistedMode,
		HookPolicy:            &persistedPolicy,
		SignalInjection:       &persistedInjection,
		SignalInjectionStatus: &persistedInjectionStatus,
		SignalInjectionReason: &persistedInjectionReason,
	})
	if err != nil {
		cleanupErr := m.cleanupSignalInjection(id, injection.dir)
		return nil, errors.Join(
			fmt.Errorf("session: encode creation metadata: %w", err),
			cleanupErr,
		)
	}
	running, processEnv, _, err := m.prepareRuntime(a, entry)
	if err != nil {
		cleanupErr := m.cleanupSignalInjection(id, injection.dir)
		return nil, errors.Join(err, cleanupErr)
	}
	running.injectionDir = injection.dir
	if _, err := m.committer.CommitAgent(
		ctx,
		a,
		agent.MoveTo(agent.StateStarting, "session start", agent.Evidence{
			Source:     agent.EvidenceSession,
			Event:      "session_start",
			Confidence: 1,
		}),
		[]event.Draft{event.NewSessionLifecycleDraft(
			string(id),
			string(id),
			"created",
			string(payload),
		)},
	); err != nil {
		running.observer.Close()
		cleanupErr := m.cleanupSignalInjection(id, running.injectionDir)
		return nil, errors.Join(
			fmt.Errorf("session: persist creation: %w", err),
			cleanupErr,
		)
	}

	m.mu.Lock()
	m.agents[id] = a
	m.sessions[id] = running
	m.mu.Unlock()

	// 3. 创建 PTY 会话。回调等待状态和会话登记完成后再进入 Manager。
	sess, err := pty.Start(pty.Config{
		Command: cmdName,
		Args:    injection.args,
		Env:     processEnv,
		Dir:     req.Dir,
		Size:    initialPTYSize,
		OnOutput: func(chunk []byte, offset uint64) {
			<-running.callbacksReady
			_ = running.output.Feed(chunk, offset)
		},
		OnOutputEnd: func(offset uint64) {
			<-running.callbacksReady
			_ = running.output.End(offset)
		},
		OnExit: func(info pty.ExitInfo) {
			<-running.callbacksReady
			m.onExit(id, running, info)
		},
	})
	if err != nil {
		startErr := fmt.Errorf("session: start pty: %w", err)
		observation, observationErr := processObservation(
			detect.KindProcessStartFailed,
			m.clock.Now(),
			&detect.ProcessFact{
				ExitKind:     detect.ExitStartupFailed,
				ErrorMessage: startErr.Error(),
			},
		)
		commitErr := observationErr
		if observationErr == nil {
			commitErr = running.observer.Terminate(observation)
		}
		m.detach(id, running)
		close(running.signalReady)
		close(running.callbacksReady)
		running.observer.Close()
		cleanupErr := m.cleanupSignalInjection(id, running.injectionDir)
		return nil, errors.Join(startErr, commitErr, cleanupErr)
	}

	m.mu.Lock()
	running.process = sess
	m.mu.Unlock()

	// 4. 状态推进：进程活着 -> Working。
	started, err := processObservation(
		detect.KindProcessStarted,
		m.clock.Now(),
		&detect.ProcessFact{HookAvailable: running.hasSignalToken},
	)
	if err == nil {
		err = running.observer.Deliver(context.Background(), started)
	}
	if err != nil {
		m.requestStop(id, running, stopCauseShutdown)
		close(running.signalReady)
		close(running.callbacksReady)
		_ = sess.Close()
		running.observer.Close()
		return nil, err
	}
	close(running.signalReady)
	if err := m.waitForRequiredHook(ctx, a, running); err != nil {
		m.invalidateSignal(id, running)
		m.requestStop(id, running, stopCauseShutdown)
		close(running.callbacksReady)
		closeErr := sess.Close()
		return nil, errors.Join(err, closeErr)
	}
	close(running.callbacksReady)

	return m.Status(id)
}

func initialTerminalSize() (term.Size, error) {
	return term.NewSize(initialTerminalRows, initialTerminalColumns)
}

func toPTYSize(size term.Size) (pty.Size, error) {
	return pty.NewSize(size.Rows(), size.Columns())
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
			if item.session.observer != nil {
				item.session.observer.Close()
			}
		}

		m.mu.Lock()
		for _, item := range attached {
			if m.sessions[item.id] == item.session {
				delete(m.sessions, item.id)
			}
		}
		m.mu.Unlock()
		m.committer.Close()
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
	var process processSession
	if attached {
		process = sess.process
	}
	m.mu.RUnlock()
	if !attached {
		if state := a.State(); state == agent.StateDone || state == agent.StateStopped {
			return nil
		}
		return fmt.Errorf("session: agent %q is %s without a PTY", id, a.State())
	}
	if process == nil {
		return fmt.Errorf("session: agent %q is starting without a PTY", id)
	}
	m.requestStop(id, sess, stopCauseUser)
	if err := process.Close(); err != nil && !errors.Is(err, pty.ErrClosed) {
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
	var process processSession
	hookStatus := detect.HookDetached
	if sess != nil {
		process = sess.process
		if sess.observer != nil {
			hookStatus = sess.observer.Snapshot().HookStatus()
		}
	}
	m.mu.RUnlock()

	st := &Status{
		AgentID:        string(a.ID()),
		Name:           a.Name(),
		Vendor:         a.Vendor(),
		Mode:           a.RunMode(),
		State:          a.State(),
		CreatedAt:      a.CreatedAt(),
		UpdatedAt:      a.UpdatedAt(),
		LastError:      a.LastError(),
		HookPolicy:     a.HookPolicy(),
		HookStatus:     hookStatus,
		LastTransition: a.LastTransition(),
	}
	st.SignalInjection = a.SignalInjection()
	st.SignalInjectionStatus, st.SignalInjectionReason = a.SignalInjectionResult()
	if process != nil {
		st.PID = process.PID()
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
	rows, err := m.store.Replay(sessionID)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].Type != string(event.TypeOutputChunk) {
			continue
		}
		if _, err := event.DecodeOutputChunkPayload(rows[i].Payload); err != nil {
			return nil, fmt.Errorf(
				"session: decode output chunk metadata at seq %d: %w",
				rows[i].Seq,
				err,
			)
		}
		if len(rows[i].OutputAttachment) != 0 {
			payload, err := event.HydrateOutputChunkPayload(
				rows[i].Payload,
				rows[i].OutputAttachment,
			)
			if err != nil {
				return nil, fmt.Errorf(
					"session: hydrate output chunk at seq %d: %w",
					rows[i].Seq,
					err,
				)
			}
			rows[i].Payload = payload
		}
		rows[i].OutputAttachment = nil
	}
	return rows, nil
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
	var process processSession
	if attached {
		process = running.process
	}
	m.mu.RUnlock()
	if closed {
		return InputResult{}, ErrManagerClosed
	}
	if !known {
		return InputResult{}, fmt.Errorf("%w: %q", ErrUnknownAgent, id)
	}
	if !attached || process == nil {
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

	written, err := process.Write(data)
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
	if _, err := m.committer.CommitEvents(
		context.Background(),
		[]event.Draft{event.NewAgentInputDraft(string(id), string(id), string(payload))},
	); err != nil {
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

// onExit 根据运行模式和停止原因记录终态，再移除 PTY。
func (m *Manager) onExit(id agent.ID, running *runningSession, info pty.ExitInfo) {
	running.inputMu.Lock()
	defer running.inputMu.Unlock()

	cause, ok := m.claimExit(id, running)
	if !ok {
		return
	}
	defer m.detach(id, running)
	defer m.finalizeSignalInjection(id, running)
	if running.observer == nil {
		return
	}
	defer running.observer.Close()

	exitKind := detect.ExitFailure
	reason := ""
	errorMessage := ""
	switch {
	case cause == stopCauseUser:
		exitKind = detect.ExitStopped
		reason = "user stop"
	case cause == stopCauseShutdown:
		exitKind = detect.ExitStopped
		reason = "manager shutdown"
	case info.Code == 0 && info.Err == nil:
		exitKind = detect.ExitSuccess
	case info.Err != nil:
		errorMessage = info.Err.Error()
	default:
		errorMessage = fmt.Sprintf("process exited code=%d", info.Code)
	}
	exitCode := info.Code
	observation, err := processObservation(
		detect.KindProcessExited,
		m.clock.Now(),
		&detect.ProcessFact{
			ExitCode:     &exitCode,
			ExitKind:     exitKind,
			Reason:       reason,
			ErrorMessage: errorMessage,
		},
	)
	if err != nil {
		return
	}
	_ = running.observer.Terminate(observation)
}

func processObservation(
	kind detect.Kind,
	at time.Time,
	fact *detect.ProcessFact,
) (detect.Observation, error) {
	signal, err := detect.NewProcessSignal(detect.Signal{
		Kind:        kind,
		VendorEvent: string(kind),
		Scope:       detect.ScopeRoot,
		Evidence:    string(kind),
		Confidence:  1,
		ReceivedAt:  at,
		Process:     fact,
	})
	if err != nil {
		return detect.Observation{}, err
	}
	return detect.ObserveSignal(signal)
}

// Fatal 返回运行时持久化或投影失败通知。
func (m *Manager) Fatal() <-chan error {
	return m.committer.Fatal()
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
	running.signalDigest = signalTokenDigest{}
	running.hasSignalToken = false
	return running.stopCause, true
}

func (m *Manager) detach(id agent.ID, running *runningSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[id] == running {
		running.signalDigest = signalTokenDigest{}
		running.hasSignalToken = false
		delete(m.sessions, id)
	}
}

func (m *Manager) invalidateSignal(id agent.ID, running *runningSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[id] == running {
		running.signalDigest = signalTokenDigest{}
		running.hasSignalToken = false
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
