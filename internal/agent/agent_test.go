package agent

import (
	"strings"
	"testing"
	"time"
)

func TestRestoreRejectsInvalidSnapshot(t *testing.T) {
	createdAt := time.Date(2026, time.October, 3, 3, 4, 5, 6, time.UTC)
	valid := RestoreSnapshot{
		ID:        "agent-1",
		Name:      "build-api",
		Vendor:    "generic",
		State:     StateStopped,
		CreatedAt: createdAt,
		UpdatedAt: createdAt.Add(time.Minute),
	}

	tests := []struct {
		name    string
		change  func(*RestoreSnapshot)
		wantErr string
	}{
		{name: "missing ID", change: func(s *RestoreSnapshot) { s.ID = "" }, wantErr: "ID"},
		{name: "blank ID", change: func(s *RestoreSnapshot) { s.ID = "   " }, wantErr: "ID"},
		{name: "missing name", change: func(s *RestoreSnapshot) { s.Name = "" }, wantErr: "name"},
		{name: "missing vendor", change: func(s *RestoreSnapshot) { s.Vendor = "" }, wantErr: "vendor"},
		{name: "invalid state", change: func(s *RestoreSnapshot) { s.State = "unknown" }, wantErr: "state"},
		{name: "missing creation time", change: func(s *RestoreSnapshot) { s.CreatedAt = time.Time{} }, wantErr: "creation time"},
		{name: "missing update time", change: func(s *RestoreSnapshot) { s.UpdatedAt = time.Time{} }, wantErr: "update time"},
		{
			name:    "update before creation",
			change:  func(s *RestoreSnapshot) { s.UpdatedAt = s.CreatedAt.Add(-time.Nanosecond) },
			wantErr: "before creation time",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := valid
			test.change(&snapshot)

			a, err := Restore(snapshot)
			if err == nil {
				t.Fatal("restore succeeded with invalid snapshot")
			}
			if a != nil {
				t.Fatalf("restore returned agent %v with error %v", a.ID(), err)
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %q, want %q context", err, test.wantErr)
			}
		})
	}
}

func TestRestoreBuildsSnapshotWithoutCallingStateHook(t *testing.T) {
	createdAt := time.Date(2026, time.October, 3, 3, 4, 5, 6, time.UTC)
	updatedAt := createdAt.Add(2 * time.Minute)
	var transitions int

	a, err := Restore(RestoreSnapshot{
		ID:        "agent-1",
		Name:      "build-api",
		Vendor:    "generic",
		State:     StateWorking,
		LastError: "previous warning",
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, WithStateChangeHook(func(_ ID, _, _ State, _ string) {
		transitions++
	}))
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if transitions != 0 {
		t.Fatalf("restore fired %d state hooks, want 0", transitions)
	}
	if a.ID() != "agent-1" ||
		a.Name() != "build-api" ||
		a.Vendor() != "generic" ||
		a.State() != StateWorking ||
		a.LastError() != "previous warning" ||
		!a.CreatedAt().Equal(createdAt) ||
		!a.UpdatedAt().Equal(updatedAt) {
		t.Fatalf("restored agent does not match snapshot")
	}

	if err := a.Transition(StateDone, "completed"); err != nil {
		t.Fatalf("transition restored agent: %v", err)
	}
	if transitions != 1 {
		t.Fatalf("transition fired %d state hooks, want 1", transitions)
	}
}

func TestRestoreRejectsOptionsThatInvalidateSnapshot(t *testing.T) {
	createdAt := time.Date(2026, time.October, 3, 3, 4, 5, 6, time.UTC)
	snapshot := RestoreSnapshot{
		ID:        "agent-1",
		Name:      "build-api",
		Vendor:    "generic",
		State:     StateStopped,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}

	tests := []struct {
		name   string
		option Option
	}{
		{name: "blank name", option: WithName(" ")},
		{name: "blank vendor", option: WithVendor(" ")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a, err := Restore(snapshot, test.option)
			if err == nil {
				t.Fatal("restore succeeded after an option invalidated the snapshot")
			}
			if a != nil {
				t.Fatalf("restore returned agent %v with error %v", a.ID(), err)
			}
		})
	}
}

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
