// Package agent 定义 Agent 抽象与状态机。
//
// 本包是全项目唯一的"状态权威"：Agent 的状态只能通过已提交的 Change
// 变更，任何组件不得直接修改 Agent 的内部状态字段。
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

// SignalInjectionMode controls process-local signal configuration.
type SignalInjectionMode string

// SignalInjectionStatus records the launch-time injection result.
type SignalInjectionStatus string

// SignalInjectionReason explains one bounded injection result.
type SignalInjectionReason string

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

	// SignalInjectionAuto enables a supported adapter injection plan.
	SignalInjectionAuto SignalInjectionMode = "auto"
	// SignalInjectionOff leaves the vendor command unchanged.
	SignalInjectionOff SignalInjectionMode = "off"

	// InjectionOff means no injection was requested.
	InjectionOff SignalInjectionStatus = "off"
	// InjectionInjected means the session plan was applied.
	InjectionInjected SignalInjectionStatus = "injected"
	// InjectionSkipped means auto injection could not be applied safely.
	InjectionSkipped SignalInjectionStatus = "skipped"
	// InjectionDetached means recovered metadata has no live injected process.
	InjectionDetached SignalInjectionStatus = "detached"

	// InjectionReasonHookPolicyOff means hooks disabled the relay.
	InjectionReasonHookPolicyOff SignalInjectionReason = "hook_policy_off"
	// InjectionReasonConfiguredOff means configuration disabled injection.
	InjectionReasonConfiguredOff SignalInjectionReason = "configured_off"
	// InjectionReasonUnsupported means the adapter has no injection capability.
	InjectionReasonUnsupported SignalInjectionReason = "unsupported"
	// InjectionReasonRelayUnavailable means no Drove relay executable was found.
	InjectionReasonRelayUnavailable SignalInjectionReason = "relay_unavailable"
	// InjectionReasonArgumentConflict means caller arguments own the setting.
	InjectionReasonArgumentConflict SignalInjectionReason = "argument_conflict"
	// InjectionReasonSessionConfig means a session plan was materialized.
	InjectionReasonSessionConfig SignalInjectionReason = "session_config"
	// InjectionReasonRecovered means no original runtime remains after restart.
	InjectionReasonRecovered SignalInjectionReason = "recovered"

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
	EvidenceNotify    EvidenceSource = "notify"
	EvidenceHeuristic EvidenceSource = "heuristic"
	EvidenceScreen    EvidenceSource = "screen"
	EvidenceTimer     EvidenceSource = "timer"
	EvidenceRecovery  EvidenceSource = "recovery"
)

// ScreenEdge describes whether a stable screen rule appeared or cleared.
type ScreenEdge string

const (
	// ScreenEdgePresent means a stable rule currently matches the screen.
	ScreenEdgePresent ScreenEdge = "present"
	// ScreenEdgeCleared means a previously matching rule no longer matches.
	ScreenEdgeCleared ScreenEdge = "cleared"
)

// ScreenAttribution is bounded adapter-authored evidence for a screen edge.
type ScreenAttribution struct {
	Rule          string     `json:"rule"`
	Edge          ScreenEdge `json:"edge"`
	Region        string     `json:"region"`
	OutputOffset  uint64     `json:"output_offset"`
	LastOutputSeq uint64     `json:"last_output_seq"`
	Evidence      string     `json:"evidence"`
}

// NewScreenAttribution validates and copies one screen attribution value.
func NewScreenAttribution(
	rule string,
	edge ScreenEdge,
	region string,
	outputOffset uint64,
	lastOutputSeq uint64,
	evidence string,
) (ScreenAttribution, error) {
	attribution := ScreenAttribution{
		Rule:          rule,
		Edge:          edge,
		Region:        region,
		OutputOffset:  outputOffset,
		LastOutputSeq: lastOutputSeq,
		Evidence:      evidence,
	}
	if err := attribution.Validate(); err != nil {
		return ScreenAttribution{}, err
	}
	return attribution, nil
}

