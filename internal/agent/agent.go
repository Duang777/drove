// Package agent 定义 Agent 抽象与状态机。
//
// 本包是全项目唯一的"状态权威"：Agent 的状态只能通过
// Transition 变更，任何组件不得直接修改 Agent 的内部状态字段。
package agent

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ID 是 agent 的稳定标识（UUID 字符串）。
type ID string

// State 表示一个 agent 的运行时状态。
type State string

// RunMode 控制 agent 进程的启动方式和自然退出语义。
type RunMode string

// HookPolicy controls hook activation for one immutable session.
type HookPolicy string

const (
	// RunModeInteractive 启动常驻交互进程。
	RunModeInteractive RunMode = "interactive"
	// RunModeOneshot 启动执行一次后退出的进程。
	RunModeOneshot RunMode = "oneshot"

	// HooksOff disables hook delivery and enables heuristic fallback.
	HooksOff HookPolicy = "off"
	// HooksAuto prefers an observed hook and otherwise enables fallback.
	HooksAuto HookPolicy = "auto"
	// HooksRequired requires a hook signal during startup.
	HooksRequired HookPolicy = "required"

	// StatePending 已创建、尚未启动（初始态）。
	StatePending State = "pending"
	// StateStarting 进程已拉起、尚未就绪（生命周期边界态）。
	StateStarting State = "starting"
	// StateWorking 正在执行任务。
	StateWorking State = "working"
	// StateBlocked 等待输入/工具结果/人工介入，无法自行推进。
	StateBlocked State = "blocked"
	// StateDone 任务完成或已退出且无错误。
	StateDone State = "done"
	// StateIdle 存活但当前无任务。
	StateIdle State = "idle"
	// StateStopped 已停止（生命周期边界态）。
	StateStopped State = "stopped"
)

// EvidenceSource identifies the authority behind one state transition.
type EvidenceSource string

const (
	EvidenceSession   EvidenceSource = "session"
	EvidenceProcess   EvidenceSource = "process"
	EvidenceHook      EvidenceSource = "hook"
	EvidenceHeuristic EvidenceSource = "heuristic"
	EvidenceTimer     EvidenceSource = "timer"
	EvidenceRecovery  EvidenceSource = "recovery"
)

// Evidence is the bounded, redacted explanation for one state transition.
type Evidence struct {
	Source     EvidenceSource `json:"source"`
	Event      string         `json:"event"`
	Confidence float64        `json:"confidence"`
	DeliveryID string         `json:"delivery_id,omitempty"`
}

// Validate checks that evidence can be stored in a versioned state payload.
func (e Evidence) Validate() error {
	switch e.Source {
	case EvidenceSession,
		EvidenceProcess,
		EvidenceHook,
		EvidenceHeuristic,
		EvidenceTimer,
		EvidenceRecovery:
	default:
		return fmt.Errorf("agent: invalid evidence source %q", e.Source)
	}
	if e.Event == "" || len(e.Event) > 64 || !asciiToken(e.Event) {
		return errors.New("agent: evidence event must contain 1 to 64 ASCII bytes")
	}
	if math.IsNaN(e.Confidence) || math.IsInf(e.Confidence, 0) ||
		e.Confidence < 0 || e.Confidence > 1 {
		return fmt.Errorf("agent: invalid evidence confidence %v", e.Confidence)
	}
	if e.Source == EvidenceHook {
		parsed, err := uuid.Parse(e.DeliveryID)
		if err != nil || parsed.String() != e.DeliveryID {
			return errors.New("agent: hook evidence delivery ID must be a canonical UUID")
		}
	} else if e.DeliveryID != "" {
		return errors.New("agent: only hook evidence may contain a delivery ID")
	}
	return nil
}

func asciiToken(value string) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r > 0x7f {
			return false
		}
	}
	return true
}

// transitions 定义合法状态迁移表。
// 语义：from -> to 是否允许。非法迁移一律返回错误。
var transitions = map[State]map[State]bool{
	StatePending: {
		StateStarting: true,
		StateStopped:  true,
	},
	StateStarting: {
		StateWorking: true,
		StateIdle:    true,
		StateStopped: true,
	},
	StateWorking: {
		StateBlocked: true,
		StateDone:    true,
		StateIdle:    true,
		StateStopped: true,
	},
	StateBlocked: {
		StateWorking: true,
		StateDone:    true,
		StateIdle:    true,
		StateStopped: true,
	},
	StateIdle: {
		StateWorking: true,
		StateDone:    true,
		StateStopped: true,
	},
	StateDone: {
		StateStopped: true,
	},
	StateStopped: {}, // 终态，不可再迁移
}

