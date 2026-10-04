// Package detect turns normalized observations into deterministic state decisions.
package detect

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Duang777/drove/internal/agent"
)

const (
	signalVersion        = 1
	maxVendorBytes       = 64
	maxVendorEventBytes  = 64
	maxVendorIDBytes     = 256
	maxNotificationBytes = 64
	maxEvidenceBytes     = 128
)

// Source identifies the authority that produced a signal.
type Source string

const (
	SourceProcess   Source = "process"
	SourceHook      Source = "hook"
	SourceNotify    Source = "notify"
	SourceHeuristic Source = "heuristic"
	SourceScreen    Source = "screen"
	SourceTimer     Source = "timer"
)

// HookStatus describes the observed hook authority for one attached session.
type HookStatus string

const (
	HookOff            HookStatus = "off"
	HookAwaiting       HookStatus = "awaiting_hook"
	HookFallback       HookStatus = "fallback"
	HookActive         HookStatus = "hook_active"
	HookRequiredFailed HookStatus = "required_failed"
	HookDetached       HookStatus = "detached"
)

// Kind is the vendor-neutral meaning of one signal.
type Kind string

const (
	KindSessionStarted        Kind = "session_started"
	KindObserved              Kind = "observed"
	KindTurnStarted           Kind = "turn_started"
	KindToolActivity          Kind = "tool_activity"
	KindHumanInputRequired    Kind = "human_input_required"
	KindHumanInputResolved    Kind = "human_input_resolved"
	KindPermissionRequested   Kind = "permission_requested"
	KindPermissionResolved    Kind = "permission_resolved"
	KindTurnStopped           Kind = "turn_stopped"
	KindTurnFailed            Kind = "turn_failed"
	KindInterrupted           Kind = "interrupted"
	KindIdlePrompt            Kind = "idle_prompt"
	KindSessionEnded          Kind = "session_ended"
	KindSubagentStarted       Kind = "subagent_started"
	KindSubagentStopped       Kind = "subagent_stopped"
	KindTaskCompleted         Kind = "task_completed"
	KindOutputActivity        Kind = "output_activity"
	KindHeuristicBlocked      Kind = "heuristic_blocked"
	KindProcessStarted        Kind = "process_started"
	KindProcessStartFailed    Kind = "process_start_failed"
	KindProcessExited         Kind = "process_exited"
	KindHookActivationExpired Kind = "hook_activation_expired"
	KindTimerFired            Kind = "timer_fired"
)

// Scope distinguishes the root session from a vendor subagent.
type Scope string

const (
	ScopeRoot     Scope = "root"
	ScopeSubagent Scope = "subagent"
)

// ExitKind is the bounded process result written to signal audit events.
type ExitKind string

const (
	ExitSuccess       ExitKind = "success"
	ExitFailure       ExitKind = "failure"
	ExitStopped       ExitKind = "stopped"
	ExitStartupFailed ExitKind = "startup_failed"
)

// ProcessFact contains process-only decision data. ErrorMessage is never part
// of the signal audit payload.
type ProcessFact struct {
	ExitCode      *int
	ExitKind      ExitKind
	Reason        string
	ErrorMessage  string
	HookAvailable bool
}

// Signal is a bounded, redacted observation suitable for durable audit.
type Signal struct {
	Version         int
	Source          Source
	Kind            Kind
	Vendor          string
	VendorEvent     string
	Scope           Scope
	VendorSessionID string
	VendorTurnID    string
	Notification    string
	Evidence        string
	Confidence      float64
	OccurredAt      time.Time
	ReceivedAt      time.Time
	DeliveryID      string
	Timer           TimerRef
	Process         *ProcessFact
	Screen          *agent.ScreenAttribution
}

// NewHookSignal validates and copies a normalized vendor hook signal.
func NewHookSignal(signal Signal) (Signal, error) {
	signal.Version = signalVersion
	signal.Source = SourceHook
	signal.Process = nil
	signal.Timer = TimerRef{}
	if err := signal.validate(); err != nil {
		return Signal{}, err
	}
	return cloneSignal(signal), nil
}

// NewNotifySignal validates a non-authoritative vendor notification.
func NewNotifySignal(signal Signal) (Signal, error) {
	signal.Version = signalVersion
	signal.Source = SourceNotify
	signal.Process = nil
	signal.Timer = TimerRef{}
	if err := signal.validate(); err != nil {
		return Signal{}, err
	}
	return cloneSignal(signal), nil
}

// NewHeuristicSignal validates and copies a terminal heuristic signal.
func NewHeuristicSignal(signal Signal) (Signal, error) {
	signal.Version = signalVersion
	signal.Source = SourceHeuristic
	signal.Process = nil
	signal.Timer = TimerRef{}
	signal.DeliveryID = ""
	if err := signal.validate(); err != nil {
		return Signal{}, err
	}
	return cloneSignal(signal), nil
}

// NewProcessSignal validates and copies a process fact.
func NewProcessSignal(signal Signal) (Signal, error) {
	signal.Version = signalVersion
	signal.Source = SourceProcess
	signal.Scope = ScopeRoot
	signal.DeliveryID = ""
	signal.Timer = TimerRef{}
	if err := signal.validate(); err != nil {
		return Signal{}, err
	}
	return cloneSignal(signal), nil
}

// NewScreenSignal validates and copies one normalized screen rule edge.
func NewScreenSignal(signal Signal) (Signal, error) {
	signal.Version = signalVersion
	signal.Source = SourceScreen
	signal.VendorEvent = "screen_rule"
	signal.Scope = ScopeRoot
	signal.DeliveryID = ""
	signal.Timer = TimerRef{}
	signal.Process = nil
	if err := signal.validate(); err != nil {
		return Signal{}, err
	}
	return cloneSignal(signal), nil
}

func newTimerSignal(kind Kind, event string, ref TimerRef, at time.Time) Signal {
	return Signal{
		Version:     signalVersion,
		Source:      SourceTimer,
		Kind:        kind,
		VendorEvent: event,
		Scope:       ScopeRoot,
		Evidence:    event,
		Confidence:  1,
		ReceivedAt:  at,
		Timer:       ref,
	}
}