// Validate rejects unbounded or non-static screen attribution.
func (a ScreenAttribution) Validate() error {
	if a.Rule == "" || len(a.Rule) > 64 || !asciiToken(a.Rule) {
		return errors.New("agent: screen rule must contain 1 to 64 ASCII bytes")
	}
	switch a.Edge {
	case ScreenEdgePresent, ScreenEdgeCleared:
	default:
		return fmt.Errorf("agent: invalid screen edge %q", a.Edge)
	}
	if a.Region == "" || len(a.Region) > 64 || !asciiToken(a.Region) {
		return errors.New("agent: screen region must contain 1 to 64 ASCII bytes")
	}
	if a.OutputOffset == 0 {
		return errors.New("agent: screen output offset must be positive")
	}
	if a.LastOutputSeq == 0 {
		return errors.New("agent: screen last output sequence must be positive")
	}
	if a.Evidence == "" || len(a.Evidence) > 128 || !asciiText(a.Evidence) {
		return errors.New("agent: screen evidence must contain 1 to 128 printable ASCII bytes")
	}
	return nil
}

// TerminalAttribution identifies a redacted signal in committed terminal output.
type TerminalAttribution struct {
	Protocol      string `json:"protocol"`
	OutputOffset  uint64 `json:"output_offset"`
	LastOutputSeq uint64 `json:"last_output_seq"`
}

// NewTerminalAttribution validates one committed terminal location.
func NewTerminalAttribution(
	protocol string,
	outputOffset uint64,
	lastOutputSeq uint64,
) (TerminalAttribution, error) {
	attribution := TerminalAttribution{
		Protocol:      protocol,
		OutputOffset:  outputOffset,
		LastOutputSeq: lastOutputSeq,
	}
	if err := attribution.Validate(); err != nil {
		return TerminalAttribution{}, err
	}
	return attribution, nil
}

// Validate rejects unsupported protocols and incomplete committed locations.
func (a TerminalAttribution) Validate() error {
	if a.Protocol != "osc9" {
		return fmt.Errorf("agent: invalid terminal protocol %q", a.Protocol)
	}
	if a.OutputOffset == 0 {
		return errors.New("agent: terminal output offset must be positive")
	}
	if a.LastOutputSeq == 0 {
		return errors.New("agent: terminal last output sequence must be positive")
	}
	return nil
}

// Evidence is the bounded, redacted explanation for one state transition.
type Evidence struct {
	Source     EvidenceSource       `json:"source"`
	Event      string               `json:"event"`
	Confidence float64              `json:"confidence"`
	DeliveryID string               `json:"delivery_id,omitempty"`
	Screen     *ScreenAttribution   `json:"screen,omitempty"`
	Terminal   *TerminalAttribution `json:"terminal,omitempty"`
}