// Valid 报告 s 是否为合法状态值。
func Valid(s State) bool {
	_, ok := transitions[s]
	return ok
}

// ValidRunMode 报告 mode 是否为合法运行模式。
func ValidRunMode(mode RunMode) bool {
	return mode == RunModeInteractive || mode == RunModeOneshot
}

// ValidHookPolicy reports whether policy is supported.
func ValidHookPolicy(policy HookPolicy) bool {
	return policy == HooksOff || policy == HooksAuto || policy == HooksRequired
}

// CanTransition 报告 from -> to 是否合法。
func CanTransition(from, to State) bool {
	m, ok := transitions[from]
	if !ok {
		return false
	}
	return m[to]
}

// CanRecoverTransition accepts historical transitions plus restart
// reconciliation from Done to Stopped.
func CanRecoverTransition(from, to State) bool {
	return CanTransition(from, to) || (from == StateDone && to == StateStopped)
}

var (
	// ErrInvalidTransition 表示状态迁移不合法。
	ErrInvalidTransition = errors.New("agent: invalid state transition")
	// ErrStaleTransitionPlan 表示计划基于的状态版本已过期或不属于目标 Agent。
	ErrStaleTransitionPlan = errors.New("agent: stale transition plan")
)

// TransitionPlan 是持久化前生成、提交后应用的状态迁移计划。
type TransitionPlan struct {
	AgentID  ID
	Revision uint64
	From     State
	To       State
	Reason   string
}

// Snapshot is an immutable view used by decision code.
type Snapshot struct {
	ID       ID
	State    State
	RunMode  RunMode
	Revision uint64
}

// Change describes one state and/or error update before it is prepared.
type Change struct {
	target       State
	reason       string
	errorMessage string
	evidence     Evidence
	at           time.Time
	hasState     bool
	hasError     bool
}

// PreparedChange is bound to one Agent revision and can only be applied once.
type PreparedChange struct {
	owner        *Agent
	agentID      ID
	revision     uint64
	from         State
	to           State
	reason       string
	errorMessage string
	evidence     Evidence
	at           time.Time
	hasState     bool
	hasError     bool
}

// MoveTo constructs a state-only change.
func MoveTo(to State, reason string, evidence Evidence) Change {
	return Change{
		target:   to,
		reason:   reason,
		evidence: evidence,
		at:       time.Now().UTC(),
		hasState: true,
	}
}

// FailTo constructs an atomic error and state change.
func FailTo(to State, reason, message string, evidence Evidence) Change {
	return Change{
		target:       to,
		reason:       reason,
		errorMessage: message,
		evidence:     evidence,
		at:           time.Now().UTC(),
		hasState:     true,
		hasError:     true,
	}
}

// RecordError constructs an error-only change.
func RecordError(message string, evidence Evidence) Change {
	return Change{
		errorMessage: message,
		evidence:     evidence,
		at:           time.Now().UTC(),
		hasError:     true,
	}
}

// Agent 是一个受控的 agent 实例。
// 它只描述状态与元数据，不持有进程/PTY/事件实现。
type Agent struct {
	mu sync.RWMutex

	id             ID
	name           string
	vendor         string // 适配器厂商标识，如 "claude" / "codex" / "generic"
	runMode        RunMode
	hookPolicy     HookPolicy
	state          State
	lastError      string
	lastTransition *Evidence

	createdAt time.Time
	updatedAt time.Time
	revision  uint64
}

// Option 是 Agent 的构建选项。
type Option func(*Agent)

