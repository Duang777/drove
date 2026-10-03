package detect

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/agent"
)

type detectorHarness struct {
	mu        sync.Mutex
	state     agent.State
	decisions []Decision
	applied   chan Decision
	timers    chan chan time.Time
}

func newDetectorHarness(t *testing.T, policy Policy, initial agent.State) (*Detector, *detectorHarness) {
	t.Helper()
	harness := &detectorHarness{
		state:   initial,
		applied: make(chan Decision, 32),
		timers:  make(chan chan time.Time, 8),
	}
	detector, err := New(Options{
		Policy:  policy,
		RunMode: agent.RunModeInteractive,
		State: func() agent.State {
			harness.mu.Lock()
			defer harness.mu.Unlock()
			return harness.state
		},
		Apply: func(_ context.Context, decision Decision) error {
			harness.mu.Lock()
			harness.decisions = append(harness.decisions, decision)
			if decision.Target != "" {
				harness.state = decision.Target
			}
			harness.mu.Unlock()
			harness.applied <- decision
			return nil
		},
		IdleDelay:      time.Second,
		ActivityWindow: time.Second,
		ActivityCount:  2,
		Now: func() time.Time {
			return time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)
		},
		After: func(time.Duration) <-chan time.Time {
			timer := make(chan time.Time, 1)
			harness.timers <- timer
			return timer
		},
	})
	if err != nil {
		t.Fatalf("new detector: %v", err)
	}
	t.Cleanup(detector.Close)
	return detector, harness
}

func (h *detectorHarness) snapshot() (agent.State, []Decision) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state, append([]Decision(nil), h.decisions...)
}

func hookSignal(deliveryID string, kind adapter.SignalKind) adapter.Signal {
	return adapter.Signal{
		DeliveryID:  deliveryID,
		Source:      adapter.SignalSourceHook,
		Kind:        kind,
		Vendor:      "claude",
		VendorEvent: "SessionStart",
		Scope:       adapter.SignalScopeRoot,
		Evidence:    "test",
		Confidence:  1,
		ReceivedAt:  time.Now().UTC(),
	}
}

func heuristicSignal(kind adapter.SignalKind, confidence float64) adapter.Signal {
	return adapter.Signal{
		Source:      adapter.SignalSourceHeuristic,
		Kind:        kind,
		Vendor:      "claude",
		VendorEvent: "terminal_hint",
		Scope:       adapter.SignalScopeRoot,
		Evidence:    "terminal evidence",
		Confidence:  confidence,
		ReceivedAt:  time.Now().UTC(),
	}
}

func TestDetectorFiltersHeuristicConfidence(t *testing.T) {
	detector, harness := newDetectorHarness(t, PolicyAuto, agent.StateWorking)

	if err := detector.Submit(context.Background(), heuristicSignal(adapter.SignalBlocked, 0.6)); err != nil {
		t.Fatalf("submit low confidence: %v", err)
	}
	if err := detector.Submit(context.Background(), heuristicSignal(adapter.SignalBlocked, 0.9)); err != nil {
		t.Fatalf("submit high confidence: %v", err)
	}

	state, decisions := harness.snapshot()
	if state != agent.StateBlocked {
		t.Fatalf("state = %s, want blocked", state)
	}
	if len(decisions) != 2 || decisions[0].Target != "" || decisions[1].Target != agent.StateBlocked {
		t.Fatalf("decisions = %+v", decisions)
	}
}

func TestDetectorHookActivationSuppressesHeuristicTransitions(t *testing.T) {
	detector, harness := newDetectorHarness(t, PolicyAuto, agent.StateWorking)

	if err := detector.Submit(context.Background(), hookSignal("delivery-1", adapter.SignalObserved)); err != nil {
		t.Fatalf("activate hook: %v", err)
	}
	select {
	case <-detector.Active():
	default:
		t.Fatal("active channel remained open")
	}
	if err := detector.Submit(context.Background(), heuristicSignal(adapter.SignalBlocked, 1)); err != nil {
		t.Fatalf("submit heuristic: %v", err)
	}

	state, decisions := harness.snapshot()
	if state != agent.StateWorking {
		t.Fatalf("state = %s, want working", state)
	}
	if len(decisions) != 2 || decisions[1].Target != "" {
		t.Fatalf("decisions = %+v", decisions)
	}
}

func TestDetectorDeduplicatesHookDelivery(t *testing.T) {
	detector, harness := newDetectorHarness(t, PolicyAuto, agent.StateWorking)
	signal := hookSignal("delivery-1", adapter.SignalBlocked)
	signal.VendorEvent = "PermissionRequest"

	if err := detector.Submit(context.Background(), signal); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if err := detector.Submit(context.Background(), signal); err != nil {
		t.Fatalf("duplicate delivery: %v", err)
	}

	state, decisions := harness.snapshot()
	if state != agent.StateBlocked || len(decisions) != 1 {
		t.Fatalf("state = %s, decisions = %+v", state, decisions)
	}
}

