package detect

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Duang777/drove/internal/agent"
)

var testTime = time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)

func TestDefaultConfigMatchesPhaseOneTiming(t *testing.T) {
	config := DefaultConfig()
	if config.HookActivation != 5*time.Second ||
		config.StopConfirmation != time.Second ||
		config.PermissionConfirmation != 750*time.Millisecond ||
		config.HeuristicConfirmation != 750*time.Millisecond ||
		config.HeuristicRecoveryWindow != time.Second ||
		config.HeuristicRecoveryLines != 2 ||
		config.FallbackIdleAfter != 60*time.Second ||
		config.HeuristicConfidence != 0.85 ||
		config.DeliveryRememberCount != 1024 {
		t.Fatalf("default config = %+v", config)
	}
}

func TestSignalConstructorsRejectInvalidSourceMetadata(t *testing.T) {
	_, err := NewHookSignal(Signal{
		Kind:        KindSessionStarted,
		Vendor:      "claude",
		VendorEvent: "SessionStart",
		Scope:       ScopeRoot,
		Confidence:  1,
		ReceivedAt:  testTime,
		DeliveryID:  "not-a-uuid",
	})
	if err == nil {
		t.Fatal("hook signal accepted an invalid delivery ID")
	}

	_, err = NewHeuristicSignal(Signal{
		Kind:        KindProcessExited,
		VendorEvent: "process_exited",
		Scope:       ScopeRoot,
		Confidence:  1,
		ReceivedAt:  testTime,
	})
	if err == nil {
		t.Fatal("heuristic signal accepted a process kind")
	}
}

func TestHookActivationPermanentlySuppressesHeuristics(t *testing.T) {
	detector := newTestDetector(t)
	state := NewState(agent.HooksAuto)
	target := newTestAgent(t, agent.StateStarting, agent.RunModeInteractive)

	started := processObservation(t, KindProcessStarted, &ProcessFact{HookAvailable: true})
	decision := decideAndApply(t, detector, &state, target, started)
	if target.State() != agent.StateWorking ||
		decision.Timer().Deadline.Sub(testTime) != 5*time.Second {
		t.Fatalf("process decision = %+v, state = %s", decision.Timer(), target.State())
	}

	activated := signalObservation(t, hookSignal(t, "session", KindSessionStarted, ScopeRoot))
	decision = decideAndApply(t, detector, &state, target, activated)
	if state.Snapshot().HookStatus() != HookActive ||
		decision.Timer().Action != TimerCancel {
		t.Fatalf(
			"hook status = %s, timer = %+v",
			state.Snapshot().HookStatus(),
			decision.Timer(),
		)
	}

	blocked := signalObservation(t, heuristicSignal(t, KindHeuristicBlocked, 1, testTime))
	decision = decideAndApply(t, detector, &state, target, blocked)
	_, outcome, ok := decision.Signal()
	if !ok || outcome != OutcomeSuppressed || target.State() != agent.StateWorking {
		t.Fatalf("heuristic outcome = %s, state = %s", outcome, target.State())
	}
}

func TestHookPermissionAndIdleTimersAreCancelable(t *testing.T) {
	detector := newTestDetector(t)
	state := NewState(agent.HooksAuto)
	target := newTestAgent(t, agent.StateWorking, agent.RunModeInteractive)
	activateHook(t, detector, &state, target)

	permission := hookSignal(t, "permission", KindPermissionRequested, ScopeRoot)
	permission.VendorEvent = "PermissionRequest"
	decision := decideAndApply(
		t,
		detector,
		&state,
		target,
		signalObservation(t, permission),
	)
	permissionTimer := decision.Timer()
	if permissionTimer.Action != TimerArm ||
		permissionTimer.Deadline.Sub(testTime) != 750*time.Millisecond {
		t.Fatalf("permission timer = %+v", permissionTimer)
	}

	activity := hookSignal(t, "activity", KindToolActivity, ScopeRoot)
	activity.VendorEvent = "PreToolUse"
	decision = decideAndApply(
		t,
		detector,
		&state,
		target,
		signalObservation(t, activity),
	)
	if decision.Timer().Action != TimerCancel {
		t.Fatalf("activity timer plan = %+v, want cancel", decision.Timer())
	}

	stale := timerObservation(t, permissionTimer.Generation, permissionTimer.Deadline)
	decision = decideAndApply(t, detector, &state, target, stale)
	_, outcome, _ := decision.Signal()
	if outcome != OutcomeStale || target.State() != agent.StateWorking {
		t.Fatalf("stale timer outcome = %s, state = %s", outcome, target.State())
	}

	stop := hookSignal(t, "stop", KindTurnStopped, ScopeRoot)
	stop.VendorEvent = "Stop"
	decision = decideAndApply(
		t,
		detector,
		&state,
		target,
		signalObservation(t, stop),
	)
	idleTimer := decision.Timer()
	if idleTimer.Action != TimerArm ||
		idleTimer.Deadline.Sub(testTime) != time.Second {
		t.Fatalf("idle timer = %+v", idleTimer)
	}
	decideAndApply(
		t,
		detector,
		&state,
		target,
		timerObservation(t, idleTimer.Generation, idleTimer.Deadline),
	)
	if target.State() != agent.StateIdle {
		t.Fatalf("state = %s, want idle", target.State())
	}
}