func (s Signal) validate() error {
	if s.Version != signalVersion {
		return fmt.Errorf("detect: unsupported signal version %d", s.Version)
	}
	if !validSource(s.Source) {
		return fmt.Errorf("detect: invalid signal source %q", s.Source)
	}
	if !validKind(s.Kind) {
		return fmt.Errorf("detect: invalid signal kind %q", s.Kind)
	}
	if s.Scope != ScopeRoot && s.Scope != ScopeSubagent {
		return fmt.Errorf("detect: invalid signal scope %q", s.Scope)
	}
	if s.VendorEvent == "" || len(s.VendorEvent) > maxVendorEventBytes ||
		!asciiToken(s.VendorEvent) {
		return errors.New("detect: vendor event must contain 1 to 64 ASCII bytes")
	}
	if len(s.Vendor) > maxVendorBytes ||
		len(s.VendorSessionID) > maxVendorIDBytes ||
		len(s.VendorTurnID) > maxVendorIDBytes ||
		len(s.Notification) > maxNotificationBytes ||
		len(s.Evidence) > maxEvidenceBytes {
		return errors.New("detect: signal metadata exceeds its size limit")
	}
	if math.IsNaN(s.Confidence) || math.IsInf(s.Confidence, 0) ||
		s.Confidence < 0 || s.Confidence > 1 {
		return fmt.Errorf("detect: invalid signal confidence %v", s.Confidence)
	}
	if s.ReceivedAt.IsZero() {
		return errors.New("detect: signal receive time is required")
	}
	if s.Source != SourceScreen && s.Screen != nil {
		return errors.New("detect: only screen signals may contain screen attribution")
	}

	switch s.Source {
	case SourceHook:
		if s.Vendor == "" {
			return errors.New("detect: hook signal vendor is required")
		}
		if !canonicalUUID(s.DeliveryID) {
			return errors.New("detect: hook delivery ID must be a canonical UUID")
		}
		if !validHookKind(s.Kind) {
			return fmt.Errorf("detect: hook source cannot report kind %q", s.Kind)
		}
		if s.Process != nil || !s.Timer.zero() {
			return errors.New("detect: hook signal contains non-hook metadata")
		}
	case SourceNotify:
		if s.Vendor == "" {
			return errors.New("detect: notify signal vendor is required")
		}
		if !canonicalUUID(s.DeliveryID) {
			return errors.New("detect: notify delivery ID must be a canonical UUID")
		}
		if s.Scope != ScopeRoot || s.Kind != KindTurnStopped {
			return fmt.Errorf(
				"detect: notify source cannot report %q at scope %q",
				s.Kind,
				s.Scope,
			)
		}
		if s.Process != nil || !s.Timer.zero() {
			return errors.New("detect: notify signal contains foreign metadata")
		}
	case SourceHeuristic:
		if s.DeliveryID != "" || s.Process != nil || !s.Timer.zero() {
			return errors.New("detect: heuristic signal contains foreign metadata")
		}
		if s.Kind != KindHeuristicBlocked &&
			s.Kind != KindTaskCompleted &&
			s.Kind != KindObserved {
			return fmt.Errorf("detect: heuristic source cannot report kind %q", s.Kind)
		}
	case SourceProcess:
		if s.DeliveryID != "" || s.Process == nil {
			return errors.New("detect: process signal requires only process metadata")
		}
		if s.Kind != KindProcessStarted &&
			s.Kind != KindProcessStartFailed &&
			s.Kind != KindProcessExited {
			return fmt.Errorf("detect: process source cannot report kind %q", s.Kind)
		}
		if err := s.Process.validate(s.Kind); err != nil {
			return err
		}
	case SourceScreen:
		if s.Vendor == "" || len(s.Vendor) > maxVendorBytes || !asciiToken(s.Vendor) {
			return errors.New("detect: screen signal vendor must be a stable ASCII token")
		}
		if s.Scope != ScopeRoot {
			return errors.New("detect: screen signal must target the root scope")
		}
		if s.DeliveryID != "" || s.Process != nil || !s.Timer.zero() {
			return errors.New("detect: screen signal contains foreign metadata")
		}
		if s.Screen == nil {
			return errors.New("detect: screen signal requires screen attribution")
		}
		if err := s.Screen.Validate(); err != nil {
			return fmt.Errorf("detect: screen attribution: %w", err)
		}
		if s.VendorEvent != "screen_rule" {
			return errors.New("detect: screen signal vendor event must be screen_rule")
		}
		if s.Evidence != "" {
			return errors.New("detect: screen signal evidence belongs in its attribution")
		}
		if !validScreenKind(s.Kind) {
			return fmt.Errorf("detect: screen source cannot report kind %q", s.Kind)
		}
		if !screenKindMatchesEdge(s.Kind, s.Screen.Edge) {
			return fmt.Errorf(
				"detect: screen kind %q cannot report edge %q",
				s.Kind,
				s.Screen.Edge,
			)
		}
		if !strings.HasPrefix(s.Screen.Rule, s.Vendor+".") {
			return errors.New("detect: screen rule must belong to its vendor")
		}
		if knownScreenRule(s.Screen.Rule) &&
			!knownScreenRuleMatchesKind(s.Screen.Rule, s.Kind) {
			return errors.New("detect: known screen rule has an incompatible kind")
		}
	case SourceTimer:
		if s.DeliveryID != "" || s.Process != nil {
			return errors.New("detect: timer signal requires a generation only")
		}
		if err := s.Timer.validate(); err != nil {
			return fmt.Errorf("detect: timer signal: %w", err)
		}
		if s.Kind != KindHookActivationExpired && s.Kind != KindTimerFired {
			return fmt.Errorf("detect: timer source cannot report kind %q", s.Kind)
		}
	}
	return nil
}

func (p ProcessFact) validate(kind Kind) error {
	switch kind {
	case KindProcessStarted:
		if p.ExitCode != nil || p.ExitKind != "" || p.Reason != "" ||
			p.ErrorMessage != "" {
			return errors.New("detect: process start contains exit metadata")
		}
	case KindProcessStartFailed:
		if p.ExitCode != nil || p.ExitKind != ExitStartupFailed || p.Reason != "" ||
			strings.TrimSpace(p.ErrorMessage) == "" {
			return errors.New("detect: process start failure metadata is incomplete")
		}
	case KindProcessExited:
		switch p.ExitKind {
		case ExitSuccess, ExitFailure, ExitStopped:
		default:
			return fmt.Errorf("detect: invalid process exit kind %q", p.ExitKind)
		}
		if p.ExitCode == nil {
			return errors.New("detect: process exit code is required")
		}
		if p.ExitKind == ExitFailure && strings.TrimSpace(p.ErrorMessage) == "" {
			return errors.New("detect: failed process exit requires an error")
		}
		if p.ExitKind == ExitStopped && strings.TrimSpace(p.Reason) == "" {
			return errors.New("detect: stopped process exit requires a reason")
		}
	default:
		return fmt.Errorf("detect: invalid process signal kind %q", kind)
	}
	return nil
}

