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
		ID:         "agent-1",
		Name:       "build-api",
		Vendor:     "generic",
		WorkingDir: "/workspace/api",
		RunMode:    RunModeOneshot,
		State:      StateWorking,
		LastError:  "previous warning",
		CreatedAt:  createdAt,
		UpdatedAt:  updatedAt,
	})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if a.ID() != "agent-1" ||
		a.Name() != "build-api" ||
		a.Vendor() != "generic" ||
		a.WorkingDir() != "/workspace/api" ||
		a.RunMode() != RunModeOneshot ||
		a.State() != StateWorking ||
		a.LastError() != "previous warning" ||
		!a.CreatedAt().Equal(createdAt) ||
		!a.UpdatedAt().Equal(updatedAt) {
		t.Fatalf("restored agent does not match snapshot")
	}

	prepared, err := a.Prepare(MoveTo(StateDone, "completed", Evidence{
		Source:     EvidenceProcess,
		Event:      "process_exited",
		Confidence: 1,
	}))
	if err != nil {
		t.Fatalf("prepare restored transition: %v", err)
	}
	if a.State() != StateWorking {
		t.Fatalf("planning changed state to %s", a.State())
	}
	if err := a.ApplyCommitted(prepared); err != nil {
		t.Fatalf("apply restored transition: %v", err)
	}
	if a.State() != StateDone || !a.UpdatedAt().Equal(prepared.Timestamp()) {
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

func TestHookPolicyAndEvidenceValidation(t *testing.T) {
	for _, policy := range []HookPolicy{HooksOff, HooksAuto, HooksRequired} {
		if !ValidHookPolicy(policy) {
			t.Errorf("expected %q to be valid", policy)
		}
	}
	if ValidHookPolicy("") || ValidHookPolicy("sometimes") {
		t.Fatal("invalid hook policy was accepted")
	}

	for _, source := range []EvidenceSource{EvidenceHook, EvidenceNotify} {
		evidence := Evidence{
			Source:     source,
			Event:      "PostToolUse",
			Confidence: 1,
			DeliveryID: "550e8400-e29b-41d4-a716-446655440000",
		}
		if err := evidence.Validate(); err != nil {
			t.Fatalf("validate %s evidence: %v", source, err)
		}
		evidence.DeliveryID = "delivery-1"
		if err := evidence.Validate(); err == nil {
			t.Fatalf("%s accepted a non-canonical delivery ID", source)
		}
	}
}

func TestScreenAttributionAndEvidenceValidation(t *testing.T) {
	valid, err := NewScreenAttribution(
		"claude.approval_prompt",
		ScreenEdgeCleared,
		"viewport.bottom",
		4312,
		918,
		"approval prompt",
	)
	if err != nil {
		t.Fatalf("new screen attribution: %v", err)
	}
	evidence := Evidence{
		Source:     EvidenceScreen,
		Event:      valid.Rule,
		Confidence: 1,
		Screen:     &valid,
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("validate screen evidence: %v", err)
	}

	tests := []struct {
		name   string
		change func(*ScreenAttribution)
	}{
		{name: "empty rule", change: func(a *ScreenAttribution) { a.Rule = "" }},
		{name: "invalid edge", change: func(a *ScreenAttribution) { a.Edge = "changed" }},
		{name: "empty region", change: func(a *ScreenAttribution) { a.Region = "" }},
		{name: "zero offset", change: func(a *ScreenAttribution) { a.OutputOffset = 0 }},
		{name: "zero sequence", change: func(a *ScreenAttribution) { a.LastOutputSeq = 0 }},
		{name: "dynamic evidence", change: func(a *ScreenAttribution) { a.Evidence = "line\ntext" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			attribution := valid
			test.change(&attribution)
			if err := attribution.Validate(); err == nil {
				t.Fatal("invalid screen attribution was accepted")
			}
		})
	}

	mismatched := evidence
	mismatched.Event = "claude.idle_prompt"
	if err := mismatched.Validate(); err == nil {
		t.Fatal("screen evidence accepted a mismatched event")
	}
	foreign := Evidence{
		Source:     EvidenceHeuristic,
		Event:      "heuristic_blocked",
		Confidence: 1,
		Screen:     &valid,
	}
	if err := foreign.Validate(); err == nil {
		t.Fatal("non-screen evidence accepted screen attribution")
	}
}

func TestLastTransitionCopiesScreenAttribution(t *testing.T) {
	attribution, err := NewScreenAttribution(
		"claude.idle_prompt",
		ScreenEdgePresent,
		"viewport.bottom",
		12,
		4,
		"idle prompt",
	)
	if err != nil {
		t.Fatalf("new screen attribution: %v", err)
	}
	target := New("screen-copy")
	apply := func(change Change) {
		t.Helper()
		prepared, prepareErr := target.Prepare(change)
		if prepareErr != nil {
			t.Fatalf("prepare transition: %v", prepareErr)
		}
		if applyErr := target.ApplyCommitted(prepared); applyErr != nil {
			t.Fatalf("apply transition: %v", applyErr)
		}
	}
	apply(MoveTo(StateStarting, "start", Evidence{
		Source: EvidenceSession, Event: "session_start", Confidence: 1,
	}))
	apply(MoveTo(StateWorking, "working", Evidence{
		Source: EvidenceProcess, Event: "process_started", Confidence: 1,
	}))
	prepared, err := target.Prepare(MoveTo(StateIdle, "screen idle", Evidence{
		Source:     EvidenceScreen,
		Event:      attribution.Rule,
		Confidence: 1,
		Screen:     &attribution,
	}))
	if err != nil {
		t.Fatalf("prepare screen transition: %v", err)
	}
	attribution.Rule = "mutated-before-apply"
	_, _, _, exposed, ok := prepared.Transition()
	if !ok {
		t.Fatal("prepared transition is missing")
	}
	exposed.Screen.Rule = "mutated-return-value"
	if err := target.ApplyCommitted(prepared); err != nil {
		t.Fatalf("apply screen transition: %v", err)
	}

	first := target.LastTransition()
	first.Screen.Rule = "mutated"
	second := target.LastTransition()
	if second.Screen.Rule != "claude.idle_prompt" {
		t.Fatalf("stored screen rule = %q, want copied attribution", second.Screen.Rule)
	}
}

func TestSignalInjectionMetadataValidation(t *testing.T) {
	if !ValidSignalInjectionMode(SignalInjectionAuto) ||
		!ValidSignalInjectionMode(SignalInjectionOff) ||
		ValidSignalInjectionMode("required") {
		t.Fatal("signal injection mode validation is incorrect")
	}
	if !ValidSignalInjectionResult(
		InjectionInjected,
		InjectionReasonSessionConfig,
	) || ValidSignalInjectionResult(
		InjectionInjected,
		InjectionReasonRelayUnavailable,
	) {
		t.Fatal("signal injection result validation is incorrect")
	}

	target := New(
		"agent",
		WithSignalInjection(
			SignalInjectionAuto,
			InjectionSkipped,
			InjectionReasonRelayUnavailable,
		),
	)
	status, reason := target.SignalInjectionResult()
	if target.SignalInjection() != SignalInjectionAuto ||
		status != InjectionSkipped ||
		reason != InjectionReasonRelayUnavailable {
		t.Fatalf(
			"signal injection = %s/%s/%s",
			target.SignalInjection(),
			status,
			reason,
		)
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
		{StateIdle, StateBlocked, true},
		{StateIdle, StateDone, true},
		{StateDone, StateStopped, false},
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
	a := New("a1", WithRunMode(RunModeOneshot))
	apply := func(to State, reason string, evidence Evidence) {
		t.Helper()
		prepared, err := a.Prepare(MoveTo(to, reason, evidence))
		if err != nil {
			t.Fatalf("prepare %s: %v", to, err)
		}
		if err := a.ApplyCommitted(prepared); err != nil {
			t.Fatalf("apply %s: %v", to, err)
		}
	}

	if a.State() != StatePending {
		t.Fatalf("initial state = %s, want pending", a.State())
	}
	apply(StateStarting, "init", Evidence{Source: EvidenceSession, Event: "session_start", Confidence: 1})
	apply(StateWorking, "start", Evidence{Source: EvidenceProcess, Event: "process_started", Confidence: 1})
	apply(StateBlocked, "awaiting input", Evidence{Source: EvidenceHeuristic, Event: "heuristic_blocked", Confidence: 0.9})
	apply(StateWorking, "resumed", Evidence{Source: EvidenceHeuristic, Event: "output_activity", Confidence: 0.9})
	apply(StateDone, "complete", Evidence{Source: EvidenceProcess, Event: "process_exited", Confidence: 1})

	// 终态不可再迁移。
	if _, err := a.Prepare(MoveTo(StateWorking, "illegal", Evidence{
		Source: EvidenceSession, Event: "invalid", Confidence: 1,
	})); err == nil {
		t.Fatal("expected ErrInvalidTransition from stopped state")
	}
}

func TestPreparedChangeAppliesStateAndErrorAtomically(t *testing.T) {
	a := New(
		"a1",
		WithRunMode(RunModeOneshot),
		WithHookPolicy(HooksAuto),
	)
	prepared, err := a.Prepare(FailTo(
		StateStopped,
		"startup failed",
		"executable not found",
		Evidence{
			Source:     EvidenceProcess,
			Event:      "process_start_failed",
			Confidence: 1,
		},
	))
	if err != nil {
		t.Fatalf("prepare change: %v", err)
	}
	if snapshot := a.Snapshot(); snapshot.State != StatePending || snapshot.Revision != 0 {
		t.Fatalf("prepare mutated agent: %+v", snapshot)
	}
	from, to, reason, evidence, ok := prepared.Transition()
	if !ok ||
		from != StatePending ||
		to != StateStopped ||
		reason != "startup failed" ||
		evidence.Event != "process_start_failed" {
		t.Fatalf("prepared transition = %s -> %s %q %+v, ok=%t", from, to, reason, evidence, ok)
	}
	if message, ok := prepared.ErrorMessage(); !ok || message != "executable not found" {
		t.Fatalf("prepared error = %q, ok=%t", message, ok)
	}

	if err := a.ApplyCommitted(prepared); err != nil {
		t.Fatalf("apply committed: %v", err)
	}
	if a.State() != StateStopped ||
		a.LastError() != "executable not found" ||
		a.LastTransition() == nil ||
		a.LastTransition().Event != "process_start_failed" ||
		!a.UpdatedAt().Equal(prepared.Timestamp()) ||
		a.Snapshot().Revision != 1 {
		t.Fatalf("applied agent state is inconsistent")
	}
	if err := a.ApplyCommitted(prepared); !errors.Is(err, ErrStaleTransitionPlan) {
		t.Fatalf("reapply error = %v, want ErrStaleTransitionPlan", err)
	}
}

func TestPreparedChangeRestrictsDoneToProcessExit(t *testing.T) {
	a := New("a1", WithRunMode(RunModeOneshot))
	starting, err := a.Prepare(MoveTo(StateStarting, "start", Evidence{
		Source: EvidenceSession, Event: "session_start", Confidence: 1,
	}))
	if err != nil {
		t.Fatalf("prepare starting: %v", err)
	}
	if err := a.ApplyCommitted(starting); err != nil {
		t.Fatalf("apply starting: %v", err)
	}
	working, err := a.Prepare(MoveTo(StateWorking, "working", Evidence{
		Source: EvidenceProcess, Event: "process_started", Confidence: 1,
	}))
	if err != nil {
		t.Fatalf("prepare working: %v", err)
	}
	if err := a.ApplyCommitted(working); err != nil {
		t.Fatalf("apply working: %v", err)
	}

	if _, err := a.Prepare(MoveTo(StateDone, "hook completed", Evidence{
		Source: EvidenceHook, Event: "TaskCompleted", Confidence: 1,
		DeliveryID: "550e8400-e29b-41d4-a716-446655440000",
	})); err == nil {
		t.Fatal("hook evidence was allowed to produce Done")
	}
	if _, err := a.Prepare(MoveTo(StateDone, "process exited", Evidence{
		Source: EvidenceProcess, Event: "process_exited", Confidence: 1,
	})); err != nil {
		t.Fatalf("natural oneshot exit was rejected: %v", err)
	}
}

func TestRecordError(t *testing.T) {
	a := New("a1")
	prepared, err := a.Prepare(RecordError("boom", Evidence{
		Source: EvidenceProcess, Event: "process_start_failed", Confidence: 1,
	}))
	if err != nil {
		t.Fatalf("prepare error: %v", err)
	}
	if err := a.ApplyCommitted(prepared); err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if a.LastError() != "boom" {
		t.Fatalf("LastError = %q, want boom", a.LastError())
	}
}