func TestSubagentStopNeverChangesRootState(t *testing.T) {
	detector := newTestDetector(t)
	state := NewState(agent.HooksAuto)
	target := newTestAgent(t, agent.StateWorking, agent.RunModeInteractive)
	activateHook(t, detector, &state, target)

	signal := hookSignal(t, "subagent-stop", KindSubagentStopped, ScopeSubagent)
	signal.VendorEvent = "SubagentStop"
	decision := decideAndApply(
		t,
		detector,
		&state,
		target,
		signalObservation(t, signal),
	)
	if _, ok := decision.Change(); ok || decision.Timer().Action != TimerKeep {
		t.Fatalf("subagent stop decision changed root state: %+v", decision)
	}
	if target.State() != agent.StateWorking {
		t.Fatalf("state = %s, want working", target.State())
	}
}

func TestFallbackBlockedRecoveryAndSilence(t *testing.T) {
	detector := newTestDetector(t)
	state := NewState(agent.HooksOff)
	target := newTestAgent(t, agent.StateStarting, agent.RunModeInteractive)

	decision := decideAndApply(
		t,
		detector,
		&state,
		target,
		processObservation(t, KindProcessStarted, &ProcessFact{}),
	)
	if decision.Timer().Deadline.Sub(testTime) != 60*time.Second {
		t.Fatalf("initial silence timer = %+v", decision.Timer())
	}

	hint := heuristicSignal(t, KindHeuristicBlocked, 0.85, testTime.Add(time.Second))
	decision = decideAndApply(
		t,
		detector,
		&state,
		target,
		signalObservation(t, hint),
	)
	blockedTimer := decision.Timer()
	if blockedTimer.Deadline.Sub(hint.ReceivedAt) != 750*time.Millisecond {
		t.Fatalf("blocked timer = %+v", blockedTimer)
	}
	decideAndApply(
		t,
		detector,
		&state,
		target,
		timerObservation(t, blockedTimer.Generation, blockedTimer.Deadline),
	)
	if target.State() != agent.StateBlocked {
		t.Fatalf("state = %s, want blocked", target.State())
	}

	firstAt := blockedTimer.Deadline.Add(time.Second)
	first, err := ObserveOutput("claude", firstAt)
	if err != nil {
		t.Fatalf("first output: %v", err)
	}
	decision = decideAndApply(t, detector, &state, target, first)
	if _, ok := decision.Change(); ok {
		t.Fatal("one output observation recovered Blocked")
	}
	second, err := ObserveOutput("claude", firstAt.Add(500*time.Millisecond))
	if err != nil {
		t.Fatalf("second output: %v", err)
	}
	decision = decideAndApply(t, detector, &state, target, second)
	if target.State() != agent.StateWorking ||
		decision.Timer().Deadline.Sub(firstAt.Add(500*time.Millisecond)) != 60*time.Second {
		t.Fatalf("recovery state = %s, timer = %+v", target.State(), decision.Timer())
	}

	silenceTimer := decision.Timer()
	decideAndApply(
		t,
		detector,
		&state,
		target,
		timerObservation(t, silenceTimer.Generation, silenceTimer.Deadline),
	)
	if target.State() != agent.StateIdle {
		t.Fatalf("state after silence = %s, want idle", target.State())
	}
}

func TestHeuristicBlockedNeedsMinimumConfidenceAndConfirmation(t *testing.T) {
	detector := newTestDetector(t)
	state := NewState(agent.HooksOff)
	target := newTestAgent(t, agent.StateWorking, agent.RunModeInteractive)

	low := heuristicSignal(t, KindHeuristicBlocked, 0.84, testTime)
	decision := decideAndApply(
		t,
		detector,
		&state,
		target,
		signalObservation(t, low),
	)
	_, outcome, _ := decision.Signal()
	if outcome != OutcomeSuppressed || target.State() != agent.StateWorking {
		t.Fatalf("low-confidence outcome = %s, state = %s", outcome, target.State())
	}

	high := heuristicSignal(t, KindHeuristicBlocked, 0.85, testTime.Add(time.Second))
	decision = decideAndApply(
		t,
		detector,
		&state,
		target,
		signalObservation(t, high),
	)
	_, outcome, _ = decision.Signal()
	if outcome != OutcomeCandidate || target.State() != agent.StateWorking {
		t.Fatalf("candidate outcome = %s, state = %s", outcome, target.State())
	}
}