// RestoreSnapshot 是从持久化事件投影出的 Agent 状态。
type RestoreSnapshot struct {
	ID             ID
	Name           string
	Vendor         string
	RunMode        RunMode
	HookPolicy     HookPolicy
	State          State
	LastError      string
	LastTransition *Evidence
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// WithName 设置 agent 显示名。
func WithName(name string) Option {
	return func(a *Agent) { a.name = name }
}

// WithVendor 设置厂商标识。
func WithVendor(vendor string) Option {
	return func(a *Agent) { a.vendor = vendor }
}

// WithRunMode 设置 agent 运行模式。
func WithRunMode(mode RunMode) Option {
	return func(a *Agent) { a.runMode = mode }
}

// WithHookPolicy sets the immutable hook policy for a new Agent.
func WithHookPolicy(policy HookPolicy) Option {
	return func(a *Agent) { a.hookPolicy = policy }
}

// New 创建处于 StatePending 的 agent。
func New(id ID, opts ...Option) *Agent {
	a := &Agent{
		id:         id,
		runMode:    RunModeInteractive,
		hookPolicy: HooksOff,
		state:      StatePending,
		createdAt:  time.Now().UTC(),
		updatedAt:  time.Now().UTC(),
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Restore 从已验证的持久化快照构造 Agent，不触发状态变更回调。
func Restore(snapshot RestoreSnapshot, opts ...Option) (*Agent, error) {
	hookPolicy := snapshot.HookPolicy
	if hookPolicy == "" {
		hookPolicy = HooksOff
	}
	a := &Agent{
		id:             snapshot.ID,
		name:           snapshot.Name,
		vendor:         snapshot.Vendor,
		runMode:        snapshot.RunMode,
		hookPolicy:     hookPolicy,
		state:          snapshot.State,
		lastError:      snapshot.LastError,
		lastTransition: cloneEvidence(snapshot.LastTransition),
		createdAt:      snapshot.CreatedAt,
		updatedAt:      snapshot.UpdatedAt,
	}
	for _, option := range opts {
		option(a)
	}
	if err := validateRestoredAgent(a); err != nil {
		return nil, err
	}
	return a, nil
}

func validateRestoredAgent(a *Agent) error {
	if strings.TrimSpace(string(a.id)) == "" {
		return errors.New("agent: restore: ID is required")
	}
	if strings.TrimSpace(a.name) == "" {
		return errors.New("agent: restore: name is required")
	}
	if strings.TrimSpace(a.vendor) == "" {
		return errors.New("agent: restore: vendor is required")
	}
	if !ValidRunMode(a.runMode) {
		return fmt.Errorf("agent: restore: invalid run mode %q", a.runMode)
	}
	if !ValidHookPolicy(a.hookPolicy) {
		return fmt.Errorf("agent: restore: invalid hook policy %q", a.hookPolicy)
	}
	if !Valid(a.state) {
		return fmt.Errorf("agent: restore: invalid state %q", a.state)
	}
	if a.createdAt.IsZero() {
		return errors.New("agent: restore: creation time is required")
	}
	if a.updatedAt.IsZero() {
		return errors.New("agent: restore: update time is required")
	}
	if a.updatedAt.Before(a.createdAt) {
		return errors.New("agent: restore: update time is before creation time")
	}
	if a.lastTransition != nil {
		if err := a.lastTransition.Validate(); err != nil {
			return fmt.Errorf("agent: restore: last transition: %w", err)
		}
	}
	return nil
}

func cloneEvidence(evidence *Evidence) *Evidence {
	if evidence == nil {
		return nil
	}
	copy := *evidence
	return &copy
}

// ID 返回 agent 标识。
func (a *Agent) ID() ID { return a.id }

// Name 返回显示名。
func (a *Agent) Name() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.name
}

// Vendor 返回厂商标识。
func (a *Agent) Vendor() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.vendor
}

// RunMode 返回 agent 的运行模式。
func (a *Agent) RunMode() RunMode {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.runMode
}

// HookPolicy returns the immutable session hook policy.
func (a *Agent) HookPolicy() HookPolicy {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.hookPolicy
}

// State 返回当前状态（读取权威入口）。
func (a *Agent) State() State {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.state
}

// LastError 返回最近一次错误描述（无则为空）。
func (a *Agent) LastError() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.lastError
}

// LastTransition returns a copy of the newest understood state evidence.
func (a *Agent) LastTransition() *Evidence {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return cloneEvidence(a.lastTransition)
}

// Snapshot returns the state fields used to prepare a deterministic decision.
func (a *Agent) Snapshot() Snapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return Snapshot{
		ID:       a.id,
		State:    a.state,
		RunMode:  a.runMode,
		Revision: a.revision,
	}
}