func validSource(source Source) bool {
	switch source {
	case SourceProcess, SourceHook, SourceNotify, SourceHeuristic, SourceScreen, SourceTimer:
		return true
	default:
		return false
	}
}

func validScreenKind(kind Kind) bool {
	switch kind {
	case KindHumanInputRequired,
		KindHumanInputResolved,
		KindInterrupted,
		KindIdlePrompt:
		return true
	default:
		return false
	}
}

func screenKindMatchesEdge(kind Kind, edge agent.ScreenEdge) bool {
	switch kind {
	case KindHumanInputRequired:
		return edge == agent.ScreenEdgePresent
	case KindHumanInputResolved:
		return edge == agent.ScreenEdgeCleared
	case KindInterrupted, KindIdlePrompt:
		return edge == agent.ScreenEdgePresent || edge == agent.ScreenEdgeCleared
	default:
		return false
	}
}

func validKind(kind Kind) bool {
	switch kind {
	case KindSessionStarted,
		KindObserved,
		KindTurnStarted,
		KindToolActivity,
		KindHumanInputRequired,
		KindHumanInputResolved,
		KindPermissionRequested,
		KindPermissionResolved,
		KindTurnStopped,
		KindTurnFailed,
		KindInterrupted,
		KindIdlePrompt,
		KindSessionEnded,
		KindSubagentStarted,
		KindSubagentStopped,
		KindTaskCompleted,
		KindOutputActivity,
		KindHeuristicBlocked,
		KindProcessStarted,
		KindProcessStartFailed,
		KindProcessExited,
		KindHookActivationExpired,
		KindTimerFired:
		return true
	default:
		return false
	}
}

func validHookKind(kind Kind) bool {
	switch kind {
	case KindSessionStarted,
		KindObserved,
		KindTurnStarted,
		KindToolActivity,
		KindHumanInputRequired,
		KindHumanInputResolved,
		KindPermissionRequested,
		KindPermissionResolved,
		KindTurnStopped,
		KindTurnFailed,
		KindInterrupted,
		KindIdlePrompt,
		KindSessionEnded,
		KindSubagentStarted,
		KindSubagentStopped,
		KindTaskCompleted:
		return true
	default:
		return false
	}
}

func cloneSignal(signal Signal) Signal {
	if signal.Process != nil {
		process := *signal.Process
		if process.ExitCode != nil {
			exitCode := *process.ExitCode
			process.ExitCode = &exitCode
		}
		signal.Process = &process
	}
	if signal.Screen != nil {
		screen := *signal.Screen
		signal.Screen = &screen
	}
	return signal
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

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

// Config contains internal Detector timing and memory limits.
type Config struct {
	HookActivation         time.Duration
	StopConfirmation       time.Duration
	PermissionConfirmation time.Duration
	HeuristicConfirmation  time.Duration
	FallbackIdleAfter      time.Duration
	HeuristicConfidence    float64
	DeliveryRememberCount  int
}

// DefaultConfig returns the fixed Phase 1A Detector settings.
func DefaultConfig() Config {
	return Config{
		HookActivation:         5 * time.Second,
		StopConfirmation:       time.Second,
		PermissionConfirmation: 750 * time.Millisecond,
		HeuristicConfirmation:  750 * time.Millisecond,
		FallbackIdleAfter:      60 * time.Second,
		HeuristicConfidence:    0.85,
		DeliveryRememberCount:  1024,
	}
}

// Detector is an immutable decision engine.
type Detector struct {
	config Config
}

// New validates a Detector configuration.
func New(config Config) (*Detector, error) {
	defaults := DefaultConfig()
	fillConfigDefaults(&config, defaults)
	if config.HookActivation <= 0 ||
		config.StopConfirmation <= 0 ||
		config.PermissionConfirmation <= 0 ||
		config.HeuristicConfirmation <= 0 ||
		config.FallbackIdleAfter <= 0 ||
		math.IsNaN(config.HeuristicConfidence) ||
		math.IsInf(config.HeuristicConfidence, 0) ||
		config.HeuristicConfidence <= 0 ||
		config.HeuristicConfidence > 1 ||
		config.DeliveryRememberCount <= 0 {
		return nil, errors.New("detect: configuration values must be positive and bounded")
	}
	return &Detector{config: config}, nil
}

func fillConfigDefaults(config *Config, defaults Config) {
	if config.HookActivation == 0 {
		config.HookActivation = defaults.HookActivation
	}
	if config.StopConfirmation == 0 {
		config.StopConfirmation = defaults.StopConfirmation
	}
	if config.PermissionConfirmation == 0 {
		config.PermissionConfirmation = defaults.PermissionConfirmation
	}
	if config.HeuristicConfirmation == 0 {
		config.HeuristicConfirmation = defaults.HeuristicConfirmation
	}
	if config.FallbackIdleAfter == 0 {
		config.FallbackIdleAfter = defaults.FallbackIdleAfter
	}
	if config.HeuristicConfidence == 0 {
		config.HeuristicConfidence = defaults.HeuristicConfidence
	}
	if config.DeliveryRememberCount == 0 {
		config.DeliveryRememberCount = defaults.DeliveryRememberCount
	}
}

// CandidatePurpose identifies one bounded confirmation or timeout purpose.
type CandidatePurpose string

const (
	CandidateHookActivation   CandidatePurpose = "hook_activation"
	CandidateHookIdle         CandidatePurpose = "hook_idle"
	CandidateHookPermission   CandidatePurpose = "hook_permission"
	CandidateHeuristicBlocked CandidatePurpose = "heuristic_blocked"
	CandidateFallbackIdle     CandidatePurpose = "fallback_idle"
	CandidateScreen           CandidatePurpose = "screen_confirmation"
)

// TimerRef identifies one candidate generation owned by the Detector.
type TimerRef struct {
	Purpose    CandidatePurpose
	Rule       string
	Generation uint64
}

func (r TimerRef) zero() bool {
	return r == (TimerRef{})
}

func (r TimerRef) validate() error {
	switch r.Purpose {
	case CandidateHookActivation,
		CandidateHookIdle,
		CandidateHookPermission,
		CandidateHeuristicBlocked,
		CandidateFallbackIdle:
		if r.Rule != "" {
			return errors.New("detect: non-screen timer reference cannot contain a rule")
		}
	case CandidateScreen:
		if r.Rule == "" || len(r.Rule) > 64 || !asciiToken(r.Rule) {
			return errors.New("detect: screen timer rule must contain 1 to 64 ASCII bytes")
		}
	default:
		return fmt.Errorf("detect: invalid candidate purpose %q", r.Purpose)
	}
	if r.Generation == 0 {
		return errors.New("detect: timer generation must be positive")
	}
	return nil
}

type candidateKey struct {
	purpose CandidatePurpose
	rule    string
}

func (k candidateKey) ref(generation uint64) TimerRef {
	return TimerRef{
		Purpose:    k.purpose,
		Rule:       k.rule,
		Generation: generation,
	}
}

type candidateState struct {
	generation uint64
	deadline   time.Time
	signal     Signal
}

type stateData struct {
	revision uint64
	policy   agent.HookPolicy
	status   HookStatus
	terminal bool

	candidates           map[candidateKey]candidateState
	candidateGenerations map[candidateKey]uint64

	deliveries  map[string]Outcome
	deliveryIDs []string
}

// State is the committed Detector projection for one attached session.
type State struct {
	mu   sync.RWMutex
	data stateData
}

// Snapshot is an immutable Detector state copy used for one decision.
type Snapshot struct {
	owner *State
	data  stateData
}

// NewState creates a Detector projection for one hook policy.
func NewState(policy agent.HookPolicy) State {
	status := HookAwaiting
	if policy == agent.HooksOff {
		status = HookOff
	}
	return State{data: stateData{
		policy:               policy,
		status:               status,
		candidates:           make(map[candidateKey]candidateState),
		candidateGenerations: make(map[candidateKey]uint64),
		deliveries:           make(map[string]Outcome),
	}}
}

// Snapshot returns a deep copy suitable for pure decision logic.
func (s *State) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Snapshot{owner: s, data: cloneStateData(s.data)}
}

