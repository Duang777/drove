package agent

import "testing"

func TestValidStates(t *testing.T) {
	valid := []State{StatePending, StateStarting, StateWorking, StateBlocked, StateDone, StateIdle, StateStopped}
	for _, s := range valid {
		if !Valid(s) {
			t.Errorf("expected %q to be valid", s)
		}
	}
	if Valid("bogus") {
		t.Error("expected bogus state to be invalid")
	}
}

func TestLegalTransitions(t *testing.T) {
	cases := []struct {
		from, to State
		ok       bool
	}{
		{StateStarting, StateWorking, true},
		{StateStarting, StateStopped, true},
		{StateWorking, StateBlocked, true},
		{StateWorking, StateDone, true},
		{StateBlocked, StateWorking, true},
		{StateBlocked, StateDone, true},
		{StateIdle, StateWorking, true},
		{StateDone, StateStopped, true},
		// 非法迁移
		{StateStopped, StateWorking, false},
		{StateDone, StateWorking, false},
		{StatePending, StateDone, false},
		{StateBlocked, StateStarting, false},
	}
	for _, c := range cases {
		if got := CanTransition(c.from, c.to); got != c.ok {
			t.Errorf("CanTransition(%s, %s) = %v, want %v", c.from, c.to, got, c.ok)
		}
	}
}

func TestTransitionLifecycle(t *testing.T) {
	var events []struct{ from, to State }
	a := New("a1", WithStateChangeHook(func(_ ID, from, to State, _ string) {
		events = append(events, struct{ from, to State }{from, to})
	}))

	if a.State() != StatePending {
		t.Fatalf("initial state = %s, want pending", a.State())
	}
	if err := a.Transition(StateStarting, "init"); err != nil {
		t.Fatalf("starting transition failed: %v", err)
	}
	if err := a.Transition(StateWorking, "start"); err != nil {
		t.Fatalf("working transition failed: %v", err)
	}
	if err := a.Transition(StateBlocked, "awaiting input"); err != nil {
		t.Fatalf("blocked transition failed: %v", err)
	}
	if err := a.Transition(StateWorking, "resumed"); err != nil {
		t.Fatalf("resume transition failed: %v", err)
	}
	if err := a.Transition(StateDone, "complete"); err != nil {
		t.Fatalf("done transition failed: %v", err)
	}
	if err := a.Transition(StateStopped, "stopped"); err != nil {
		t.Fatalf("stopped transition failed: %v", err)
	}

	if len(events) != 6 {
		t.Fatalf("expected 6 state-change callbacks, got %d", len(events))
	}

	// 终态不可再迁移。
	if err := a.Transition(StateWorking, "illegal"); err == nil {
		t.Fatal("expected ErrInvalidTransition from stopped state")
	}
}

func TestSetError(t *testing.T) {
	a := New("a1")
	a.SetError("boom")
	if a.LastError() != "boom" {
		t.Fatalf("LastError = %q, want boom", a.LastError())
	}
}