// Validate checks that evidence can be stored in a versioned state payload.
func (e Evidence) Validate() error {
	switch e.Source {
	case EvidenceSession,
		EvidenceProcess,
		EvidenceHook,
		EvidenceNotify,
		EvidenceHeuristic,
		EvidenceScreen,
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
	if e.Source == EvidenceHook ||
		e.Source == EvidenceNotify && e.Terminal == nil {
		parsed, err := uuid.Parse(e.DeliveryID)
		if err != nil || parsed.String() != e.DeliveryID {
			return errors.New(
				"agent: delivered evidence ID must be a canonical UUID",
			)
		}
	} else if e.Source == EvidenceNotify {
		if e.DeliveryID != "" {
			return errors.New(
				"agent: terminal notify evidence cannot contain a delivery ID",
			)
		}
	} else if e.DeliveryID != "" {
		return errors.New("agent: only delivered evidence may contain a delivery ID")
	}
	if e.Source == EvidenceScreen {
		if e.Screen == nil {
			return errors.New("agent: screen evidence requires screen attribution")
		}
		if err := e.Screen.Validate(); err != nil {
			return fmt.Errorf("agent: screen evidence attribution: %w", err)
		}
		if e.Event != e.Screen.Rule {
			return errors.New("agent: screen evidence event must match its rule")
		}
	} else if e.Screen != nil {
		return errors.New("agent: only screen evidence may contain screen attribution")
	}
	if e.Terminal != nil {
		if e.Source != EvidenceNotify {
			return errors.New(
				"agent: only notify evidence may contain terminal attribution",
			)
		}
		if err := e.Terminal.Validate(); err != nil {
			return fmt.Errorf("agent: terminal evidence attribution: %w", err)
		}
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

func asciiText(value string) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r > 0x7e {
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
		StateBlocked: true,
		StateDone:    true,
		StateStopped: true,
	},
	StateDone:    {},
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

// ValidSignalInjectionMode reports whether mode is supported.
func ValidSignalInjectionMode(mode SignalInjectionMode) bool {
	return mode == SignalInjectionAuto || mode == SignalInjectionOff
}

// ValidSignalInjectionResult reports whether a status and reason can coexist.
func ValidSignalInjectionResult(
	status SignalInjectionStatus,
	reason SignalInjectionReason,
) bool {
	switch status {
	case InjectionOff:
		return reason == InjectionReasonHookPolicyOff ||
			reason == InjectionReasonConfiguredOff
	case InjectionInjected:
		return reason == InjectionReasonSessionConfig
	case InjectionSkipped:
		return reason == InjectionReasonUnsupported ||
			reason == InjectionReasonRelayUnavailable ||
			reason == InjectionReasonArgumentConflict
	case InjectionDetached:
		return reason == InjectionReasonRecovered
	default:
		return false
	}
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
	resume       bool
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
	resume       bool
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

// ResumeToStarting constructs the only change allowed to reopen a stopped Agent.
func ResumeToStarting(reason string, evidence Evidence) Change {
	return Change{
		target:   StateStarting,
		reason:   reason,
		evidence: evidence,
		at:       time.Now().UTC(),
		hasState: true,
		resume:   true,
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

	id              ID
	name            string
	vendor          string // 适配器厂商标识，如 "claude" / "codex" / "generic"
	workingDir      string
	runMode         RunMode
	hookPolicy      HookPolicy
	signalInjection SignalInjectionMode
	injectionStatus SignalInjectionStatus
	injectionReason SignalInjectionReason
	state           State
	lastError       string
	lastTransition  *Evidence

	createdAt time.Time
	updatedAt time.Time
	revision  uint64
}

// Option 是 Agent 的构建选项。
type Option func(*Agent)

// RestoreSnapshot 是从持久化事件投影出的 Agent 状态。
type RestoreSnapshot struct {
	ID              ID
	Name            string
	Vendor          string
	WorkingDir      string
	RunMode         RunMode
	HookPolicy      HookPolicy
	SignalInjection SignalInjectionMode
	InjectionStatus SignalInjectionStatus
	InjectionReason SignalInjectionReason
	State           State
	LastError       string
	LastTransition  *Evidence
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// WithName 设置 agent 显示名。
func WithName(name string) Option {
	return func(a *Agent) { a.name = name }
}

// WithVendor 设置厂商标识。
func WithVendor(vendor string) Option {
	return func(a *Agent) { a.vendor = vendor }
}

// WithWorkingDir sets the immutable process working directory.
func WithWorkingDir(dir string) Option {
	return func(a *Agent) { a.workingDir = dir }
}

// WithRunMode 设置 agent 运行模式。
func WithRunMode(mode RunMode) Option {
	return func(a *Agent) { a.runMode = mode }
}

// WithHookPolicy sets the immutable hook policy for a new Agent.
func WithHookPolicy(policy HookPolicy) Option {
	return func(a *Agent) { a.hookPolicy = policy }
}

// WithSignalInjection sets immutable launch injection metadata.
func WithSignalInjection(
	mode SignalInjectionMode,
	status SignalInjectionStatus,
	reason SignalInjectionReason,
) Option {
	return func(a *Agent) {
		a.signalInjection = mode
		a.injectionStatus = status
		a.injectionReason = reason
	}
}

// New 创建处于 StatePending 的 agent。
func New(id ID, opts ...Option) *Agent {
	a := &Agent{
		id:              id,
		runMode:         RunModeInteractive,
		hookPolicy:      HooksOff,
		signalInjection: SignalInjectionOff,
		injectionStatus: InjectionOff,
		injectionReason: InjectionReasonConfiguredOff,
		state:           StatePending,
		createdAt:       time.Now().UTC(),
		updatedAt:       time.Now().UTC(),
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
	signalInjection := snapshot.SignalInjection
	injectionStatus := snapshot.InjectionStatus
	injectionReason := snapshot.InjectionReason
	if signalInjection == "" {
		signalInjection = SignalInjectionOff
	}
	if injectionStatus == "" && injectionReason == "" {
		injectionStatus = InjectionOff
		injectionReason = InjectionReasonConfiguredOff
	}
	a := &Agent{
		id:              snapshot.ID,
		name:            snapshot.Name,
		vendor:          snapshot.Vendor,
		workingDir:      snapshot.WorkingDir,
		runMode:         snapshot.RunMode,
		hookPolicy:      hookPolicy,
		signalInjection: signalInjection,
		injectionStatus: injectionStatus,
		injectionReason: injectionReason,
		state:           snapshot.State,
		lastError:       snapshot.LastError,
		lastTransition:  cloneEvidence(snapshot.LastTransition),
		createdAt:       snapshot.CreatedAt,
		updatedAt:       snapshot.UpdatedAt,
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
	if !ValidSignalInjectionMode(a.signalInjection) {
		return fmt.Errorf(
			"agent: restore: invalid signal injection mode %q",
			a.signalInjection,
		)
	}
	if !ValidSignalInjectionResult(a.injectionStatus, a.injectionReason) {
		return fmt.Errorf(
			"agent: restore: invalid signal injection result %q/%q",
			a.injectionStatus,
			a.injectionReason,
		)
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
	if evidence.Screen != nil {
		screen := *evidence.Screen
		copy.Screen = &screen
	}
	if evidence.Terminal != nil {
		terminal := *evidence.Terminal
		copy.Terminal = &terminal
	}
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

// WorkingDir returns the immutable process working directory.
func (a *Agent) WorkingDir() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.workingDir
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

// SignalInjection returns the immutable requested injection mode.
func (a *Agent) SignalInjection() SignalInjectionMode {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.signalInjection
}

// SignalInjectionResult returns immutable launch injection metadata.
func (a *Agent) SignalInjectionResult() (
	SignalInjectionStatus,
	SignalInjectionReason,
) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.injectionStatus, a.injectionReason
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
		if !validPreparedTransition(a.state, change.target, change.resume) {
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
	copiedEvidence := *cloneEvidence(&change.evidence)
	return PreparedChange{
		owner:        a,
		agentID:      a.id,
		revision:     a.revision,
		from:         a.state,
		to:           change.target,
		reason:       change.reason,
		errorMessage: change.errorMessage,
		evidence:     copiedEvidence,
		at:           change.at,
		hasState:     change.hasState,
		hasError:     change.hasError,
		resume:       change.resume,
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
	if prepared.hasState &&
		!validPreparedTransition(prepared.from, prepared.to, prepared.resume) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, prepared.from, prepared.to)
	}
	if prepared.hasError {
		a.lastError = prepared.errorMessage
	}
	if prepared.hasState {
		a.state = prepared.to
		a.lastTransition = cloneEvidence(&prepared.evidence)
	}
	a.updatedAt = prepared.at
	a.revision++
	return nil
}

func validPreparedTransition(from, to State, resume bool) bool {
	if resume {
		return from == StateStopped && to == StateStarting
	}
	return CanTransition(from, to)
}

// Transition exposes the prepared state transition data.
func (p PreparedChange) Transition() (
	from State,
	to State,
	reason string,
	evidence Evidence,
	ok bool,
) {
	evidence = *cloneEvidence(&p.evidence)
	return p.from, p.to, p.reason, evidence, p.hasState
}

// ErrorMessage returns the prepared error update when present.
func (p PreparedChange) ErrorMessage() (string, bool) {
	return p.errorMessage, p.hasError
}

// Timestamp returns the timestamp shared by the durable events and projection.
func (p PreparedChange) Timestamp() time.Time {
	return p.at
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