// HookStatus returns the current hook authority state.
func (s Snapshot) HookStatus() HookStatus {
	return s.data.status
}

// Terminal reports whether a terminal process fact has committed.
func (s Snapshot) Terminal() bool {
	return s.data.terminal
}

// Timer returns the active timer, if any.
func (s Snapshot) Timer() TimerPlan {
	plan, ok := earliestTimer(s.data)
	if !ok {
		return TimerPlan{Action: TimerKeep}
	}
	return plan
}

// ApplyCommitted applies one decision after its event batch is durable.
func (s *State) ApplyCommitted(decision Decision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if decision.duplicate || decision.owner != s ||
		decision.expectedRevision != s.data.revision {
		return fmt.Errorf(
			"%w: revision=%d",
			ErrStaleDecision,
			s.data.revision,
		)
	}
	s.data = cloneStateData(decision.next)
	return nil
}

func cloneStateData(data stateData) stateData {
	candidates := make(map[candidateKey]candidateState, len(data.candidates))
	for key, candidate := range data.candidates {
		candidate.signal = cloneSignal(candidate.signal)
		candidates[key] = candidate
	}
	data.candidates = candidates
	data.candidateGenerations = maps.Clone(data.candidateGenerations)
	data.deliveryIDs = append([]string(nil), data.deliveryIDs...)
	deliveries := make(map[string]Outcome, len(data.deliveries))
	maps.Copy(deliveries, data.deliveries)
	data.deliveries = deliveries
	return data
}

type observationKind uint8

const (
	observationSignal observationKind = iota + 1
	observationOutput
	observationTimer
)

// Observation is a sealed normalized input to Detector.Decide.
type Observation struct {
	kind     observationKind
	signal   Signal
	vendor   string
	at       time.Time
	timerRef TimerRef
}

// ObserveSignal wraps a validated hook, heuristic, or process signal.
func ObserveSignal(signal Signal) (Observation, error) {
	if err := signal.validate(); err != nil {
		return Observation{}, err
	}
	if signal.Source == SourceTimer {
		return Observation{}, errors.New("detect: timer signals require ObserveTimer")
	}
	return Observation{kind: observationSignal, signal: cloneSignal(signal)}, nil
}

// ObserveOutput creates a redacted output activity observation.
func ObserveOutput(vendor string, at time.Time) (Observation, error) {
	if at.IsZero() {
		return Observation{}, errors.New("detect: output observation time is required")
	}
	if len(vendor) > maxVendorBytes {
		return Observation{}, errors.New("detect: output vendor exceeds 64 bytes")
	}
	return Observation{kind: observationOutput, vendor: vendor, at: at}, nil
}

// ObserveTimer creates a firing tagged with its candidate timer reference.
func ObserveTimer(ref TimerRef, at time.Time) (Observation, error) {
	if err := ref.validate(); err != nil {
		return Observation{}, fmt.Errorf("detect: timer reference: %w", err)
	}
	if at.IsZero() {
		return Observation{}, errors.New("detect: timer time is required")
	}
	return Observation{
		kind:     observationTimer,
		at:       at,
		timerRef: ref,
	}, nil
}

// Outcome records how a committed signal affected state.
type Outcome string

const (
	OutcomeObserved   Outcome = "observed"
	OutcomeCandidate  Outcome = "candidate"
	OutcomeTransition Outcome = "transitioned"
	OutcomeSuppressed Outcome = "suppressed"
	OutcomeStale      Outcome = "stale"
	OutcomeTerminal   Outcome = "terminal"
)

// TimerAction tells the session actor how to update its live timer.
type TimerAction string

const (
	TimerKeep   TimerAction = "keep"
	TimerCancel TimerAction = "cancel"
	TimerArm    TimerAction = "arm"
)

// TimerPlan is an immutable timer update.
type TimerPlan struct {
	Action   TimerAction
	Deadline time.Time
	Ref      TimerRef
}

// Decision is one immutable signal audit, optional Agent change, and Detector update.
type Decision struct {
	owner            *State
	expectedRevision uint64
	next             stateData
	signal           Signal
	outcome          Outcome
	change           agent.Change
	hasChange        bool
	timer            TimerPlan
	duplicate        bool
}

