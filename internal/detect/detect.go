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
	SourceHeuristic Source = "heuristic"
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
	TimerGeneration uint64
	Process         *ProcessFact
}

// NewHookSignal validates and copies a normalized vendor hook signal.
func NewHookSignal(signal Signal) (Signal, error) {
	signal.Version = signalVersion
	signal.Source = SourceHook
	signal.Process = nil
	signal.TimerGeneration = 0
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
	signal.TimerGeneration = 0
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
	signal.TimerGeneration = 0
	if err := signal.validate(); err != nil {
		return Signal{}, err
	}
	return cloneSignal(signal), nil
}

func newTimerSignal(kind Kind, event string, generation uint64, at time.Time) Signal {
	return Signal{
		Version:         signalVersion,
		Source:          SourceTimer,
		Kind:            kind,
		VendorEvent:     event,
		Scope:           ScopeRoot,
		Evidence:        event,
		Confidence:      1,
		ReceivedAt:      at,
		TimerGeneration: generation,
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
		if s.Process != nil || s.TimerGeneration != 0 {
			return errors.New("detect: hook signal contains non-hook metadata")
		}
	case SourceHeuristic:
		if s.DeliveryID != "" || s.Process != nil || s.TimerGeneration != 0 {
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
	case SourceTimer:
		if s.DeliveryID != "" || s.Process != nil || s.TimerGeneration == 0 {
			return errors.New("detect: timer signal requires a generation only")
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
	case SourceProcess, SourceHook, SourceHeuristic, SourceTimer:
		return true
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
	HookActivation          time.Duration
	StopConfirmation        time.Duration
	PermissionConfirmation  time.Duration
	HeuristicConfirmation   time.Duration
	HeuristicRecoveryWindow time.Duration
	HeuristicRecoveryLines  int
	FallbackIdleAfter       time.Duration
	HeuristicConfidence     float64
	DeliveryRememberCount   int
}

// DefaultConfig returns the fixed Phase 1A Detector settings.
func DefaultConfig() Config {
	return Config{
		HookActivation:          5 * time.Second,
		StopConfirmation:        time.Second,
		PermissionConfirmation:  750 * time.Millisecond,
		HeuristicConfirmation:   750 * time.Millisecond,
		HeuristicRecoveryWindow: time.Second,
		HeuristicRecoveryLines:  2,
		FallbackIdleAfter:       60 * time.Second,
		HeuristicConfidence:     0.85,
		DeliveryRememberCount:   1024,
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
		config.HeuristicRecoveryWindow <= 0 ||
		config.HeuristicRecoveryLines <= 0 ||
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
	if config.HeuristicRecoveryWindow == 0 {
		config.HeuristicRecoveryWindow = defaults.HeuristicRecoveryWindow
	}
	if config.HeuristicRecoveryLines == 0 {
		config.HeuristicRecoveryLines = defaults.HeuristicRecoveryLines
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

type timerKind uint8

const (
	timerNone timerKind = iota
	timerHookActivation
	timerHookIdle
	timerHookPermission
	timerHeuristicBlocked
	timerFallbackIdle
)

type stateData struct {
	revision uint64
	policy   agent.HookPolicy
	status   HookStatus
	terminal bool

	timerKind       timerKind
	timerGeneration uint64
	timerDeadline   time.Time
	timerCandidate  Signal

	recentOutput []time.Time
	deliveries   map[string]Outcome
	deliveryIDs  []string
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
		policy:     policy,
		status:     status,
		deliveries: make(map[string]Outcome),
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
	if s.data.timerKind == timerNone {
		return TimerPlan{Action: TimerKeep, Generation: s.data.timerGeneration}
	}
	return TimerPlan{
		Action:     TimerArm,
		Deadline:   s.data.timerDeadline,
		Generation: s.data.timerGeneration,
	}
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
	data.timerCandidate = cloneSignal(data.timerCandidate)
	data.recentOutput = append([]time.Time(nil), data.recentOutput...)
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
	kind       observationKind
	signal     Signal
	vendor     string
	at         time.Time
	generation uint64
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

// ObserveTimer creates a timer firing tagged with its arm generation.
func ObserveTimer(generation uint64, at time.Time) (Observation, error) {
	if generation == 0 || at.IsZero() {
		return Observation{}, errors.New("detect: timer generation and time are required")
	}
	return Observation{
		kind:       observationTimer,
		at:         at,
		generation: generation,
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
	Action     TimerAction
	Deadline   time.Time
	Generation uint64
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

	switch observation.kind {
	case observationSignal:
		if err := observation.signal.validate(); err != nil {
			return Decision{}, err
		}
		switch observation.signal.Source {
		case SourceHook:
			return d.decideHook(state, current, observation.signal)
		case SourceHeuristic:
			return d.decideHeuristic(state, current, observation.signal)
		case SourceProcess:
			return d.decideProcess(state, current, observation.signal)
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
		signal, err := NewHeuristicSignal(Signal{
			Kind:        KindObserved,
			Vendor:      observation.vendor,
			VendorEvent: string(KindOutputActivity),
			Scope:       ScopeRoot,
			Evidence:    string(KindOutputActivity),
			Confidence:  1,
			ReceivedAt:  observation.at,
		})
		if err != nil {
			return Decision{}, err
		}
		return d.decideHeuristic(state, current, signal)
	case observationTimer:
		return d.decideTimer(state, current, observation)
	default:
		return Decision{}, errors.New("detect: invalid observation")
	}
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

	switch signal.Kind {
	case KindTurnStarted, KindHumanInputResolved:
		cancelTimer(&decision)
		transition(&decision, current, agent.StateWorking, "hook "+signal.VendorEvent, "")
	case KindToolActivity:
		cancelTimer(&decision)
		if signal.Scope == ScopeRoot {
			transition(&decision, current, agent.StateWorking, "hook "+signal.VendorEvent, "")
		}
	case KindSubagentStarted:
		cancelTimer(&decision)
		transition(&decision, current, agent.StateWorking, "hook "+signal.VendorEvent, "")
	case KindHumanInputRequired:
		cancelTimer(&decision)
		transition(&decision, current, agent.StateBlocked, "hook "+signal.VendorEvent, "")
	case KindPermissionRequested:
		armTimer(&decision, timerHookPermission, signal.ReceivedAt.Add(d.config.PermissionConfirmation), signal)
		decision.outcome = OutcomeCandidate
	case KindPermissionResolved:
		if decision.next.timerKind == timerHookPermission {
			cancelTimer(&decision)
		}
	case KindTurnStopped, KindTurnFailed, KindInterrupted, KindIdlePrompt:
		if signal.Scope == ScopeRoot {
			armTimer(&decision, timerHookIdle, signal.ReceivedAt.Add(d.config.StopConfirmation), signal)
			decision.outcome = OutcomeCandidate
		}
	}
	if !wasActive && decision.timer.Action == TimerKeep {
		cancelTimer(&decision)
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

	if signal.Kind == KindHeuristicBlocked {
		if current.State == agent.StateWorking || current.State == agent.StateIdle {
			if signal.Confidence >= d.config.HeuristicConfidence {
				if decision.next.timerKind != timerHeuristicBlocked {
					armTimer(
						&decision,
						timerHeuristicBlocked,
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

	if decision.next.timerKind == timerHeuristicBlocked {
		cancelTimer(&decision)
	}
	if current.State == agent.StateBlocked {
		pruneOutput(&decision.next, signal.ReceivedAt, d.config.HeuristicRecoveryWindow)
		decision.next.recentOutput = append(decision.next.recentOutput, signal.ReceivedAt)
		if len(decision.next.recentOutput) >= d.config.HeuristicRecoveryLines {
			decision.next.recentOutput = nil
			transition(
				&decision,
				current,
				agent.StateWorking,
				"sustained fallback output",
				"",
			)
			armFallbackIdle(&decision, agent.Snapshot{
				State: agent.StateWorking,
			}, signal.ReceivedAt, d.config.FallbackIdleAfter)
		}
		return decision, nil
	}
	decision.next.recentOutput = nil
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
				armTimer(
					&decision,
					timerHookActivation,
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
			armTimer(
				&decision,
				timerHookActivation,
				signal.ReceivedAt.Add(d.config.HookActivation),
				Signal{},
			)
		}
	case KindProcessStartFailed:
		decision.outcome = OutcomeTerminal
		decision.next.terminal = true
		decision.next.status = HookDetached
		cancelTimer(&decision)
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
		cancelTimer(&decision)

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

func (d *Detector) decideTimer(
	state Snapshot,
	current agent.Snapshot,
	observation Observation,
) (Decision, error) {
	timer := state.data.timerKind
	eventName := timerEvent(timer)
	kind := KindTimerFired
	if timer == timerHookActivation {
		kind = KindHookActivationExpired
	}
	signal := newTimerSignal(kind, eventName, observation.generation, observation.at)
	if err := signal.validate(); err != nil {
		return Decision{}, err
	}
	decision := newDecision(state, signal, OutcomeObserved)
	if observation.generation != state.data.timerGeneration || timer == timerNone {
		decision.outcome = OutcomeStale
		return decision, nil
	}

	switch timer {
	case timerHookActivation:
		if state.data.policy == agent.HooksRequired {
			decision.next.terminal = true
			decision.next.status = HookRequiredFailed
			transition(
				&decision,
				current,
				agent.StateStopped,
				"required hook activation timed out",
				"required hook not observed",
			)
			cancelTimer(&decision)
		} else {
			decision.next.status = HookFallback
			if current.State == agent.StateWorking {
				armTimer(
					&decision,
					timerFallbackIdle,
					observation.at.Add(d.config.FallbackIdleAfter),
					Signal{},
				)
			} else {
				cancelTimer(&decision)
			}
		}
	case timerHookPermission:
		cancelTimer(&decision)
		transition(
			&decision,
			current,
			agent.StateBlocked,
			"hook permission request confirmed",
			"",
		)
	case timerHookIdle:
		cancelTimer(&decision)
		transition(
			&decision,
			current,
			agent.StateIdle,
			"hook idle confirmed",
			"",
		)
	case timerHeuristicBlocked:
		cancelTimer(&decision)
		transition(
			&decision,
			current,
			agent.StateBlocked,
			"heuristic blocked confirmed",
			"",
		)
	case timerFallbackIdle:
		cancelTimer(&decision)
		transition(
			&decision,
			current,
			agent.StateIdle,
			"fallback silence",
			"",
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
		timer: TimerPlan{
			Action:     TimerKeep,
			Generation: state.data.timerGeneration,
		},
	}
}

func transition(
	decision *Decision,
	current agent.Snapshot,
	target agent.State,
	reason string,
	errorMessage string,
) {
	if current.State == target {
		if errorMessage != "" {
			decision.change = agent.RecordError(errorMessage, evidence(decision.signal))
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
			evidence(decision.signal),
		)
	} else {
		decision.change = agent.MoveTo(target, reason, evidence(decision.signal))
	}
	decision.hasChange = true
	if decision.outcome != OutcomeTerminal {
		decision.outcome = OutcomeTransition
	}
}

func evidence(signal Signal) agent.Evidence {
	return agent.Evidence{
		Source:     agent.EvidenceSource(signal.Source),
		Event:      signal.VendorEvent,
		Confidence: signal.Confidence,
		DeliveryID: signal.DeliveryID,
	}
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
	armTimer(decision, timerFallbackIdle, at.Add(delay), Signal{})
}

func armTimer(
	decision *Decision,
	kind timerKind,
	deadline time.Time,
	candidate Signal,
) {
	decision.next.timerGeneration++
	decision.next.timerKind = kind
	decision.next.timerDeadline = deadline
	decision.next.timerCandidate = cloneSignal(candidate)
	decision.timer = TimerPlan{
		Action:     TimerArm,
		Deadline:   deadline,
		Generation: decision.next.timerGeneration,
	}
}

func cancelTimer(decision *Decision) {
	if decision.next.timerKind == timerNone {
		return
	}
	decision.next.timerGeneration++
	decision.next.timerKind = timerNone
	decision.next.timerDeadline = time.Time{}
	decision.next.timerCandidate = Signal{}
	decision.timer = TimerPlan{
		Action:     TimerCancel,
		Generation: decision.next.timerGeneration,
	}
}

func timerEvent(kind timerKind) string {
	switch kind {
	case timerHookActivation:
		return string(KindHookActivationExpired)
	case timerHookIdle:
		return "idle_confirmation"
	case timerHookPermission:
		return "permission_confirmation"
	case timerHeuristicBlocked:
		return "heuristic_blocked_confirmation"
	case timerFallbackIdle:
		return "fallback_idle_timeout"
	default:
		return "stale_timer"
	}
}

func pruneOutput(data *stateData, at time.Time, window time.Duration) {
	cutoff := at.Add(-window)
	kept := data.recentOutput[:0]
	for _, observedAt := range data.recentOutput {
		if !observedAt.Before(cutoff) && !observedAt.After(at) {
			kept = append(kept, observedAt)
		}
	}
	data.recentOutput = kept
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
