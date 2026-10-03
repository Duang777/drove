package agent

import (
	"errors"
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
		RunMode:   RunModeOneshot,
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
		{name: "missing run mode", change: func(s *RestoreSnapshot) { s.RunMode = "" }, wantErr: "run mode"},
		{name: "invalid run mode", change: func(s *RestoreSnapshot) { s.RunMode = "batch" }, wantErr: "run mode"},
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

func TestRestoreBuildsSnapshotAndSupportsPlannedTransition(t *testing.T) {
	createdAt := time.Date(2026, time.October, 3, 3, 4, 5, 6, time.UTC)
	updatedAt := createdAt.Add(2 * time.Minute)

	a, err := Restore(RestoreSnapshot{
		ID:        "agent-1",
		Name:      "build-api",
		Vendor:    "generic",
		RunMode:   RunModeOneshot,
		State:     StateWorking,
		LastError: "previous warning",
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if a.ID() != "agent-1" ||
		a.Name() != "build-api" ||
		a.Vendor() != "generic" ||
		a.RunMode() != RunModeOneshot ||
		a.State() != StateWorking ||
		a.LastError() != "previous warning" ||
		!a.CreatedAt().Equal(createdAt) ||
		!a.UpdatedAt().Equal(updatedAt) {
		t.Fatalf("restored agent does not match snapshot")
	}

	plan, err := a.PlanTransition(StateDone, "completed")
	if err != nil {
		t.Fatalf("plan restored transition: %v", err)
	}
	if a.State() != StateWorking {
		t.Fatalf("planning changed state to %s", a.State())
	}
	appliedAt := updatedAt.Add(time.Minute)
	if err := a.ApplyTransition(plan, appliedAt); err != nil {
		t.Fatalf("apply restored transition: %v", err)
	}
	if a.State() != StateDone || !a.UpdatedAt().Equal(appliedAt) {
		t.Fatalf("applied state = %s at %s", a.State(), a.UpdatedAt())
	}
}

func TestRestoreRejectsOptionsThatInvalidateSnapshot(t *testing.T) {
	createdAt := time.Date(2026, time.October, 3, 3, 4, 5, 6, time.UTC)
	snapshot := RestoreSnapshot{
		ID:        "agent-1",
		Name:      "build-api",
		Vendor:    "generic",
		RunMode:   RunModeOneshot,
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
		{name: "invalid run mode", option: WithRunMode("batch")},
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

func TestRunMode(t *testing.T) {
	for _, mode := range []RunMode{RunModeInteractive, RunModeOneshot} {
		if !ValidRunMode(mode) {
			t.Errorf("expected %q to be valid", mode)
		}
	}
	for _, mode := range []RunMode{"", "batch", "Interactive"} {
		if ValidRunMode(mode) {
			t.Errorf("expected %q to be invalid", mode)
		}
	}

	if got := New("default").RunMode(); got != RunModeInteractive {
		t.Fatalf("default run mode = %q, want %q", got, RunModeInteractive)
	}
	if got := New("oneshot", WithRunMode(RunModeOneshot)).RunMode(); got != RunModeOneshot {
		t.Fatalf("configured run mode = %q, want %q", got, RunModeOneshot)
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
		{StateBlocked, StateIdle, true},
		{StateIdle, StateWorking, true},
		{StateIdle, StateDone, true},
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
	a := New("a1")

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

	// 终态不可再迁移。
	if err := a.Transition(StateWorking, "illegal"); err == nil {
		t.Fatal("expected ErrInvalidTransition from stopped state")
	}
}

func TestTransitionPlanRejectsStaleAndForeignApply(t *testing.T) {
	a := New("a1")
	first, err := a.PlanTransition(StateStarting, "first")
	if err != nil {
		t.Fatalf("plan first transition: %v", err)
	}
	if err := a.ApplyTransition(first, time.Now().UTC()); err != nil {
		t.Fatalf("apply first transition: %v", err)
	}
	if err := a.ApplyTransition(first, time.Now().UTC()); !errors.Is(err, ErrStaleTransitionPlan) {
		t.Fatalf("reapply error = %v, want ErrStaleTransitionPlan", err)
	}

	foreign := New("a2")
	foreignPlan, err := foreign.PlanTransition(StateStarting, "foreign")
	if err != nil {
		t.Fatalf("plan foreign transition: %v", err)
	}
	if err := a.ApplyTransition(foreignPlan, time.Now().UTC()); !errors.Is(err, ErrStaleTransitionPlan) {
		t.Fatalf("foreign apply error = %v, want ErrStaleTransitionPlan", err)
	}
}

func TestSetError(t *testing.T) {
	a := New("a1")
	a.SetError("boom")
	if a.LastError() != "boom" {
		t.Fatalf("LastError = %q, want boom", a.LastError())
	}
}