// Duplicate reports whether the hook delivery was committed previously.
func (d Decision) Duplicate() bool {
	return d.duplicate
}

// Signal returns the signal and outcome that must be committed.
func (d Decision) Signal() (Signal, Outcome, bool) {
	if d.duplicate || d.signal.Version == 0 {
		return Signal{}, "", false
	}
	return cloneSignal(d.signal), d.outcome, true
}

// Change returns the optional Agent projection change.
func (d Decision) Change() (agent.Change, bool) {
	return d.change, d.hasChange
}

// Timer returns the post-commit timer action.
func (d Decision) Timer() TimerPlan {
	return d.timer
}

var (
	// ErrHooksDisabled indicates that policy off rejects hook observations.
	ErrHooksDisabled = errors.New("detect: hooks are disabled")
	// ErrStaleDecision indicates that a decision no longer matches Detector state.
	ErrStaleDecision = errors.New("detect: stale decision")
)

// Decide computes one decision without mutating Detector, Agent, or State.
func (d *Detector) Decide(
	state Snapshot,
	current agent.Snapshot,
	observation Observation,
) (Decision, error) {
	if state.owner == nil {
		return Decision{}, errors.New("detect: state snapshot is required")
	}
	if !agent.ValidHookPolicy(state.data.policy) {
		return Decision{}, fmt.Errorf("detect: invalid hook policy %q", state.data.policy)
	}
	if !agent.Valid(current.State) || !agent.ValidRunMode(current.RunMode) {
		return Decision{}, errors.New("detect: invalid Agent snapshot")
	}

	var (
		decision    Decision
		decisionErr error
	)
	switch observation.kind {
	case observationSignal:
		if validationErr := observation.signal.validate(); validationErr != nil {
			return Decision{}, validationErr
		}
		switch observation.signal.Source {
		case SourceHook:
			decision, decisionErr = d.decideHook(state, current, observation.signal)
		case SourceNotify:
			decision, decisionErr = d.decideNotify(state, current, observation.signal)
		case SourceHeuristic:
			decision, decisionErr = d.decideHeuristic(state, current, observation.signal)
		case SourceProcess:
			decision, decisionErr = d.decideProcess(state, current, observation.signal)
		case SourceScreen:
			decision, decisionErr = d.decideScreen(state, current, observation.signal)
		default:
			return Decision{}, fmt.Errorf(
				"detect: unsupported observed signal source %q",
				observation.signal.Source,
			)
		}
	case observationOutput:
		if observation.at.IsZero() {
			return Decision{}, errors.New("detect: output observation time is required")
		}
		signal, signalErr := NewHeuristicSignal(Signal{
			Kind:        KindObserved,
			Vendor:      observation.vendor,
			VendorEvent: string(KindOutputActivity),
			Scope:       ScopeRoot,
			Evidence:    string(KindOutputActivity),
			Confidence:  1,
			ReceivedAt:  observation.at,
		})
		if signalErr != nil {
			return Decision{}, signalErr
		}
		decision, decisionErr = d.decideHeuristic(state, current, signal)
	case observationTimer:
		decision, decisionErr = d.decideTimer(state, current, observation)
	default:
		return Decision{}, errors.New("detect: invalid observation")
	}
	if decisionErr != nil {
		return Decision{}, decisionErr
	}
	if !decision.duplicate {
		decision.timer = timerPlanAfterDecision(decision.next)
	}
	return decision, nil
}

func (d *Detector) decideNotify(
	state Snapshot,
	current agent.Snapshot,
	signal Signal,
) (Decision, error) {
	if state.data.policy == agent.HooksOff {
		return Decision{}, ErrHooksDisabled
	}
	if outcome, duplicate := state.data.deliveries[signal.DeliveryID]; duplicate {
		return Decision{
			owner:            state.owner,
			expectedRevision: state.data.revision,
			signal:           cloneSignal(signal),
			outcome:          outcome,
			duplicate:        true,
			timer:            TimerPlan{Action: TimerKeep},
		}, nil
	}

	decision := newDecision(state, signal, OutcomeObserved)
	switch {
	case state.data.terminal:
		decision.outcome = OutcomeTerminal
	case state.data.policy == agent.HooksRequired,
		state.data.status == HookAwaiting,
		state.data.status == HookActive:
		decision.outcome = OutcomeSuppressed
	case state.data.status == HookFallback:
		if current.State == agent.StateWorking || current.State == agent.StateBlocked {
			armCandidate(
				&decision,
				candidateKey{purpose: CandidateHookIdle},
				signal.ReceivedAt.Add(d.config.StopConfirmation),
				signal,
			)
			decision.outcome = OutcomeCandidate
		}
	default:
		decision.outcome = OutcomeSuppressed
	}
	rememberDelivery(
		&decision.next,
		signal.DeliveryID,
		decision.outcome,
		d.config.DeliveryRememberCount,
	)
	return decision, nil
}