func TestDuplicateDeliveryCacheIsBounded(t *testing.T) {
	config := DefaultConfig()
	config.DeliveryRememberCount = 2
	detector, err := New(config)
	if err != nil {
		t.Fatalf("new detector: %v", err)
	}
	state := NewState(agent.HooksAuto)
	target := newTestAgent(t, agent.StateWorking, agent.RunModeInteractive)

	for _, label := range []string{"one", "two", "three"} {
		decision := decideAndApply(
			t,
			detector,
			&state,
			target,
			signalObservation(t, hookSignal(t, label, KindObserved, ScopeRoot)),
		)
		if decision.Duplicate() {
			t.Fatalf("first delivery %q was duplicate", label)
		}
	}

	evicted, err := detector.Decide(
		state.Snapshot(),
		target.Snapshot(),
		signalObservation(t, hookSignal(t, "one", KindObserved, ScopeRoot)),
	)
	if err != nil {
		t.Fatalf("decide evicted delivery: %v", err)
	}
	if evicted.Duplicate() {
		t.Fatal("evicted delivery remained in cache")
	}

	duplicate, err := detector.Decide(
		state.Snapshot(),
		target.Snapshot(),
		signalObservation(t, hookSignal(t, "three", KindObserved, ScopeRoot)),
	)
	if err != nil {
		t.Fatalf("decide duplicate: %v", err)
	}
	if !duplicate.Duplicate() {
		t.Fatal("recent delivery was not detected as duplicate")
	}
}

func TestProcessFactsOwnTerminalState(t *testing.T) {
	t.Run("oneshot success", func(t *testing.T) {
		detector := newTestDetector(t)
		state := NewState(agent.HooksOff)
		target := newTestAgent(t, agent.StateWorking, agent.RunModeOneshot)
		code := 0

		decision := decideAndApply(
			t,
			detector,
			&state,
			target,
			processObservation(t, KindProcessExited, &ProcessFact{
				ExitCode: &code,
				ExitKind: ExitSuccess,
			}),
		)
		_, outcome, _ := decision.Signal()
		if outcome != OutcomeTerminal ||
			target.State() != agent.StateDone ||
			state.Snapshot().HookStatus() != HookDetached {
			t.Fatalf(
				"outcome = %s, state = %s, hook = %s",
				outcome,
				target.State(),
				state.Snapshot().HookStatus(),
			)
		}
	})

	t.Run("required without activation", func(t *testing.T) {
		detector := newTestDetector(t)
		state := NewState(agent.HooksRequired)
		target := newTestAgent(t, agent.StateWorking, agent.RunModeOneshot)
		code := 0

		decideAndApply(
			t,
			detector,
			&state,
			target,
			processObservation(t, KindProcessExited, &ProcessFact{
				ExitCode: &code,
				ExitKind: ExitSuccess,
			}),
		)
		if target.State() != agent.StateStopped ||
			target.LastError() != "required hook not observed" {
			t.Fatalf("state = %s, error = %q", target.State(), target.LastError())
		}
	})
}

func TestRequiredActivationTimerFailsSession(t *testing.T) {
	detector := newTestDetector(t)
	state := NewState(agent.HooksRequired)
	target := newTestAgent(t, agent.StateStarting, agent.RunModeInteractive)

	started := decideAndApply(
		t,
		detector,
		&state,
		target,
		processObservation(t, KindProcessStarted, &ProcessFact{HookAvailable: true}),
	)
	timer := started.Timer()
	decideAndApply(
		t,
		detector,
		&state,
		target,
		timerObservation(t, timer.Generation, timer.Deadline),
	)
	if target.State() != agent.StateStopped ||
		target.LastError() != "required hook not observed" ||
		state.Snapshot().HookStatus() != HookRequiredFailed {
		t.Fatalf(
			"state = %s, error = %q, hook = %s",
			target.State(),
			target.LastError(),
			state.Snapshot().HookStatus(),
		)
	}
	late := decideAndApply(
		t,
		detector,
		&state,
		target,
		signalObservation(t, hookSignal(t, "late-required", KindSessionStarted, ScopeRoot)),
	)
	_, outcome, _ := late.Signal()
	if outcome != OutcomeTerminal ||
		state.Snapshot().HookStatus() != HookRequiredFailed {
		t.Fatalf(
			"late hook outcome = %s, hook status = %s",
			outcome,
			state.Snapshot().HookStatus(),
		)
	}
}