func TestDetectorRecoversBlockedAfterSustainedFallbackOutput(t *testing.T) {
	detector, harness := newDetectorHarness(t, PolicyAuto, agent.StateBlocked)
	first := time.Now().UTC()

	if err := detector.ObserveOutput(context.Background(), first); err != nil {
		t.Fatalf("first output: %v", err)
	}
	if err := detector.ObserveOutput(context.Background(), first.Add(100*time.Millisecond)); err != nil {
		t.Fatalf("second output: %v", err)
	}

	state, decisions := harness.snapshot()
	if state != agent.StateWorking {
		t.Fatalf("state = %s, want working", state)
	}
	if len(decisions) != 1 ||
		decisions[0].Signal.VendorEvent != "output_activity" ||
		decisions[0].Target != agent.StateWorking {
		t.Fatalf("decisions = %+v", decisions)
	}
}

func TestDetectorConfirmsAndCancelsIdleCandidate(t *testing.T) {
	t.Run("confirm", func(t *testing.T) {
		detector, harness := newDetectorHarness(t, PolicyAuto, agent.StateWorking)
		signal := hookSignal("delivery-stop", adapter.SignalIdle)
		signal.VendorEvent = "Stop"
		if err := detector.Submit(context.Background(), signal); err != nil {
			t.Fatalf("submit stop: %v", err)
		}
		timer := <-harness.timers
		timer <- time.Now()

		select {
		case decision := <-harness.applied:
			if decision.Signal.VendorEvent != "Stop" {
				t.Fatalf("first decision = %+v", decision)
			}
		case <-time.After(time.Second):
			t.Fatal("missing Stop audit decision")
		}
		select {
		case decision := <-harness.applied:
			if decision.Target != agent.StateIdle ||
				decision.Signal.Source != adapter.SignalSourceTimer {
				t.Fatalf("timer decision = %+v", decision)
			}
		case <-time.After(time.Second):
			t.Fatal("missing Idle confirmation")
		}
	})

	t.Run("cancel", func(t *testing.T) {
		detector, harness := newDetectorHarness(t, PolicyAuto, agent.StateWorking)
		stop := hookSignal("delivery-stop", adapter.SignalIdle)
		stop.VendorEvent = "Stop"
		if err := detector.Submit(context.Background(), stop); err != nil {
			t.Fatalf("submit stop: %v", err)
		}
		timer := <-harness.timers
		activity := hookSignal("delivery-working", adapter.SignalWorking)
		activity.VendorEvent = "PreToolUse"
		if err := detector.Submit(context.Background(), activity); err != nil {
			t.Fatalf("submit activity: %v", err)
		}
		timer <- time.Now()
		time.Sleep(10 * time.Millisecond)

		state, decisions := harness.snapshot()
		if state != agent.StateWorking || len(decisions) != 2 {
			t.Fatalf("state = %s, decisions = %+v", state, decisions)
		}
	})
}

func TestDetectorPoliciesAndSubagentIdle(t *testing.T) {
	t.Run("off rejects hook", func(t *testing.T) {
		detector, harness := newDetectorHarness(t, PolicyOff, agent.StateWorking)
		err := detector.Submit(context.Background(), hookSignal("delivery-1", adapter.SignalBlocked))
		if !errors.Is(err, ErrHooksDisabled) {
			t.Fatalf("error = %v, want ErrHooksDisabled", err)
		}
		_, decisions := harness.snapshot()
		if len(decisions) != 0 {
			t.Fatalf("decisions = %+v, want none", decisions)
		}
	})

	t.Run("required ignores heuristic transition", func(t *testing.T) {
		detector, harness := newDetectorHarness(t, PolicyRequired, agent.StateWorking)
		if err := detector.Submit(
			context.Background(),
			heuristicSignal(adapter.SignalBlocked, 1),
		); err != nil {
			t.Fatalf("submit heuristic: %v", err)
		}
		state, decisions := harness.snapshot()
		if state != agent.StateWorking || len(decisions) != 1 || decisions[0].Target != "" {
			t.Fatalf("state = %s, decisions = %+v", state, decisions)
		}
	})

	t.Run("subagent stop is not idle", func(t *testing.T) {
		detector, harness := newDetectorHarness(t, PolicyAuto, agent.StateWorking)
		signal := hookSignal("delivery-1", adapter.SignalIdle)
		signal.VendorEvent = "Stop"
		signal.Scope = adapter.SignalScopeSubagent
		if err := detector.Submit(context.Background(), signal); err != nil {
			t.Fatalf("submit subagent stop: %v", err)
		}
		state, decisions := harness.snapshot()
		if state != agent.StateWorking || len(decisions) != 1 || decisions[0].Target != "" {
			t.Fatalf("state = %s, decisions = %+v", state, decisions)
		}
		select {
		case <-harness.timers:
			t.Fatal("subagent Stop created Idle timer")
		default:
		}
	})
}