func (d *Detector) decideHook(
	state Snapshot,
	current agent.Snapshot,
	signal Signal,
) (Decision, error) {
	if state.data.policy == agent.HooksOff {
		return Decision{}, ErrHooksDisabled
	}
	if outcome, duplicate := state.data.deliveries[signal.DeliveryID]; duplicate {
		return Decision{
			owner:            state.owner,
			expectedRevision: state.data.revision,
			signal:           cloneSignal(signal),
			outcome:          outcome,
			duplicate:        true,
			timer:            TimerPlan{Action: TimerKeep},
		}, nil
	}

	decision := newDecision(state, signal, OutcomeObserved)
	if state.data.terminal {
		decision.outcome = OutcomeTerminal
		rememberDelivery(
			&decision.next,
			signal.DeliveryID,
			decision.outcome,
			d.config.DeliveryRememberCount,
		)
		return decision, nil
	}
	wasActive := decision.next.status == HookActive
	decision.next.status = HookActive
	if !wasActive {
		cancelAllCandidates(&decision)
	} else {
		cancelCandidatesByPurpose(&decision, CandidateScreen)
	}

	switch signal.Kind {
	case KindTurnStarted, KindHumanInputResolved:
		cancelAllCandidates(&decision)
		transition(&decision, current, agent.StateWorking, "hook "+signal.VendorEvent, "")
	case KindToolActivity:
		if signal.Scope == ScopeRoot {
			cancelAllCandidates(&decision)
			transition(&decision, current, agent.StateWorking, "hook "+signal.VendorEvent, "")
		}
	case KindSubagentStarted:
		cancelAllCandidates(&decision)
		transition(&decision, current, agent.StateWorking, "hook "+signal.VendorEvent, "")
	case KindHumanInputRequired:
		cancelAllCandidates(&decision)
		transition(&decision, current, agent.StateBlocked, "hook "+signal.VendorEvent, "")
	case KindPermissionRequested:
		cancelCandidate(&decision, candidateKey{purpose: CandidateHookIdle})
		armCandidate(
			&decision,
			candidateKey{purpose: CandidateHookPermission},
			signal.ReceivedAt.Add(d.config.PermissionConfirmation),
			signal,
		)
		decision.outcome = OutcomeCandidate
	case KindPermissionResolved:
		cancelCandidate(&decision, candidateKey{purpose: CandidateHookPermission})
	case KindTurnStopped, KindTurnFailed, KindInterrupted, KindIdlePrompt:
		if signal.Scope == ScopeRoot {
			cancelCandidate(&decision, candidateKey{purpose: CandidateHookPermission})
			armCandidate(
				&decision,
				candidateKey{purpose: CandidateHookIdle},
				signal.ReceivedAt.Add(d.config.StopConfirmation),
				signal,
			)
			decision.outcome = OutcomeCandidate
		}
	}
	rememberDelivery(&decision.next, signal.DeliveryID, decision.outcome, d.config.DeliveryRememberCount)
	return decision, nil
}

func (d *Detector) decideHeuristic(
	state Snapshot,
	current agent.Snapshot,
	signal Signal,
) (Decision, error) {
	decision := newDecision(state, signal, OutcomeObserved)
	if state.data.terminal {
		decision.outcome = OutcomeTerminal
		return decision, nil
	}
	if !fallbackEnabled(state.data) {
		decision.outcome = OutcomeSuppressed
		return decision, nil
	}
	idleKey := candidateKey{purpose: CandidateHookIdle}
	if candidate, ok := decision.next.candidates[idleKey]; ok &&
		candidate.signal.Source == SourceNotify {
		cancelCandidate(&decision, idleKey)
	}

	if signal.Kind == KindHeuristicBlocked {
		if current.State == agent.StateWorking || current.State == agent.StateIdle {
			if signal.Confidence >= d.config.HeuristicConfidence {
				key := candidateKey{purpose: CandidateHeuristicBlocked}
				if _, exists := decision.next.candidates[key]; !exists {
					armCandidate(
						&decision,
						key,
						signal.ReceivedAt.Add(d.config.HeuristicConfirmation),
						signal,
					)
				}
				decision.outcome = OutcomeCandidate
				return decision, nil
			}
			decision.outcome = OutcomeSuppressed
			armFallbackIdle(&decision, current, signal.ReceivedAt, d.config.FallbackIdleAfter)
			return decision, nil
		}
		return decision, nil
	}

	cancelCandidate(&decision, candidateKey{purpose: CandidateHeuristicBlocked})
	armFallbackIdle(&decision, current, signal.ReceivedAt, d.config.FallbackIdleAfter)
	return decision, nil
}

func (d *Detector) decideProcess(
	state Snapshot,
	current agent.Snapshot,
	signal Signal,
) (Decision, error) {
	decision := newDecision(state, signal, OutcomeObserved)
	process := signal.Process
	cancelAllCandidates(&decision)
	switch signal.Kind {
	case KindProcessStarted:
		transition(&decision, current, agent.StateWorking, "process started", "")
		switch state.data.policy {
		case agent.HooksOff:
			armFallbackIdle(&decision, agent.Snapshot{
				State: agent.StateWorking,
			}, signal.ReceivedAt, d.config.FallbackIdleAfter)
		case agent.HooksAuto:
			if process.HookAvailable {
				armCandidate(
					&decision,
					candidateKey{purpose: CandidateHookActivation},
					signal.ReceivedAt.Add(d.config.HookActivation),
					Signal{},
				)
			} else {
				decision.next.status = HookFallback
				armFallbackIdle(&decision, agent.Snapshot{
					State: agent.StateWorking,
				}, signal.ReceivedAt, d.config.FallbackIdleAfter)
			}
		case agent.HooksRequired:
			armCandidate(
				&decision,
				candidateKey{purpose: CandidateHookActivation},
				signal.ReceivedAt.Add(d.config.HookActivation),
				Signal{},
			)
		}
	case KindProcessStartFailed:
		decision.outcome = OutcomeTerminal
		decision.next.terminal = true
		decision.next.status = HookDetached
		transition(
			&decision,
			current,
			agent.StateStopped,
			"startup failed",
			process.ErrorMessage,
		)
	case KindProcessExited:
		decision.outcome = OutcomeTerminal
		decision.next.terminal = true
		decision.next.status = HookDetached

		target := agent.StateStopped
		message := process.ErrorMessage
		reason := fmt.Sprintf("process exited code=%d", *process.ExitCode)
		if process.ExitKind == ExitStopped {
			reason = process.Reason
			message = ""
		} else if process.ExitKind == ExitSuccess &&
			current.RunMode == agent.RunModeOneshot &&
			(state.data.policy != agent.HooksRequired || state.data.status == HookActive) {
			target = agent.StateDone
		} else if process.ExitKind == ExitSuccess &&
			state.data.policy == agent.HooksRequired &&
			state.data.status != HookActive {
			message = "required hook not observed"
		}
		transition(&decision, current, target, reason, message)
	}
	return decision, nil
}