func TestApplyCommittedRejectsAStaleDecision(t *testing.T) {
	detector := newTestDetector(t)
	state := NewState(agent.HooksAuto)
	target := newTestAgent(t, agent.StateWorking, agent.RunModeInteractive)
	observation := signalObservation(t, hookSignal(t, "one", KindObserved, ScopeRoot))

	first, err := detector.Decide(state.Snapshot(), target.Snapshot(), observation)
	if err != nil {
		t.Fatalf("first decision: %v", err)
	}
	second, err := detector.Decide(state.Snapshot(), target.Snapshot(), observation)
	if err != nil {
		t.Fatalf("second decision: %v", err)
	}
	if err := state.ApplyCommitted(first); err != nil {
		t.Fatalf("apply first: %v", err)
	}
	if err := state.ApplyCommitted(second); !errors.Is(err, ErrStaleDecision) {
		t.Fatalf("apply stale decision error = %v", err)
	}
}

func newTestDetector(t *testing.T) *Detector {
	t.Helper()
	detector, err := New(Config{})
	if err != nil {
		t.Fatalf("new detector: %v", err)
	}
	return detector
}

func newTestAgent(
	t *testing.T,
	state agent.State,
	mode agent.RunMode,
) *agent.Agent {
	t.Helper()
	target, err := agent.Restore(agent.RestoreSnapshot{
		ID:         "agent-1",
		Name:       "test",
		Vendor:     "claude",
		RunMode:    mode,
		HookPolicy: agent.HooksAuto,
		State:      state,
		CreatedAt:  testTime.Add(-time.Minute),
		UpdatedAt:  testTime,
	})
	if err != nil {
		t.Fatalf("restore Agent: %v", err)
	}
	return target
}

func hookSignal(
	t *testing.T,
	label string,
	kind Kind,
	scope Scope,
) Signal {
	t.Helper()
	signal, err := NewHookSignal(Signal{
		Kind:            kind,
		Vendor:          "claude",
		VendorEvent:     "SessionStart",
		Scope:           scope,
		VendorSessionID: "vendor-session",
		Confidence:      1,
		ReceivedAt:      testTime,
		DeliveryID:      uuid.NewSHA1(uuid.NameSpaceOID, []byte(label)).String(),
	})
	if err != nil {
		t.Fatalf("new hook signal: %v", err)
	}
	return signal
}

func heuristicSignal(
	t *testing.T,
	kind Kind,
	confidence float64,
	at time.Time,
) Signal {
	t.Helper()
	signal, err := NewHeuristicSignal(Signal{
		Kind:        kind,
		Vendor:      "claude",
		VendorEvent: "terminal_hint",
		Scope:       ScopeRoot,
		Evidence:    "terminal hint",
		Confidence:  confidence,
		ReceivedAt:  at,
	})
	if err != nil {
		t.Fatalf("new heuristic signal: %v", err)
	}
	return signal
}

func processObservation(
	t *testing.T,
	kind Kind,
	fact *ProcessFact,
) Observation {
	t.Helper()
	signal, err := NewProcessSignal(Signal{
		Kind:        kind,
		VendorEvent: string(kind),
		Confidence:  1,
		ReceivedAt:  testTime,
		Process:     fact,
	})
	if err != nil {
		t.Fatalf("new process signal: %v", err)
	}
	return signalObservation(t, signal)
}

func signalObservation(t *testing.T, signal Signal) Observation {
	t.Helper()
	observation, err := ObserveSignal(signal)
	if err != nil {
		t.Fatalf("observe signal: %v", err)
	}
	return observation
}

func timerObservation(t *testing.T, generation uint64, at time.Time) Observation {
	t.Helper()
	observation, err := ObserveTimer(generation, at)
	if err != nil {
		t.Fatalf("observe timer: %v", err)
	}
	return observation
}

func decideAndApply(
	t *testing.T,
	detector *Detector,
	state *State,
	target *agent.Agent,
	observation Observation,
) Decision {
	t.Helper()
	decision, err := detector.Decide(state.Snapshot(), target.Snapshot(), observation)
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if change, ok := decision.Change(); ok {
		prepared, prepareErr := target.Prepare(change)
		if prepareErr != nil {
			t.Fatalf("prepare Agent change: %v", prepareErr)
		}
		if applyErr := target.ApplyCommitted(prepared); applyErr != nil {
			t.Fatalf("apply Agent change: %v", applyErr)
		}
	}
	if applyErr := state.ApplyCommitted(decision); applyErr != nil {
		t.Fatalf("apply Detector decision: %v", applyErr)
	}
	return decision
}

func activateHook(
	t *testing.T,
	detector *Detector,
	state *State,
	target *agent.Agent,
) {
	t.Helper()
	decideAndApply(
		t,
		detector,
		state,
		target,
		signalObservation(t, hookSignal(t, "activate", KindSessionStarted, ScopeRoot)),
	)
}