// Prepare validates a change without mutating the Agent.
func (a *Agent) Prepare(change Change) (PreparedChange, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if !change.hasState && !change.hasError {
		return PreparedChange{}, errors.New("agent: empty change")
	}
	if change.at.IsZero() {
		return PreparedChange{}, errors.New("agent: change timestamp is required")
	}
	if err := change.evidence.Validate(); err != nil {
		return PreparedChange{}, fmt.Errorf("agent: change evidence: %w", err)
	}
	if change.hasError && strings.TrimSpace(change.errorMessage) == "" {
		return PreparedChange{}, errors.New("agent: error message is required")
	}
	if change.hasState {
		if strings.TrimSpace(change.reason) == "" {
			return PreparedChange{}, errors.New("agent: transition reason is required")
		}
		if !CanTransition(a.state, change.target) {
			return PreparedChange{}, fmt.Errorf(
				"%w: %s -> %s",
				ErrInvalidTransition,
				a.state,
				change.target,
			)
		}
		if change.target == StateDone &&
			(a.runMode != RunModeOneshot ||
				change.evidence.Source != EvidenceProcess ||
				change.evidence.Event != "process_exited") {
			return PreparedChange{}, errors.New(
				"agent: done requires a successful natural oneshot process exit",
			)
		}
	}
	return PreparedChange{
		owner:        a,
		agentID:      a.id,
		revision:     a.revision,
		from:         a.state,
		to:           change.target,
		reason:       change.reason,
		errorMessage: change.errorMessage,
		evidence:     change.evidence,
		at:           change.at,
		hasState:     change.hasState,
		hasError:     change.hasError,
	}, nil
}

// ApplyCommitted applies a previously prepared change after durable commit.
func (a *Agent) ApplyCommitted(prepared PreparedChange) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if prepared.owner != a ||
		prepared.agentID != a.id ||
		prepared.revision != a.revision ||
		prepared.from != a.state {
		return fmt.Errorf(
			"%w: agent=%q revision=%d state=%s",
			ErrStaleTransitionPlan,
			a.id,
			a.revision,
			a.state,
		)
	}
	if prepared.hasState && !CanTransition(prepared.from, prepared.to) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, prepared.from, prepared.to)
	}
	if prepared.hasError {
		a.lastError = prepared.errorMessage
	}
	if prepared.hasState {
		a.state = prepared.to
		evidence := prepared.evidence
		a.lastTransition = &evidence
	}
	a.updatedAt = prepared.at
	a.revision++
	return nil
}

// Transition exposes the prepared state transition data.
func (p PreparedChange) Transition() (
	from State,
	to State,
	reason string,
	evidence Evidence,
	ok bool,
) {
	return p.from, p.to, p.reason, p.evidence, p.hasState
}

// ErrorMessage returns the prepared error update when present.
func (p PreparedChange) ErrorMessage() (string, bool) {
	return p.errorMessage, p.hasError
}

// Timestamp returns the timestamp shared by the durable events and projection.
func (p PreparedChange) Timestamp() time.Time {
	return p.at
}

// PlanTransition 验证并生成不会立即改变状态的迁移计划。
func (a *Agent) PlanTransition(to State, reason string) (TransitionPlan, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !CanTransition(a.state, to) {
		return TransitionPlan{}, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, a.state, to)
	}
	return TransitionPlan{
		AgentID:  a.id,
		Revision: a.revision,
		From:     a.state,
		To:       to,
		Reason:   reason,
	}, nil
}

// ValidateTransitionPlan 确认计划仍基于当前权威状态。
func (a *Agent) ValidateTransitionPlan(plan TransitionPlan) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.validateTransitionPlan(plan)
}

// ApplyTransition 在事件已持久化后应用计划。
func (a *Agent) ApplyTransition(plan TransitionPlan, at time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.validateTransitionPlan(plan); err != nil {
		return err
	}
	if at.IsZero() {
		return errors.New("agent: transition time is required")
	}
	a.state = plan.To
	a.updatedAt = at
	a.revision++
	return nil
}

func (a *Agent) validateTransitionPlan(plan TransitionPlan) error {
	if plan.AgentID != a.id || plan.Revision != a.revision || plan.From != a.state {
		return fmt.Errorf(
			"%w: agent=%q revision=%d state=%s",
			ErrStaleTransitionPlan,
			a.id,
			a.revision,
			a.state,
		)
	}
	if !CanTransition(plan.From, plan.To) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, plan.From, plan.To)
	}
	return nil
}

// Transition 立即应用一个迁移，供状态机的独立使用者调用。
func (a *Agent) Transition(to State, reason string) error {
	plan, err := a.PlanTransition(to, reason)
	if err != nil {
		return err
	}
	return a.ApplyTransition(plan, time.Now().UTC())
}

// SetError 记录 agent 的错误信息（不改变状态）。
func (a *Agent) SetError(errMsg string) {
	a.SetErrorAt(errMsg, time.Now().UTC())
}

// SetErrorAt 在事件提交后使用同一时间记录错误信息。
func (a *Agent) SetErrorAt(errMsg string, at time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastError = errMsg
	a.updatedAt = at
}

// CreatedAt / UpdatedAt 返回时间戳。
func (a *Agent) CreatedAt() time.Time {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.createdAt
}

func (a *Agent) UpdatedAt() time.Time {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.updatedAt
}