func (d *Detector) decideScreen(
	state Snapshot,
	current agent.Snapshot,
	signal Signal,
) (Decision, error) {
	decision := newDecision(state, signal, OutcomeSuppressed)
	key := candidateKey{
		purpose: CandidateScreen,
		rule:    signal.Screen.Rule,
	}
	cancelCandidate(&decision, key)
	if state.data.terminal {
		decision.outcome = OutcomeTerminal
		return decision, nil
	}

	confirmation, known := ScreenRuleConfirmation(
		signal.Screen.Rule,
		signal.Kind,
		signal.Screen.Edge,
	)
	if !known {
		return decision, nil
	}

	allowed := false
	switch state.data.status {
	case HookActive:
		allowed =
			isApprovalClearance(signal) && current.State == agent.StateBlocked ||
				isClaudeInterrupt(signal) && current.State == agent.StateWorking
	case HookOff, HookFallback:
		switch {
		case isApprovalPresence(signal):
			allowed = current.State == agent.StateWorking || current.State == agent.StateIdle
		case isApprovalClearance(signal):
			allowed = current.State == agent.StateBlocked
		case isIdleOrInterruptPresence(signal):
			allowed = current.State == agent.StateWorking || current.State == agent.StateBlocked
		}
	case HookAwaiting, HookRequiredFailed, HookDetached:
	}
	if !allowed {
		return decision, nil
	}

	armCandidate(&decision, key, signal.ReceivedAt.Add(confirmation), signal)
	decision.outcome = OutcomeCandidate
	return decision, nil
}

func (d *Detector) decideTimer(
	state Snapshot,
	current agent.Snapshot,
	observation Observation,
) (Decision, error) {
	ref := observation.timerRef
	eventName := timerEvent(ref.Purpose)
	kind := KindTimerFired
	if ref.Purpose == CandidateHookActivation {
		kind = KindHookActivationExpired
	}
	signal := newTimerSignal(kind, eventName, ref, observation.at)
	if err := signal.validate(); err != nil {
		return Decision{}, err
	}
	decision := newDecision(state, signal, OutcomeObserved)
	key := candidateKey{purpose: ref.Purpose, rule: ref.Rule}
	candidate, exists := state.data.candidates[key]
	if !exists || candidate.generation != ref.Generation {
		decision.outcome = OutcomeStale
		return decision, nil
	}
	cancelCandidate(&decision, key)

	switch ref.Purpose {
	case CandidateHookActivation:
		if state.data.policy == agent.HooksRequired {
			decision.next.terminal = true
			decision.next.status = HookRequiredFailed
			cancelAllCandidates(&decision)
			transition(
				&decision,
				current,
				agent.StateStopped,
				"required hook activation timed out",
				"required hook not observed",
			)
		} else {
			decision.next.status = HookFallback
			if current.State == agent.StateWorking {
				armCandidate(
					&decision,
					candidateKey{purpose: CandidateFallbackIdle},
					observation.at.Add(d.config.FallbackIdleAfter),
					Signal{},
				)
			}
		}
	case CandidateHookPermission:
		transition(
			&decision,
			current,
			agent.StateBlocked,
			"hook permission request confirmed",
			"",
		)
	case CandidateHookIdle:
		if candidate.signal.Source == SourceNotify {
			transitionWithEvidence(
				&decision,
				current,
				agent.StateIdle,
				"notify idle confirmed",
				"",
				evidence(candidate.signal),
			)
		} else {
			transition(
				&decision,
				current,
				agent.StateIdle,
				"hook idle confirmed",
				"",
			)
		}
	case CandidateHeuristicBlocked:
		transition(
			&decision,
			current,
			agent.StateBlocked,
			"heuristic blocked confirmed",
			"",
		)
	case CandidateFallbackIdle:
		transition(
			&decision,
			current,
			agent.StateIdle,
			"fallback silence",
			"",
		)
	case CandidateScreen:
		target, ok := screenCandidateTarget(candidate.signal, current.State)
		if !ok {
			return decision, nil
		}
		transitionWithEvidence(
			&decision,
			current,
			target,
			"screen "+candidate.signal.Screen.Rule+" confirmed",
			"",
			evidence(candidate.signal),
		)
	default:
		decision.outcome = OutcomeStale
	}
	return decision, nil
}

func newDecision(state Snapshot, signal Signal, outcome Outcome) Decision {
	next := cloneStateData(state.data)
	next.revision++
	return Decision{
		owner:            state.owner,
		expectedRevision: state.data.revision,
		next:             next,
		signal:           cloneSignal(signal),
		outcome:          outcome,
		timer:            TimerPlan{Action: TimerKeep},
	}
}

func transition(
	decision *Decision,
	current agent.Snapshot,
	target agent.State,
	reason string,
	errorMessage string,
) {
	transitionWithEvidence(
		decision,
		current,
		target,
		reason,
		errorMessage,
		evidence(decision.signal),
	)
}

func transitionWithEvidence(
	decision *Decision,
	current agent.Snapshot,
	target agent.State,
	reason string,
	errorMessage string,
	transitionEvidence agent.Evidence,
) {
	if current.State == target {
		if errorMessage != "" {
			decision.change = agent.RecordError(errorMessage, transitionEvidence)
			decision.hasChange = true
		}
		return
	}
	if !agent.CanTransition(current.State, target) {
		return
	}
	if errorMessage != "" {
		decision.change = agent.FailTo(
			target,
			reason,
			errorMessage,
			transitionEvidence,
		)
	} else {
		decision.change = agent.MoveTo(target, reason, transitionEvidence)
	}
	decision.hasChange = true
	cancelIncompatibleCandidates(decision, target)
	if decision.outcome != OutcomeTerminal {
		decision.outcome = OutcomeTransition
	}
}

func evidence(signal Signal) agent.Evidence {
	result := agent.Evidence{
		Source:     agent.EvidenceSource(signal.Source),
		Event:      signal.VendorEvent,
		Confidence: signal.Confidence,
		DeliveryID: signal.DeliveryID,
	}
	if signal.Screen != nil {
		screen := *signal.Screen
		result.Event = screen.Rule
		result.Screen = &screen
	}
	return result
}

func fallbackEnabled(data stateData) bool {
	return data.policy == agent.HooksOff || data.status == HookFallback
}

func armFallbackIdle(
	decision *Decision,
	current agent.Snapshot,
	at time.Time,
	delay time.Duration,
) {
	if current.State != agent.StateWorking {
		return
	}
	armCandidate(
		decision,
		candidateKey{purpose: CandidateFallbackIdle},
		at.Add(delay),
		Signal{},
	)
}

func armCandidate(
	decision *Decision,
	key candidateKey,
	deadline time.Time,
	candidate Signal,
) {
	generation := decision.next.candidateGenerations[key] + 1
	if generation == 0 {
		generation = 1
	}
	decision.next.candidateGenerations[key] = generation
	decision.next.candidates[key] = candidateState{
		generation: generation,
		deadline:   deadline,
		signal:     cloneSignal(candidate),
	}
}

func cancelCandidate(decision *Decision, key candidateKey) {
	if _, exists := decision.next.candidates[key]; !exists {
		return
	}
	generation := decision.next.candidateGenerations[key] + 1
	if generation == 0 {
		generation = 1
	}
	decision.next.candidateGenerations[key] = generation
	delete(decision.next.candidates, key)
}

func cancelCandidatesByPurpose(decision *Decision, purpose CandidatePurpose) {
	for key := range decision.next.candidates {
		if key.purpose == purpose {
			cancelCandidate(decision, key)
		}
	}
}

func cancelAllCandidates(decision *Decision) {
	for key := range decision.next.candidates {
		cancelCandidate(decision, key)
	}
}

func cancelIncompatibleCandidates(decision *Decision, state agent.State) {
	for key, candidate := range decision.next.candidates {
		compatible := false
		switch key.purpose {
		case CandidateHookActivation, CandidateFallbackIdle:
			compatible = state == agent.StateWorking
		case CandidateHookIdle:
			compatible = state == agent.StateWorking || state == agent.StateBlocked
		case CandidateHookPermission, CandidateHeuristicBlocked:
			compatible = state == agent.StateWorking || state == agent.StateIdle
		case CandidateScreen:
			_, compatible = screenCandidateTarget(candidate.signal, state)
		}
		if !compatible {
			cancelCandidate(decision, key)
		}
	}
}

func timerPlanAfterDecision(data stateData) TimerPlan {
	if plan, ok := earliestTimer(data); ok {
		return plan
	}
	return TimerPlan{Action: TimerCancel}
}

func earliestTimer(data stateData) (TimerPlan, bool) {
	var (
		earliestKey       candidateKey
		earliestCandidate candidateState
		found             bool
	)
	for key, candidate := range data.candidates {
		if !found ||
			candidate.deadline.Before(earliestCandidate.deadline) ||
			candidate.deadline.Equal(earliestCandidate.deadline) &&
				(string(key.purpose) < string(earliestKey.purpose) ||
					key.purpose == earliestKey.purpose && key.rule < earliestKey.rule) {
			earliestKey = key
			earliestCandidate = candidate
			found = true
		}
	}
	if !found {
		return TimerPlan{}, false
	}
	return TimerPlan{
		Action:   TimerArm,
		Deadline: earliestCandidate.deadline,
		Ref:      earliestKey.ref(earliestCandidate.generation),
	}, true
}

func timerEvent(purpose CandidatePurpose) string {
	switch purpose {
	case CandidateHookActivation:
		return string(KindHookActivationExpired)
	case CandidateHookIdle:
		return "idle_confirmation"
	case CandidateHookPermission:
		return "permission_confirmation"
	case CandidateHeuristicBlocked:
		return "heuristic_blocked_confirmation"
	case CandidateFallbackIdle:
		return "fallback_idle_timeout"
	case CandidateScreen:
		return "screen_confirmation"
	default:
		return "stale_timer"
	}
}

// ScreenRuleConfirmation returns the fixed Detector duration for a screen edge.
func ScreenRuleConfirmation(
	rule string,
	kind Kind,
	edge agent.ScreenEdge,
) (time.Duration, bool) {
	switch {
	case (rule == "claude.approval_prompt" || rule == "codex.approval_prompt") &&
		kind == KindHumanInputRequired &&
		edge == agent.ScreenEdgePresent:
		return 750 * time.Millisecond, true
	case (rule == "claude.approval_prompt" || rule == "codex.approval_prompt") &&
		kind == KindHumanInputResolved &&
		edge == agent.ScreenEdgeCleared:
		return 500 * time.Millisecond, true
	case (rule == "claude.idle_prompt" || rule == "codex.idle_prompt") &&
		kind == KindIdlePrompt &&
		edge == agent.ScreenEdgePresent:
		return time.Second, true
	case rule == "claude.interrupted" &&
		kind == KindInterrupted &&
		edge == agent.ScreenEdgePresent:
		return time.Second, true
	default:
		return 0, false
	}
}

func knownScreenRule(rule string) bool {
	switch rule {
	case "claude.approval_prompt",
		"claude.idle_prompt",
		"claude.interrupted",
		"codex.approval_prompt",
		"codex.idle_prompt":
		return true
	default:
		return false
	}
}

func knownScreenRuleMatchesKind(rule string, kind Kind) bool {
	switch rule {
	case "claude.approval_prompt", "codex.approval_prompt":
		return kind == KindHumanInputRequired || kind == KindHumanInputResolved
	case "claude.idle_prompt", "codex.idle_prompt":
		return kind == KindIdlePrompt
	case "claude.interrupted":
		return kind == KindInterrupted
	default:
		return false
	}
}

func isApprovalPresence(signal Signal) bool {
	return signal.Kind == KindHumanInputRequired &&
		(signal.Screen.Rule == "claude.approval_prompt" ||
			signal.Screen.Rule == "codex.approval_prompt")
}

func isApprovalClearance(signal Signal) bool {
	return signal.Kind == KindHumanInputResolved &&
		(signal.Screen.Rule == "claude.approval_prompt" ||
			signal.Screen.Rule == "codex.approval_prompt")
}

func isClaudeInterrupt(signal Signal) bool {
	return signal.Kind == KindInterrupted &&
		signal.Screen.Rule == "claude.interrupted"
}

func isIdleOrInterruptPresence(signal Signal) bool {
	return signal.Kind == KindIdlePrompt ||
		signal.Kind == KindInterrupted
}

func screenCandidateTarget(signal Signal, current agent.State) (agent.State, bool) {
	switch signal.Kind {
	case KindHumanInputRequired:
		if current == agent.StateWorking || current == agent.StateIdle {
			return agent.StateBlocked, true
		}
	case KindHumanInputResolved:
		if current == agent.StateBlocked {
			return agent.StateWorking, true
		}
	case KindIdlePrompt, KindInterrupted:
		if current == agent.StateWorking || current == agent.StateBlocked {
			return agent.StateIdle, true
		}
	}
	return "", false
}

func rememberDelivery(data *stateData, id string, outcome Outcome, limit int) {
	data.deliveries[id] = outcome
	data.deliveryIDs = append(data.deliveryIDs, id)
	if len(data.deliveryIDs) <= limit {
		return
	}
	oldest := data.deliveryIDs[0]
	data.deliveryIDs = data.deliveryIDs[1:]
	delete(data.deliveries, oldest)
}
