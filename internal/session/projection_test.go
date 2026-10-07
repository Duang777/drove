package session

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
)

func TestRecoveryProjectorReconcilesEveryState(t *testing.T) {
	base := time.Date(2026, time.October, 3, 4, 5, 6, 7, time.UTC)
	tests := []struct {
		name            string
		stateRow        *store.EventRow
		wantRows        int
		wantInterrupted int
		wantError       string
	}{
		{name: "pending", wantRows: 2, wantInterrupted: 1, wantError: restartInterruptionError},
		{
			name:            "starting",
			stateRow:        &store.EventRow{From: "pending", To: "starting"},
			wantRows:        2,
			wantInterrupted: 1,
			wantError:       restartInterruptionError,
		},
		{
			name:            "working",
			stateRow:        &store.EventRow{From: "starting", To: "working"},
			wantRows:        2,
			wantInterrupted: 1,
			wantError:       restartInterruptionError,
		},
		{
			name:            "blocked",
			stateRow:        &store.EventRow{From: "working", To: "blocked"},
			wantRows:        2,
			wantInterrupted: 1,
			wantError:       restartInterruptionError,
		},
		{
			name:            "idle",
			stateRow:        &store.EventRow{From: "working", To: "idle"},
			wantRows:        2,
			wantInterrupted: 1,
			wantError:       restartInterruptionError,
		},
		{
			name:     "done",
			stateRow: &store.EventRow{From: "working", To: "done"},
			wantRows: 1,
		},
		{
			name:     "stopped",
			stateRow: &store.EventRow{From: "pending", To: "stopped"},
			wantRows: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projector := newRecoveryProjector()
			if err := projector.Apply(store.EventRow{
				Seq:       1,
				Timestamp: base,
				Type:      string(event.TypeSessionLifecycle),
				SessionID: "agent-1",
				AgentID:   "agent-1",
				Reason:    "created",
				Payload:   `{"version":1,"name":"agent","vendor":"generic"}`,
			}); err != nil {
				t.Fatalf("apply creation: %v", err)
			}
			lastInputSeq := uint64(1)
			if test.stateRow != nil {
				row := *test.stateRow
				row.Seq = 2
				row.Timestamp = base.Add(time.Second)
				row.Type = string(event.TypeStateChanged)
				row.SessionID = "agent-1"
				row.AgentID = "agent-1"
				if err := projector.Apply(row); err != nil {
					t.Fatalf("apply state: %v", err)
				}
				lastInputSeq = 2
			}

			plan, err := projector.Finish(base.Add(time.Hour))
			if err != nil {
				t.Fatalf("finish projection: %v", err)
			}
			if len(plan.Snapshots) != 1 || plan.Snapshots[0].State != agent.StateStopped {
				t.Fatalf("snapshots = %+v, want one stopped session", plan.Snapshots)
			}
			if plan.Snapshots[0].RunMode != agent.RunModeOneshot {
				t.Fatalf("run mode = %q, want %q", plan.Snapshots[0].RunMode, agent.RunModeOneshot)
			}
			if plan.Snapshots[0].LastError != test.wantError {
				t.Fatalf("last error = %q, want %q", plan.Snapshots[0].LastError, test.wantError)
			}
			if len(plan.Reconciliation) != test.wantRows {
				t.Fatalf("reconciliation rows = %d, want %d", len(plan.Reconciliation), test.wantRows)
			}
			if plan.Report.Interrupted != test.wantInterrupted {
				t.Fatalf("interrupted = %d, want %d", plan.Report.Interrupted, test.wantInterrupted)
			}
			if plan.Report.LastSeq != lastInputSeq+uint64(test.wantRows) {
				t.Fatalf("last seq = %d, want %d", plan.Report.LastSeq, lastInputSeq+uint64(test.wantRows))
			}
		})
	}
}

func TestRecoveryProjectorUsesLegacyMetadataAndFactTimestamps(t *testing.T) {
	createdAt := time.Date(2026, time.October, 3, 5, 0, 0, 0, time.UTC)
	projector := newRecoveryProjector()
	for _, row := range []store.EventRow{
		{
			Seq:       1,
			Timestamp: createdAt,
			Type:      string(event.TypeStateChanged),
			SessionID: "legacy-agent",
			From:      "pending",
			To:        "stopped",
		},
		{
			Seq:       2,
			Timestamp: createdAt.Add(-time.Hour),
			Type:      string(event.TypeError),
			SessionID: "legacy-agent",
			Payload:   "persisted failure",
		},
		{
			Seq:       3,
			Timestamp: createdAt.Add(time.Hour),
			Type:      string(event.TypeOutput),
			SessionID: "legacy-agent",
			Payload:   "ignored output",
		},
		{
			Seq:       4,
			Timestamp: createdAt.Add(90 * time.Minute),
			Type:      string(event.TypeOutputChunk),
			SessionID: "legacy-agent",
			Payload:   `{"version":99}`,
		},
		{
			Seq:       5,
			Timestamp: createdAt.Add(2 * time.Hour),
			Type:      string(event.TypeError),
			SessionID: "legacy-agent",
		},
	} {
		if err := projector.Apply(row); err != nil {
			t.Fatalf("apply seq %d: %v", row.Seq, err)
		}
	}

	plan, err := projector.Finish(createdAt.Add(3 * time.Hour))
	if err != nil {
		t.Fatalf("finish projection: %v", err)
	}
	if plan.Report.LegacyMetadata != 1 || plan.Report.Sessions != 1 || plan.Report.LastSeq != 5 {
		t.Fatalf("report = %+v", plan.Report)
	}
	if len(plan.Reconciliation) != 0 {
		t.Fatalf("reconciliation = %+v, want none", plan.Reconciliation)
	}
	snapshot := plan.Snapshots[0]
	if snapshot.Name != "legacy-agent" ||
		snapshot.Vendor != "unknown" ||
		snapshot.RunMode != agent.RunModeOneshot ||
		snapshot.LastError != "persisted failure" ||
		!snapshot.CreatedAt.Equal(createdAt) ||
		!snapshot.UpdatedAt.Equal(createdAt) {
		t.Fatalf("legacy snapshot = %+v", snapshot)
	}
}

func TestRecoveryProjectorKeepsStateTimeAcrossLaterErrors(t *testing.T) {
	createdAt := time.Date(2026, time.October, 3, 6, 0, 0, 0, time.UTC)
	stateSince := createdAt.Add(time.Minute)
	updatedAt := createdAt.Add(2 * time.Minute)
	projector := newRecoveryProjector()
	for _, row := range []store.EventRow{
		{
			Seq:       1,
			Timestamp: createdAt,
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Reason:    "created",
			Payload:   `{"version":1,"name":"agent","vendor":"generic"}`,
		},
		{
			Seq:       2,
			Timestamp: stateSince,
			Type:      string(event.TypeStateChanged),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			From:      "pending",
			To:        "stopped",
		},
		{
			Seq:       3,
			Timestamp: updatedAt,
			Type:      string(event.TypeError),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Payload:   "late diagnostic",
		},
	} {
		if err := projector.Apply(row); err != nil {
			t.Fatalf("apply seq %d: %v", row.Seq, err)
		}
	}

	plan, err := projector.Finish(createdAt.Add(3 * time.Minute))
	if err != nil {
		t.Fatalf("finish projection: %v", err)
	}
	if len(plan.Snapshots) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(plan.Snapshots))
	}
	snapshot := plan.Snapshots[0]
	if !snapshot.StateSince.Equal(stateSince) {
		t.Fatalf("state since = %s, want %s", snapshot.StateSince, stateSince)
	}
	if !snapshot.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("updated at = %s, want %s", snapshot.UpdatedAt, updatedAt)
	}
}

func TestRecoveryProjectorIgnoresOutputAndErrorOnlySessions(t *testing.T) {
	base := time.Date(2026, time.October, 3, 5, 0, 0, 0, time.UTC)
	projector := newRecoveryProjector()
	for _, row := range []store.EventRow{
		{Seq: 1, Timestamp: base, Type: string(event.TypeOutput), SessionID: "orphan", Payload: "line"},
		{Seq: 2, Timestamp: base, Type: string(event.TypeOutputChunk), SessionID: "orphan", Payload: `{"version":99}`},
		{Seq: 3, Timestamp: base, Type: string(event.TypeError), SessionID: "orphan", Payload: "failure"},
		{Seq: 4, Timestamp: base, Type: string(event.TypeOutput), Payload: "daemon output"},
		{Seq: 5, Timestamp: base, Type: string(event.TypeOutputChunk), Payload: `{"version":99}`},
		{Seq: 6, Timestamp: base, Type: string(event.TypeError), Payload: "daemon error"},
	} {
		if err := projector.Apply(row); err != nil {
			t.Fatalf("apply seq %d: %v", row.Seq, err)
		}
	}

	plan, err := projector.Finish(base.Add(time.Hour))
	if err != nil {
		t.Fatalf("finish projection: %v", err)
	}
	if len(plan.Snapshots) != 0 || len(plan.Reconciliation) != 0 {
		t.Fatalf("plan = %+v, want no sessions", plan)
	}
	if plan.Report.ScannedEvents != 6 || plan.Report.Sessions != 0 || plan.Report.LastSeq != 6 {
		t.Fatalf("report = %+v", plan.Report)
	}
}

func TestRecoveryProjectorIgnoresInputOnlySessions(t *testing.T) {
	base := time.Date(2026, time.October, 3, 5, 0, 0, 0, time.UTC)
	projector := newRecoveryProjector()
	if err := projector.Apply(store.EventRow{
		Seq:       1,
		Timestamp: base,
		Type:      string(event.TypeAgentInput),
		SessionID: "orphan",
		AgentID:   "orphan",
		Reason:    "accepted",
		Payload:   `{"version":1,"bytes":9}`,
	}); err != nil {
		t.Fatalf("apply input: %v", err)
	}

	plan, err := projector.Finish(base.Add(time.Hour))
	if err != nil {
		t.Fatalf("finish projection: %v", err)
	}
	if len(plan.Snapshots) != 0 || len(plan.Reconciliation) != 0 {
		t.Fatalf("plan = %+v, want no sessions", plan)
	}
	if plan.Report.ScannedEvents != 1 || plan.Report.Sessions != 0 || plan.Report.LastSeq != 1 {
		t.Fatalf("report = %+v", plan.Report)
	}
}

func TestRecoveryProjectorIgnoresSignalOnlySessions(t *testing.T) {
	base := time.Date(2026, time.October, 3, 5, 0, 0, 0, time.UTC)
	projector := newRecoveryProjector()
	if err := projector.Apply(store.EventRow{
		Seq:       1,
		Timestamp: base,
		Type:      string(event.TypeAgentSignal),
		SessionID: "orphan",
		AgentID:   "orphan",
		Reason:    "hook",
		Payload: `{"version":1,"source":"hook","kind":"session_started","vendor":"claude",` +
			`"vendor_event":"SessionStart","scope":"root","confidence":1,` +
			`"received_at":"2026-10-03T05:00:00Z",` +
			`"delivery_id":"550e8400-e29b-41d4-a716-446655440000","outcome":"observed"}`,
	}); err != nil {
		t.Fatalf("apply signal: %v", err)
	}

	plan, err := projector.Finish(base.Add(time.Hour))
	if err != nil {
		t.Fatalf("finish projection: %v", err)
	}
	if len(plan.Snapshots) != 0 || len(plan.Reconciliation) != 0 {
		t.Fatalf("plan = %+v, want no sessions", plan)
	}
	if plan.Report.ScannedEvents != 1 || plan.Report.Sessions != 0 || plan.Report.LastSeq != 1 {
		t.Fatalf("report = %+v", plan.Report)
	}
}

func TestRecoveryProjectorRestoresLatestVendorSessionReference(t *testing.T) {
	base := time.Date(2026, time.October, 3, 5, 0, 0, 0, time.UTC)
	projector := newRecoveryProjector()
	rows := []store.EventRow{
		{
			Seq:       1,
			Timestamp: base,
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Reason:    "created",
			Payload:   `{"version":1,"name":"agent","vendor":"claude"}`,
		},
		{
			Seq:       2,
			Timestamp: base.Add(time.Second),
			Type:      string(event.TypeAgentSignal),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Reason:    "hook",
			Payload: `{"version":1,"source":"hook","kind":"session_started","vendor":"claude",` +
				`"vendor_event":"SessionStart","scope":"root","vendor_session_id":"legacy-ref",` +
				`"confidence":1,"received_at":"2026-10-03T05:00:01Z",` +
				`"delivery_id":"550e8400-e29b-41d4-a716-446655440000","outcome":"observed"}`,
		},
		{
			Seq:       3,
			Timestamp: base.Add(2 * time.Second),
			Type:      string(event.TypeAgentSignal),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Reason:    "hook",
			Payload: `{"version":1,"source":"hook","kind":"observed","vendor":"claude",` +
				`"vendor_event":"Notification","scope":"root","vendor_session_ref":"current-ref",` +
				`"confidence":1,"received_at":"2026-10-03T05:00:02Z",` +
				`"delivery_id":"550e8400-e29b-41d4-a716-446655440001","outcome":"observed"}`,
		},
	}
	for _, row := range rows {
		if err := projector.Apply(row); err != nil {
			t.Fatalf("apply seq %d: %v", row.Seq, err)
		}
	}

	plan, err := projector.Finish(base.Add(time.Hour))
	if err != nil {
		t.Fatalf("finish projection: %v", err)
	}
	if got := plan.VendorSessionRefs["agent-1"]; got != "current-ref" {
		t.Fatalf("vendor session reference = %q, want current-ref", got)
	}
	if !plan.ResumeOnStart["agent-1"] {
		t.Fatal("nonterminal recovered session is not eligible for automatic resume")
	}
}

func TestRecoveryProjectorRestoresProcessGroupCleanupFence(t *testing.T) {
	base := time.Date(2026, time.October, 6, 15, 0, 0, 0, time.UTC)
	rows := []store.EventRow{
		{
			Seq:       1,
			Timestamp: base,
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Reason:    "created",
			Payload:   `{"version":1,"name":"agent","vendor":"claude"}`,
		},
		{
			Seq:       2,
			Timestamp: base.Add(time.Second),
			Type:      string(event.TypeStateChanged),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			From:      "pending",
			To:        "starting",
		},
		{
			Seq:       3,
			Timestamp: base.Add(2 * time.Second),
			Type:      string(event.TypeStateChanged),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			From:      "starting",
			To:        "working",
		},
		{
			Seq:       4,
			Timestamp: base.Add(3 * time.Second),
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Reason:    processGroupCleanupFailed,
			Payload:   `{"version":1,"pid":4242}`,
		},
		{
			Seq:       5,
			Timestamp: base.Add(4 * time.Second),
			Type:      string(event.TypeStateChanged),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			From:      "working",
			To:        "stopped",
		},
	}
	project := func(t *testing.T, rows []store.EventRow) recoveryPlan {
		t.Helper()
		projector := newRecoveryProjector()
		for _, row := range rows {
			if err := projector.Apply(row); err != nil {
				t.Fatalf("apply seq %d: %v", row.Seq, err)
			}
		}
		plan, err := projector.Finish(base.Add(time.Hour))
		if err != nil {
			t.Fatalf("finish projection: %v", err)
		}
		return plan
	}

	pending := project(t, rows)
	if got := pending.ProcessGroupCleanup["agent-1"]; got != 4242 {
		t.Fatalf("restored process group PID = %d, want 4242", got)
	}

	rows = append(rows, store.EventRow{
		Seq:       6,
		Timestamp: base.Add(5 * time.Second),
		Type:      string(event.TypeSessionLifecycle),
		SessionID: "agent-1",
		AgentID:   "agent-1",
		Reason:    processGroupCleanupCompleted,
		Payload:   `{"version":1,"pid":4242}`,
	})
	completed := project(t, rows)
	if _, exists := completed.ProcessGroupCleanup["agent-1"]; exists {
		t.Fatalf(
			"completed process group cleanup remained pending: %+v",
			completed.ProcessGroupCleanup,
		)
	}
}

func TestRecoveryProjectorPreservesResumeAfterPersistedRestartStop(
	t *testing.T,
) {
	base := time.Date(2026, time.October, 5, 12, 0, 0, 0, time.UTC)
	rows := []store.EventRow{
		{
			Seq:       1,
			Timestamp: base,
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Reason:    "created",
			Payload:   `{"version":1,"name":"agent","vendor":"claude"}`,
		},
		{
			Seq:       2,
			Timestamp: base.Add(time.Second),
			Type:      string(event.TypeStateChanged),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			From:      "pending",
			To:        "starting",
		},
		{
			Seq:       3,
			Timestamp: base.Add(2 * time.Second),
			Type:      string(event.TypeStateChanged),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			From:      "starting",
			To:        "working",
		},
		{
			Seq:       4,
			Timestamp: base.Add(3 * time.Second),
			Type:      string(event.TypeAgentSignal),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Reason:    "hook",
			Payload: `{"version":1,"source":"hook","kind":"observed","vendor":"claude",` +
				`"vendor_event":"Notification","scope":"root","vendor_session_ref":"resume-ref",` +
				`"confidence":1,"received_at":"2026-10-05T12:00:02Z",` +
				`"delivery_id":"550e8400-e29b-41d4-a716-446655440001","outcome":"observed"}`,
		},
	}
	first := newRecoveryProjector()
	for _, row := range rows {
		if err := first.Apply(row); err != nil {
			t.Fatalf("apply initial seq %d: %v", row.Seq, err)
		}
	}
	firstPlan, err := first.Finish(base.Add(time.Hour))
	if err != nil {
		t.Fatalf("finish initial projection: %v", err)
	}
	rows = append(rows, firstPlan.Reconciliation...)

	restarted := newRecoveryProjector()
	for _, row := range rows {
		if err := restarted.Apply(row); err != nil {
			t.Fatalf("apply restarted seq %d: %v", row.Seq, err)
		}
	}
	restartedPlan, err := restarted.Finish(base.Add(2 * time.Hour))
	if err != nil {
		t.Fatalf("finish restarted projection: %v", err)
	}
	if !restartedPlan.ResumeOnStart["agent-1"] {
		t.Fatal("persisted daemon restart stop lost automatic resume intent")
	}
	if len(restartedPlan.Reconciliation) != 0 {
		t.Fatalf(
			"restarted reconciliation = %+v, want no duplicate stop",
			restartedPlan.Reconciliation,
		)
	}

	nextSeq := uint64(len(rows) + 1)
	rows = append(
		rows,
		store.EventRow{
			Seq:       nextSeq,
			Timestamp: base.Add(3 * time.Hour),
			Type:      string(event.TypeAgentResumed),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Reason:    "requested",
			Payload:   `{"version":1,"vendor_session_ref":"resume-ref"}`,
		},
		store.EventRow{
			Seq:       nextSeq + 1,
			Timestamp: base.Add(3*time.Hour + time.Second),
			Type:      string(event.TypeStateChanged),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			From:      "stopped",
			To:        "starting",
		},
		store.EventRow{
			Seq:       nextSeq + 2,
			Timestamp: base.Add(3*time.Hour + 2*time.Second),
			Type:      string(event.TypeStateChanged),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			From:      "starting",
			To:        "stopped",
			Reason:    "startup failed",
			Payload: `{"version":1,"source":"process","event":"process_start_failed",` +
				`"confidence":1}`,
		},
	)
	failedResume := newRecoveryProjector()
	for _, row := range rows {
		if err := failedResume.Apply(row); err != nil {
			t.Fatalf("apply failed resume seq %d: %v", row.Seq, err)
		}
	}
	failedPlan, err := failedResume.Finish(base.Add(4 * time.Hour))
	if err != nil {
		t.Fatalf("finish failed resume projection: %v", err)
	}
	if !failedPlan.ResumeOnStart["agent-1"] {
		t.Fatal("failed startup resume lost automatic resume intent")
	}
}

func TestRecoveryProjectorDoesNotResumeDoneStartupResume(t *testing.T) {
	base := time.Date(2026, time.October, 6, 1, 0, 0, 0, time.UTC)
	rows := []store.EventRow{
		{
			Seq: 1, Timestamp: base, Type: string(event.TypeSessionLifecycle),
			SessionID: "agent-1", AgentID: "agent-1", Reason: "created",
			Payload: `{"version":1,"name":"agent","vendor":"claude"}`,
		},
		{
			Seq: 2, Timestamp: base.Add(time.Second), Type: string(event.TypeAgentSignal),
			SessionID: "agent-1", AgentID: "agent-1", Reason: "hook",
			Payload: `{"version":1,"source":"hook","kind":"observed","vendor":"claude",` +
				`"vendor_event":"Notification","scope":"root","vendor_session_ref":"resume-ref",` +
				`"confidence":1,"received_at":"2026-10-06T01:00:01Z",` +
				`"delivery_id":"550e8400-e29b-41d4-a716-446655440001","outcome":"observed"}`,
		},
		{
			Seq: 3, Timestamp: base.Add(2 * time.Second), Type: string(event.TypeStateChanged),
			SessionID: "agent-1", AgentID: "agent-1", From: "pending", To: "starting",
		},
		{
			Seq: 4, Timestamp: base.Add(3 * time.Second), Type: string(event.TypeStateChanged),
			SessionID: "agent-1", AgentID: "agent-1", From: "starting", To: "working",
		},
		{
			Seq: 5, Timestamp: base.Add(4 * time.Second), Type: string(event.TypeStateChanged),
			SessionID: "agent-1", AgentID: "agent-1", From: "working", To: "stopped",
			Reason: restartStopReason,
		},
		{
			Seq: 6, Timestamp: base.Add(5 * time.Second), Type: string(event.TypeAgentResumed),
			SessionID: "agent-1", AgentID: "agent-1", Reason: "requested",
			Payload: `{"version":1,"vendor_session_ref":"resume-ref"}`,
		},
		{
			Seq: 7, Timestamp: base.Add(6 * time.Second), Type: string(event.TypeStateChanged),
			SessionID: "agent-1", AgentID: "agent-1", From: "stopped", To: "starting",
		},
		{
			Seq: 8, Timestamp: base.Add(7 * time.Second), Type: string(event.TypeStateChanged),
			SessionID: "agent-1", AgentID: "agent-1", From: "starting", To: "working",
		},
		{
			Seq: 9, Timestamp: base.Add(8 * time.Second), Type: string(event.TypeStateChanged),
			SessionID: "agent-1", AgentID: "agent-1", From: "working", To: "done",
		},
	}
	projector := newRecoveryProjector()
	for _, row := range rows {
		if err := projector.Apply(row); err != nil {
			t.Fatalf("apply seq %d: %v", row.Seq, err)
		}
	}
	plan, err := projector.Finish(base.Add(time.Hour))
	if err != nil {
		t.Fatalf("finish projection: %v", err)
	}
	if plan.ResumeOnStart["agent-1"] {
		t.Fatal("completed startup resume remained eligible without completion marker")
	}
	rows = append(rows, plan.Reconciliation...)
	restarted := newRecoveryProjector()
	for _, row := range rows {
		if err := restarted.Apply(row); err != nil {
			t.Fatalf("apply restarted seq %d: %v", row.Seq, err)
		}
	}
	restartedPlan, err := restarted.Finish(base.Add(2 * time.Hour))
	if err != nil {
		t.Fatalf("finish restarted projection: %v", err)
	}
	if restartedPlan.ResumeOnStart["agent-1"] {
		t.Fatal("completed startup resume became eligible after reconciliation")
	}
}

func TestRecoveryProjectorRequiresDurableCompletionToConsumeStartupResume(
	t *testing.T,
) {
	base := time.Date(2026, time.October, 5, 13, 0, 0, 0, time.UTC)
	baseRows := []store.EventRow{
		{
			Seq: 1, Timestamp: base, Type: string(event.TypeSessionLifecycle),
			SessionID: "agent-1", AgentID: "agent-1", Reason: "created",
			Payload: `{"version":1,"name":"agent","vendor":"claude"}`,
		},
		{
			Seq: 2, Timestamp: base.Add(time.Second), Type: string(event.TypeAgentSignal),
			SessionID: "agent-1", AgentID: "agent-1", Reason: "hook",
			Payload: `{"version":1,"source":"hook","kind":"observed","vendor":"claude",` +
				`"vendor_event":"Notification","scope":"root","vendor_session_ref":"resume-ref",` +
				`"confidence":1,"received_at":"2026-10-05T13:00:01Z",` +
				`"delivery_id":"550e8400-e29b-41d4-a716-446655440001","outcome":"observed"}`,
		},
		{
			Seq: 3, Timestamp: base.Add(2 * time.Second), Type: string(event.TypeStateChanged),
			SessionID: "agent-1", AgentID: "agent-1", From: "pending", To: "starting",
		},
		{
			Seq: 4, Timestamp: base.Add(3 * time.Second), Type: string(event.TypeStateChanged),
			SessionID: "agent-1", AgentID: "agent-1", From: "starting", To: "working",
		},
		{
			Seq: 5, Timestamp: base.Add(4 * time.Second), Type: string(event.TypeStateChanged),
			SessionID: "agent-1", AgentID: "agent-1", From: "working", To: "stopped",
			Reason: restartStopReason,
		},
		{
			Seq: 6, Timestamp: base.Add(5 * time.Second), Type: string(event.TypeAgentResumed),
			SessionID: "agent-1", AgentID: "agent-1", Reason: "requested",
			Payload: `{"version":1,"vendor_session_ref":"resume-ref"}`,
		},
		{
			Seq: 7, Timestamp: base.Add(6 * time.Second), Type: string(event.TypeStateChanged),
			SessionID: "agent-1", AgentID: "agent-1", From: "stopped", To: "starting",
		},
		{
			Seq: 8, Timestamp: base.Add(7 * time.Second), Type: string(event.TypeStateChanged),
			SessionID: "agent-1", AgentID: "agent-1", From: "starting", To: "working",
		},
	}
	tests := []struct {
		name       string
		finalRows  []store.EventRow
		wantResume bool
	}{
		{
			name: "legacy explicit user stop",
			finalRows: []store.EventRow{{
				Seq: 9, Timestamp: base.Add(8 * time.Second), Type: string(event.TypeStateChanged),
				SessionID: "agent-1", AgentID: "agent-1", From: "working", To: "stopped",
				Reason: "user stop",
			}},
			wantResume: false,
		},
		{
			name: "user stop with process group cleanup failure",
			finalRows: []store.EventRow{
				{
					Seq: 9, Timestamp: base.Add(8 * time.Second),
					Type:      string(event.TypeSessionLifecycle),
					SessionID: "agent-1", AgentID: "agent-1",
					Reason:  processGroupCleanupFailed,
					Payload: `{"version":1,"pid":4242}`,
				},
				{
					Seq: 10, Timestamp: base.Add(9 * time.Second),
					Type:      string(event.TypeSessionLifecycle),
					SessionID: "agent-1", AgentID: "agent-1",
					Reason:  startupResumeCancelledReason,
					Payload: `{"version":1}`,
				},
				{
					Seq: 11, Timestamp: base.Add(10 * time.Second),
					Type:      string(event.TypeStateChanged),
					SessionID: "agent-1", AgentID: "agent-1",
					From: "working", To: "stopped",
					Reason: "process exited code=0",
				},
				{
					Seq: 12, Timestamp: base.Add(11 * time.Second),
					Type:      string(event.TypeSessionLifecycle),
					SessionID: "agent-1", AgentID: "agent-1",
					Reason:  processGroupCleanupCompleted,
					Payload: `{"version":1,"pid":4242}`,
				},
			},
			wantResume: false,
		},
		{
			name: "required hook failure",
			finalRows: []store.EventRow{{
				Seq: 9, Timestamp: base.Add(8 * time.Second), Type: string(event.TypeStateChanged),
				SessionID: "agent-1", AgentID: "agent-1", From: "working", To: "stopped",
				Reason: "required hook activation failed",
			}},
			wantResume: true,
		},
		{
			name: "durable completion",
			finalRows: []store.EventRow{
				{
					Seq: 9, Timestamp: base.Add(8 * time.Second),
					Type:      string(event.TypeSessionLifecycle),
					SessionID: "agent-1", AgentID: "agent-1",
					Reason:  startupResumeCompletedReason,
					Payload: `{"version":1}`,
				},
				{
					Seq: 10, Timestamp: base.Add(9 * time.Second),
					Type:      string(event.TypeStateChanged),
					SessionID: "agent-1", AgentID: "agent-1",
					From: "working", To: "stopped",
					Reason: "process exited",
				},
			},
			wantResume: false,
		},
		{
			name: "terminal exit before durable completion",
			finalRows: []store.EventRow{
				{
					Seq: 9, Timestamp: base.Add(8 * time.Second),
					Type:      string(event.TypeStateChanged),
					SessionID: "agent-1", AgentID: "agent-1",
					From: "working", To: "done",
					Reason: "process exited",
				},
				{
					Seq: 10, Timestamp: base.Add(9 * time.Second),
					Type:      string(event.TypeSessionLifecycle),
					SessionID: "agent-1", AgentID: "agent-1",
					Reason:  startupResumeCompletedReason,
					Payload: `{"version":1}`,
				},
			},
			wantResume: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projector := newRecoveryProjector()
			rows := append(
				append([]store.EventRow(nil), baseRows...),
				test.finalRows...,
			)
			for _, row := range rows {
				if err := projector.Apply(row); err != nil {
					t.Fatalf("apply seq %d: %v", row.Seq, err)
				}
			}
			plan, err := projector.Finish(base.Add(time.Hour))
			if err != nil {
				t.Fatalf("finish projection: %v", err)
			}
			if got := plan.ResumeOnStart["agent-1"]; got != test.wantResume {
				t.Fatalf(
					"startup resume eligibility = %t, want %t",
					got,
					test.wantResume,
				)
			}
		})
	}
}

func TestRecoveryProjectorDoesNotResumeCompletedSessionAfterRestartStop(
	t *testing.T,
) {
	base := time.Date(2026, time.October, 5, 12, 0, 0, 0, time.UTC)
	rows := []store.EventRow{
		{
			Seq:       1,
			Timestamp: base,
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Reason:    "created",
			Payload:   `{"version":1,"name":"agent","vendor":"claude"}`,
		},
		{
			Seq:       2,
			Timestamp: base.Add(time.Second),
			Type:      string(event.TypeAgentSignal),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Reason:    "hook",
			Payload: `{"version":1,"source":"hook","kind":"observed","vendor":"claude",` +
				`"vendor_event":"Notification","scope":"root","vendor_session_ref":"done-ref",` +
				`"confidence":1,"received_at":"2026-10-05T12:00:01Z",` +
				`"delivery_id":"550e8400-e29b-41d4-a716-446655440001","outcome":"observed"}`,
		},
		{
			Seq:       3,
			Timestamp: base.Add(2 * time.Second),
			Type:      string(event.TypeStateChanged),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			From:      "working",
			To:        "done",
		},
	}
	first := newRecoveryProjector()
	for _, row := range rows {
		if err := first.Apply(row); err != nil {
			t.Fatalf("apply initial seq %d: %v", row.Seq, err)
		}
	}
	firstPlan, err := first.Finish(base.Add(time.Hour))
	if err != nil {
		t.Fatalf("finish initial projection: %v", err)
	}
	if firstPlan.ResumeOnStart["agent-1"] {
		t.Fatal("completed session is eligible for automatic resume")
	}
	rows = append(rows, firstPlan.Reconciliation...)

	restarted := newRecoveryProjector()
	for _, row := range rows {
		if err := restarted.Apply(row); err != nil {
			t.Fatalf("apply restarted seq %d: %v", row.Seq, err)
		}
	}
	restartedPlan, err := restarted.Finish(base.Add(2 * time.Hour))
	if err != nil {
		t.Fatalf("finish restarted projection: %v", err)
	}
	if restartedPlan.ResumeOnStart["agent-1"] {
		t.Fatal("persisted done restart stop enabled automatic resume")
	}
	if len(restartedPlan.Reconciliation) != 0 {
		t.Fatalf(
			"restarted reconciliation = %+v, want no duplicate stop",
			restartedPlan.Reconciliation,
		)
	}
}

func TestRecoveryProjectorReadsVersionTwoMetadataAndEvidence(t *testing.T) {
	base := time.Date(2026, time.October, 3, 5, 0, 0, 0, time.UTC)
	projector := newRecoveryProjector()
	rows := []store.EventRow{
		{
			Seq:       1,
			Timestamp: base,
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Reason:    "created",
			Payload: `{"version":2,"name":"agent","vendor":"claude",` +
				`"mode":"interactive","hook_policy":"auto"}`,
		},
		{
			Seq:       2,
			Timestamp: base.Add(time.Second),
			Type:      string(event.TypeStateChanged),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			From:      "pending",
			To:        "stopped",
			Reason:    "startup failed",
			Payload: `{"version":1,"source":"process","event":"process_start_failed",` +
				`"confidence":1}`,
		},
	}
	for _, row := range rows {
		if err := projector.Apply(row); err != nil {
			t.Fatalf("apply seq %d: %v", row.Seq, err)
		}
	}

	plan, err := projector.Finish(base.Add(time.Hour))
	if err != nil {
		t.Fatalf("finish projection: %v", err)
	}
	snapshot := plan.Snapshots[0]
	if snapshot.HookPolicy != agent.HooksAuto ||
		snapshot.SignalInjection != agent.SignalInjectionOff ||
		snapshot.InjectionStatus != agent.InjectionDetached ||
		snapshot.InjectionReason != agent.InjectionReasonRecovered ||
		snapshot.LastTransition == nil ||
		snapshot.LastTransition.Source != agent.EvidenceProcess ||
		snapshot.LastTransition.Event != "process_start_failed" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestRecoveryProjectorReadsAdditiveVersionTwoInjectionMetadata(t *testing.T) {
	base := time.Date(2026, time.October, 3, 5, 0, 0, 0, time.UTC)
	projector := newRecoveryProjector()
	rows := []store.EventRow{
		{
			Seq:       1,
			Timestamp: base,
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Reason:    "created",
			Payload: `{"version":2,"name":"agent","vendor":"claude",` +
				`"mode":"interactive","hook_policy":"auto",` +
				`"signal_injection":"auto","signal_injection_status":"injected",` +
				`"signal_injection_reason":"session_config"}`,
		},
		{
			Seq:       2,
			Timestamp: base.Add(time.Second),
			Type:      string(event.TypeStateChanged),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			From:      "pending",
			To:        "stopped",
			Reason:    "startup failed",
			Payload: `{"version":1,"source":"process","event":"process_start_failed",` +
				`"confidence":1}`,
		},
	}
	for _, row := range rows {
		if err := projector.Apply(row); err != nil {
			t.Fatalf("apply seq %d: %v", row.Seq, err)
		}
	}
	plan, err := projector.Finish(base.Add(time.Hour))
	if err != nil {
		t.Fatalf("finish projection: %v", err)
	}
	snapshot := plan.Snapshots[0]
	if snapshot.SignalInjection != agent.SignalInjectionAuto ||
		snapshot.InjectionStatus != agent.InjectionDetached ||
		snapshot.InjectionReason != agent.InjectionReasonRecovered {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestRecoveryProjectorCountsUnknownAuditPayloadVersions(t *testing.T) {
	base := time.Date(2026, time.October, 3, 5, 0, 0, 0, time.UTC)
	projector := newRecoveryProjector()
	for _, row := range []store.EventRow{
		{
			Seq:       1,
			Timestamp: base,
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Reason:    "created",
			Payload:   `{"version":1,"name":"agent","vendor":"generic"}`,
		},
		{
			Seq:       2,
			Timestamp: base.Add(time.Second),
			Type:      string(event.TypeAgentSignal),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Payload:   `{"version":99}`,
		},
		{
			Seq:       3,
			Timestamp: base.Add(2 * time.Second),
			Type:      string(event.TypeStateChanged),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			From:      "pending",
			To:        "stopped",
			Payload:   `{"version":99}`,
		},
	} {
		if err := projector.Apply(row); err != nil {
			t.Fatalf("apply seq %d: %v", row.Seq, err)
		}
	}
	plan, err := projector.Finish(base.Add(time.Hour))
	if err != nil {
		t.Fatalf("finish projection: %v", err)
	}
	if plan.Report.UnknownSignalPayloadVersions != 1 ||
		plan.Report.UnknownStateEvidenceVersions != 1 {
		t.Fatalf("report = %+v", plan.Report)
	}
}

func TestRecoveryProjectorReadsScreenV3WithoutOutputAttachments(t *testing.T) {
	base := time.Date(2026, time.October, 4, 10, 0, 0, 0, time.UTC)
	screen := &event.ScreenAttributionPayload{
		Rule:          "claude.approval_prompt",
		Edge:          "present",
		Region:        "viewport.bottom",
		OutputOffset:  4312,
		LastOutputSeq: 918,
		Evidence:      "approval prompt",
	}
	projector := newRecoveryProjector()
	rows := []store.EventRow{
		{
			Seq: 1, Timestamp: base, Type: string(event.TypeSessionLifecycle),
			SessionID: "agent-1", AgentID: "agent-1", Reason: "created",
			Payload: `{"version":1,"name":"agent","vendor":"claude"}`,
		},
		{
			Seq: 2, Timestamp: base.Add(time.Second), Type: string(event.TypeStateChanged),
			SessionID: "agent-1", AgentID: "agent-1", From: "pending", To: "starting",
			Payload: `{"version":1,"source":"session","event":"session_start","confidence":1}`,
		},
		{
			Seq: 3, Timestamp: base.Add(2 * time.Second), Type: string(event.TypeStateChanged),
			SessionID: "agent-1", AgentID: "agent-1", From: "starting", To: "working",
			Payload: `{"version":1,"source":"process","event":"process_started","confidence":1}`,
		},
	}
	for _, outcome := range []string{
		"candidate",
		"suppressed",
		"transitioned",
		"stale",
		"terminal",
	} {
		payload := event.SignalPayloadV3{
			SignalPayloadV1: event.SignalPayloadV1{
				Version: 3, Source: "screen", Kind: "human_input_required",
				Vendor: "claude", VendorEvent: "screen_rule", Scope: "root",
				Confidence: 1, ReceivedAt: base.Format(time.RFC3339Nano),
				Outcome: outcome,
			},
			Screen: screen,
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("encode %s signal: %v", outcome, err)
		}
		rows = append(rows, store.EventRow{
			Seq: uint64(len(rows) + 1), Timestamp: base.Add(time.Duration(len(rows)) * time.Second),
			Type: string(event.TypeAgentSignal), SessionID: "agent-1", AgentID: "agent-1",
			Payload: string(encoded),
		})
	}
	statePayload, err := json.Marshal(event.StateEvidencePayloadV3{
		StateEvidencePayloadV1: event.StateEvidencePayloadV1{
			Version: 3, Source: "screen", Event: screen.Rule, Confidence: 1,
		},
		Screen: screen,
	})
	if err != nil {
		t.Fatalf("encode screen state evidence: %v", err)
	}
	rows = append(rows, store.EventRow{
		Seq: uint64(len(rows) + 1), Timestamp: base.Add(time.Duration(len(rows)) * time.Second),
		Type: string(event.TypeStateChanged), SessionID: "agent-1", AgentID: "agent-1",
		From: "working", To: "blocked", Reason: "screen approval confirmed",
		Payload: string(statePayload),
	})

	for _, row := range rows {
		if len(row.OutputAttachment) != 0 {
			t.Fatalf("seed row %d unexpectedly has output attachment", row.Seq)
		}
		if err := projector.Apply(row); err != nil {
			t.Fatalf("apply seq %d: %v", row.Seq, err)
		}
	}
	draft := projector.sessions["agent-1"]
	if draft.lastTransition == nil ||
		draft.lastTransition.Source != agent.EvidenceScreen ||
		draft.lastTransition.Screen == nil ||
		draft.lastTransition.Screen.Rule != screen.Rule {
		t.Fatalf("screen transition = %+v", draft.lastTransition)
	}
	plan, err := projector.Finish(base.Add(time.Hour))
	if err != nil {
		t.Fatalf("finish projection: %v", err)
	}
	if plan.Report.UnknownSignalPayloadVersions != 0 ||
		plan.Report.UnknownStateEvidenceVersions != 0 {
		t.Fatalf("recovery report = %+v", plan.Report)
	}
}

func TestRecoveryProjectorReadsTerminalV4(t *testing.T) {
	base := time.Date(2026, time.October, 5, 10, 0, 0, 0, time.UTC)
	terminal := &event.TerminalAttributionPayload{
		Protocol:      "osc9",
		OutputOffset:  4312,
		LastOutputSeq: 918,
	}
	signalPayload, err := json.Marshal(event.SignalPayloadV4{
		SignalPayloadV1: event.SignalPayloadV1{
			Version: 4, Source: "notify", Kind: "permission_requested",
			Vendor: "codex", VendorEvent: "tui_notification", Scope: "root",
			Notification: "approval-requested", Evidence: "approval requested",
			Confidence: 1, ReceivedAt: base.Format(time.RFC3339Nano),
			Outcome: "candidate",
		},
		Terminal: terminal,
	})
	if err != nil {
		t.Fatalf("encode terminal signal: %v", err)
	}
	statePayload, err := json.Marshal(event.StateEvidencePayloadV4{
		StateEvidencePayloadV1: event.StateEvidencePayloadV1{
			Version: 4, Source: "notify", Event: "tui_notification", Confidence: 1,
		},
		Terminal: terminal,
	})
	if err != nil {
		t.Fatalf("encode terminal state evidence: %v", err)
	}

	projector := newRecoveryProjector()
	rows := []store.EventRow{
		{
			Seq: 1, Timestamp: base, Type: string(event.TypeSessionLifecycle),
			SessionID: "agent-1", AgentID: "agent-1", Reason: "created",
			Payload: `{"version":1,"name":"agent","vendor":"codex"}`,
		},
		{
			Seq: 2, Timestamp: base.Add(time.Second), Type: string(event.TypeStateChanged),
			SessionID: "agent-1", AgentID: "agent-1", From: "pending", To: "starting",
			Payload: `{"version":1,"source":"session","event":"session_start","confidence":1}`,
		},
		{
			Seq: 3, Timestamp: base.Add(2 * time.Second), Type: string(event.TypeStateChanged),
			SessionID: "agent-1", AgentID: "agent-1", From: "starting", To: "working",
			Payload: `{"version":1,"source":"process","event":"process_started","confidence":1}`,
		},
		{
			Seq: 4, Timestamp: base.Add(3 * time.Second), Type: string(event.TypeAgentSignal),
			SessionID: "agent-1", AgentID: "agent-1", Payload: string(signalPayload),
		},
		{
			Seq: 5, Timestamp: base.Add(4 * time.Second), Type: string(event.TypeStateChanged),
			SessionID: "agent-1", AgentID: "agent-1", From: "working", To: "blocked",
			Reason: "notify permission request confirmed", Payload: string(statePayload),
		},
	}
	for _, row := range rows {
		if err := projector.Apply(row); err != nil {
			t.Fatalf("apply seq %d: %v", row.Seq, err)
		}
	}
	evidence := projector.sessions["agent-1"].lastTransition
	if evidence == nil ||
		evidence.Source != agent.EvidenceNotify ||
		evidence.Event != "tui_notification" ||
		evidence.DeliveryID != "" ||
		evidence.Terminal == nil ||
		evidence.Terminal.OutputOffset != terminal.OutputOffset {
		t.Fatalf("terminal transition = %+v", evidence)
	}
	plan, err := projector.Finish(base.Add(time.Hour))
	if err != nil {
		t.Fatalf("finish projection: %v", err)
	}
	if plan.Report.UnknownSignalPayloadVersions != 0 ||
		plan.Report.UnknownStateEvidenceVersions != 0 {
		t.Fatalf("report = %+v", plan.Report)
	}
}

func TestRecoveryProjectorRejectsMalformedKnownScreenV3(t *testing.T) {
	base := time.Date(2026, time.October, 4, 10, 0, 0, 0, time.UTC)
	tests := []store.EventRow{
		{
			Seq: 1, Timestamp: base, Type: string(event.TypeAgentSignal),
			SessionID: "agent-1", AgentID: "agent-1",
			Payload: `{"version":3,"source":"screen","kind":"idle_prompt",` +
				`"vendor":"claude","vendor_event":"screen_rule","scope":"root",` +
				`"confidence":1,"received_at":"2026-10-04T10:00:00Z","outcome":"candidate"}`,
		},
		{
			Seq: 1, Timestamp: base, Type: string(event.TypeStateChanged),
			SessionID: "agent-1", AgentID: "agent-1", From: "pending", To: "starting",
			Payload: `{"version":3,"source":"screen",` +
				`"event":"claude.approval_prompt","confidence":1}`,
		},
	}
	for _, row := range tests {
		projector := newRecoveryProjector()
		if err := projector.Apply(row); err == nil {
			t.Fatalf("malformed %s v3 payload was accepted", row.Type)
		}
	}
}

func TestRecoveryProjectorAcceptsV3WithoutScreenForOtherSource(t *testing.T) {
	projector := newRecoveryProjector()
	row := store.EventRow{
		Seq: 1, Timestamp: time.Date(2026, time.October, 4, 10, 0, 0, 0, time.UTC),
		Type: string(event.TypeStateChanged), SessionID: "agent-1", AgentID: "agent-1",
		From: "pending", To: "starting",
		Payload: `{"version":3,"source":"session",` +
			`"event":"session_start","confidence":1}`,
	}
	if err := projector.Apply(row); err != nil {
		t.Fatalf("apply non-screen v3 evidence: %v", err)
	}
	evidence := projector.sessions["agent-1"].lastTransition
	if evidence == nil ||
		evidence.Source != agent.EvidenceSession ||
		evidence.Screen != nil {
		t.Fatalf("projected evidence = %+v", evidence)
	}
}

func TestRecoveryProjectorAcceptsNotifyAuditPayloads(t *testing.T) {
	base := time.Date(2026, time.October, 3, 5, 0, 0, 0, time.UTC)
	projector := newRecoveryProjector()
	rows := []store.EventRow{
		{
			Seq:       1,
			Timestamp: base,
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Reason:    "created",
			Payload:   `{"version":1,"name":"agent","vendor":"codex"}`,
		},
		{
			Seq:       2,
			Timestamp: base.Add(time.Second),
			Type:      string(event.TypeAgentSignal),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Payload: `{"version":2,"source":"notify","kind":"turn_stopped",` +
				`"vendor":"codex","vendor_event":"agent-turn-complete",` +
				`"scope":"root","confidence":1,` +
				`"received_at":"2026-10-03T05:00:01Z",` +
				`"delivery_id":"550e8400-e29b-41d4-a716-446655440000",` +
				`"outcome":"candidate"}`,
		},
		{
			Seq:       3,
			Timestamp: base.Add(2 * time.Second),
			Type:      string(event.TypeStateChanged),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			From:      "pending",
			To:        "stopped",
			Reason:    "notify idle confirmed",
			Payload: `{"version":2,"source":"notify",` +
				`"event":"agent-turn-complete","confidence":1,` +
				`"delivery_id":"550e8400-e29b-41d4-a716-446655440000"}`,
		},
	}
	for _, row := range rows {
		if err := projector.Apply(row); err != nil {
			t.Fatalf("apply seq %d: %v", row.Seq, err)
		}
	}

	plan, err := projector.Finish(base.Add(time.Hour))
	if err != nil {
		t.Fatalf("finish projection: %v", err)
	}
	if plan.Report.UnknownSignalPayloadVersions != 0 ||
		plan.Report.UnknownStateEvidenceVersions != 0 {
		t.Fatalf("report = %+v", plan.Report)
	}
	snapshot := plan.Snapshots[0]
	if snapshot.LastTransition == nil ||
		snapshot.LastTransition.Source != agent.EvidenceNotify ||
		snapshot.LastTransition.Event != "agent-turn-complete" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestRecoveryProjectorStateChainCompatibility(t *testing.T) {
	base := time.Date(2026, time.October, 3, 5, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		rows        []store.EventRow
		wantPartial int
		wantErr     bool
	}{
		{
			name: "first state anchors",
			rows: []store.EventRow{
				{Seq: 1, Type: string(event.TypeStateChanged), SessionID: "s1", From: "working", To: "blocked"},
			},
		},
		{
			name: "matching state after gap",
			rows: []store.EventRow{
				{Seq: 1, Type: string(event.TypeStateChanged), SessionID: "s1", From: "pending", To: "starting"},
				{Seq: 3, Type: string(event.TypeStateChanged), SessionID: "s1", From: "starting", To: "working"},
			},
		},
		{
			name: "mismatch after gap reanchors",
			rows: []store.EventRow{
				{Seq: 1, Type: string(event.TypeStateChanged), SessionID: "s1", From: "pending", To: "starting"},
				{Seq: 3, Type: string(event.TypeStateChanged), SessionID: "s1", From: "working", To: "done"},
			},
			wantPartial: 1,
		},
		{
			name: "mismatch without gap fails",
			rows: []store.EventRow{
				{Seq: 1, Type: string(event.TypeStateChanged), SessionID: "s1", From: "pending", To: "starting"},
				{Seq: 2, Type: string(event.TypeStateChanged), SessionID: "s1", From: "working", To: "done"},
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projector := newRecoveryProjector()
			var applyErr error
			for i := range test.rows {
				test.rows[i].Timestamp = base.Add(time.Duration(i) * time.Second)
				applyErr = projector.Apply(test.rows[i])
				if applyErr != nil {
					break
				}
			}
			if test.wantErr {
				if applyErr == nil || !strings.Contains(applyErr.Error(), "state chain mismatch") {
					t.Fatalf("apply error = %v, want state chain mismatch", applyErr)
				}
				return
			}
			if applyErr != nil {
				t.Fatalf("apply: %v", applyErr)
			}
			plan, err := projector.Finish(base.Add(time.Hour))
			if err != nil {
				t.Fatalf("finish projection: %v", err)
			}
			if plan.Report.PartialHistory != test.wantPartial {
				t.Fatalf("partial history = %d, want %d", plan.Report.PartialHistory, test.wantPartial)
			}
		})
	}
}

func TestRecoveryProjectorRequiresAdjacentResumeEvent(t *testing.T) {
	base := time.Date(2026, time.October, 4, 14, 0, 0, 0, time.UTC)
	prefix := []store.EventRow{
		{
			Seq:       1,
			Timestamp: base,
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "s1",
			AgentID:   "s1",
			Reason:    "created",
			Payload:   `{"version":1,"name":"agent","vendor":"claude"}`,
		},
		{
			Seq:       2,
			Timestamp: base.Add(time.Second),
			Type:      string(event.TypeStateChanged),
			SessionID: "s1",
			AgentID:   "s1",
			From:      "pending",
			To:        "stopped",
		},
	}
	resumed := store.EventRow{
		Seq:       3,
		Timestamp: base.Add(2 * time.Second),
		Type:      string(event.TypeAgentResumed),
		SessionID: "s1",
		AgentID:   "s1",
		Reason:    "requested",
		Payload:   `{"version":1,"vendor_session_ref":"vendor-session-1"}`,
	}
	starting := store.EventRow{
		Seq:       4,
		Timestamp: base.Add(3 * time.Second),
		Type:      string(event.TypeStateChanged),
		SessionID: "s1",
		AgentID:   "s1",
		From:      "stopped",
		To:        "starting",
	}
	tests := []struct {
		name    string
		middle  []store.EventRow
		wantErr bool
	}{
		{name: "adjacent same agent", middle: []store.EventRow{resumed}},
		{name: "missing resume", wantErr: true},
		{
			name: "different agent",
			middle: []store.EventRow{{
				Seq:       3,
				Timestamp: base.Add(2 * time.Second),
				Type:      string(event.TypeAgentResumed),
				SessionID: "s2",
				AgentID:   "s2",
				Reason:    "requested",
				Payload:   resumed.Payload,
			}},
			wantErr: true,
		},
		{
			name: "intervening event",
			middle: []store.EventRow{
				resumed,
				{
					Seq:       4,
					Timestamp: base.Add(3 * time.Second),
					Type:      string(event.TypeOutput),
					SessionID: "s1",
					AgentID:   "s1",
				},
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projector := newRecoveryProjector()
			rows := append([]store.EventRow(nil), prefix...)
			rows = append(rows, test.middle...)
			final := starting
			final.Seq = uint64(len(rows) + 1)
			final.Timestamp = base.Add(time.Duration(len(rows)) * time.Second)
			rows = append(rows, final)
			var applyErr error
			for _, row := range rows {
				if applyErr = projector.Apply(row); applyErr != nil {
					break
				}
			}
			if test.wantErr {
				if applyErr == nil || !strings.Contains(applyErr.Error(), "agent.resumed") {
					t.Fatalf("apply error = %v, want agent.resumed rejection", applyErr)
				}
				return
			}
			if applyErr != nil {
				t.Fatalf("apply: %v", applyErr)
			}
		})
	}
}

func TestRecoveryProjectorRejectsMalformedResumePayload(t *testing.T) {
	projector := newRecoveryProjector()
	err := projector.Apply(store.EventRow{
		Seq:       1,
		Timestamp: time.Date(2026, time.October, 4, 14, 0, 0, 0, time.UTC),
		Type:      string(event.TypeAgentResumed),
		SessionID: "s1",
		AgentID:   "s1",
		Reason:    "requested",
		Payload:   `{"version":1,"vendor_session_ref":""}`,
	})
	if err == nil || !strings.Contains(err.Error(), "validate agent.resumed payload") {
		t.Fatalf("apply error = %v, want malformed resume payload", err)
	}
}

func TestRecoveryProjectorOrdersReconciliationByFirstEvent(t *testing.T) {
	base := time.Date(2026, time.October, 3, 5, 0, 0, 0, time.UTC)
	projector := newRecoveryProjector()
	for _, row := range []store.EventRow{
		{
			Seq:       1,
			Timestamp: base,
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "agent-b",
			Reason:    "created",
			Payload:   `{"version":1,"name":"b","vendor":"generic"}`,
		},
		{
			Seq:       2,
			Timestamp: base,
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "agent-a",
			Reason:    "created",
			Payload:   `{"version":1,"name":"a","vendor":"generic"}`,
		},
	} {
		if err := projector.Apply(row); err != nil {
			t.Fatalf("apply seq %d: %v", row.Seq, err)
		}
	}

	plan, err := projector.Finish(base.Add(time.Hour))
	if err != nil {
		t.Fatalf("finish projection: %v", err)
	}
	if len(plan.Reconciliation) != 4 {
		t.Fatalf("reconciliation count = %d, want 4", len(plan.Reconciliation))
	}
	got := []string{
		plan.Reconciliation[0].SessionID,
		plan.Reconciliation[1].SessionID,
		plan.Reconciliation[2].SessionID,
		plan.Reconciliation[3].SessionID,
	}
	want := []string{"agent-b", "agent-b", "agent-a", "agent-a"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reconciliation order = %v, want %v", got, want)
		}
	}
}

func TestRecoveryProjectorAcceptsValidatedResizeWithoutChangingState(t *testing.T) {
	projector := newRecoveryProjector()
	base := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	rows := []store.EventRow{
		{
			Seq:       1,
			Timestamp: base,
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "s1",
			AgentID:   "s1",
			Reason:    "created",
			Payload:   `{"version":1,"name":"agent","vendor":"generic"}`,
		},
		{
			Seq:       2,
			Timestamp: base.Add(time.Second),
			Type:      string(event.TypeAgentResized),
			SessionID: "s1",
			AgentID:   "s1",
			Payload:   `{"version":1,"rows":50,"columns":160,"output_offset":12}`,
		},
	}
	for _, row := range rows {
		if err := projector.Apply(row); err != nil {
			t.Fatalf("apply seq %d: %v", row.Seq, err)
		}
	}
	draft := projector.sessions["s1"]
	if draft == nil || draft.state != agent.StatePending || draft.updatedAt != base {
		t.Fatalf("resize changed recovered state: %+v", draft)
	}
	if projector.lastSeq != 2 || projector.report.ScannedEvents != 2 {
		t.Fatalf("projector position = (%d, %d)", projector.lastSeq, projector.report.ScannedEvents)
	}
}

func TestRecoveryProjectorWorkspaceRemovalDoesNotChangeStateTimestamp(
	t *testing.T,
) {
	projector := newRecoveryProjector()
	base := time.Date(2026, time.October, 4, 12, 30, 0, 0, time.UTC)
	stoppedAt := base.Add(time.Second)
	rows := []store.EventRow{
		{
			Seq:       1,
			Timestamp: base,
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "s1",
			AgentID:   "s1",
			Reason:    "created",
			Payload:   `{"version":1,"name":"agent","vendor":"generic"}`,
		},
		{
			Seq:       2,
			Timestamp: stoppedAt,
			Type:      string(event.TypeStateChanged),
			SessionID: "s1",
			AgentID:   "s1",
			From:      string(agent.StatePending),
			To:        string(agent.StateStopped),
		},
		{
			Seq:       3,
			Timestamp: base.Add(2 * time.Second),
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "s1",
			AgentID:   "s1",
			Reason:    workspaceRemovedReason,
			Payload:   `{"version":1}`,
		},
	}
	for _, row := range rows {
		if err := projector.Apply(row); err != nil {
			t.Fatalf("apply seq %d: %v", row.Seq, err)
		}
	}
	draft := projector.sessions["s1"]
	if draft == nil || !draft.updatedAt.Equal(stoppedAt) {
		t.Fatalf("workspace removal changed recovered state time: %+v", draft)
	}
	plan, err := projector.Finish(base.Add(time.Hour))
	if err != nil {
		t.Fatalf("finish projection: %v", err)
	}
	if len(plan.Snapshots) != 1 ||
		!plan.Snapshots[0].UpdatedAt.Equal(stoppedAt) {
		t.Fatalf("workspace removal snapshot = %+v", plan.Snapshots)
	}
}

func TestRecoveryProjectorAcceptsAttachmentAuditWithoutChangingState(t *testing.T) {
	projector := newRecoveryProjector()
	base := time.Date(2026, time.October, 4, 13, 0, 0, 0, time.UTC)
	rows := []store.EventRow{
		{
			Seq:       1,
			Timestamp: base,
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "s1",
			AgentID:   "s1",
			Reason:    "created",
			Payload:   `{"version":1,"name":"agent","vendor":"generic"}`,
		},
		{
			Seq:       2,
			Timestamp: base.Add(time.Second),
			Type:      string(event.TypeAgentAttachment),
			SessionID: "s1",
			AgentID:   "s1",
			Payload:   `{"version":1,"action":"attached","access":"read_write"}`,
		},
		{
			Seq:       3,
			Timestamp: base.Add(2 * time.Second),
			Type:      string(event.TypeAgentAttachment),
			SessionID: "s1",
			AgentID:   "s1",
			Payload:   `{"version":1,"action":"detached","access":"read_write"}`,
		},
	}
	for _, row := range rows {
		if err := projector.Apply(row); err != nil {
			t.Fatalf("apply seq %d: %v", row.Seq, err)
		}
	}
	draft := projector.sessions["s1"]
	if draft == nil || draft.state != agent.StatePending || draft.updatedAt != base {
		t.Fatalf("attachment audit changed recovered state: %+v", draft)
	}
	if projector.lastSeq != 3 || projector.report.ScannedEvents != 3 {
		t.Fatalf("projector position = (%d, %d)", projector.lastSeq, projector.report.ScannedEvents)
	}
}

func TestRecoveryProjectorRejectsCriticalCorruption(t *testing.T) {
	base := time.Date(2026, time.October, 3, 5, 0, 0, 0, time.UTC)
	created := func(seq uint64) store.EventRow {
		return store.EventRow{
			Seq:       seq,
			Timestamp: base,
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "s1",
			AgentID:   "s1",
			Reason:    "created",
			Payload:   `{"version":1,"name":"agent","vendor":"generic"}`,
		}
	}
	tests := []struct {
		name    string
		rows    []store.EventRow
		wantErr string
	}{
		{
			name:    "zero sequence",
			rows:    []store.EventRow{{Timestamp: base, Type: string(event.TypeOutput), SessionID: "s1"}},
			wantErr: "seq 0",
		},
		{
			name: "repeated sequence",
			rows: []store.EventRow{
				{Seq: 1, Timestamp: base, Type: string(event.TypeOutput), SessionID: "s1"},
				{Seq: 1, Timestamp: base, Type: string(event.TypeOutput), SessionID: "s1"},
			},
			wantErr: "not greater",
		},
		{
			name:    "unknown type",
			rows:    []store.EventRow{{Seq: 1, Timestamp: base, Type: "mystery"}},
			wantErr: "unknown event type",
		},
		{
			name:    "empty lifecycle session",
			rows:    []store.EventRow{{Seq: 1, Timestamp: base, Type: string(event.TypeSessionLifecycle), Reason: "created"}},
			wantErr: "empty session ID",
		},
		{
			name:    "empty state session",
			rows:    []store.EventRow{{Seq: 1, Timestamp: base, Type: string(event.TypeStateChanged), From: "pending", To: "starting"}},
			wantErr: "empty session ID",
		},
		{
			name:    "empty input session",
			rows:    []store.EventRow{{Seq: 1, Timestamp: base, Type: string(event.TypeAgentInput)}},
			wantErr: "empty session ID",
		},
		{
			name:    "empty signal session",
			rows:    []store.EventRow{{Seq: 1, Timestamp: base, Type: string(event.TypeAgentSignal)}},
			wantErr: "empty session ID",
		},
		{
			name:    "empty resize session",
			rows:    []store.EventRow{{Seq: 1, Timestamp: base, Type: string(event.TypeAgentResized)}},
			wantErr: "empty session ID",
		},
		{
			name:    "empty attachment session",
			rows:    []store.EventRow{{Seq: 1, Timestamp: base, Type: string(event.TypeAgentAttachment)}},
			wantErr: "empty session ID",
		},
		{
			name:    "mismatched agent",
			rows:    []store.EventRow{{Seq: 1, Timestamp: base, Type: string(event.TypeOutput), SessionID: "s1", AgentID: "a2"}},
			wantErr: "does not match",
		},
		{
			name:    "mismatched input agent",
			rows:    []store.EventRow{{Seq: 1, Timestamp: base, Type: string(event.TypeAgentInput), SessionID: "s1", AgentID: "a2"}},
			wantErr: "does not match",
		},
		{
			name:    "mismatched signal agent",
			rows:    []store.EventRow{{Seq: 1, Timestamp: base, Type: string(event.TypeAgentSignal), SessionID: "s1", AgentID: "a2"}},
			wantErr: "does not match",
		},
		{
			name:    "mismatched resize agent",
			rows:    []store.EventRow{{Seq: 1, Timestamp: base, Type: string(event.TypeAgentResized), SessionID: "s1", AgentID: "a2"}},
			wantErr: "does not match",
		},
		{
			name:    "mismatched attachment agent",
			rows:    []store.EventRow{{Seq: 1, Timestamp: base, Type: string(event.TypeAgentAttachment), SessionID: "s1", AgentID: "a2"}},
			wantErr: "does not match",
		},
		{
			name: "malformed resize payload",
			rows: []store.EventRow{{
				Seq:       1,
				Timestamp: base,
				Type:      string(event.TypeAgentResized),
				SessionID: "s1",
				AgentID:   "s1",
				Payload:   `{"version":1,"rows":0,"columns":120}`,
			}},
			wantErr: "validate resize payload",
		},
		{
			name: "malformed known signal payload",
			rows: []store.EventRow{{
				Seq:       1,
				Timestamp: base,
				Type:      string(event.TypeAgentSignal),
				SessionID: "s1",
				AgentID:   "s1",
				Payload:   `{"version":1}`,
			}},
			wantErr: "validate signal payload",
		},
		{
			name: "malformed attachment payload",
			rows: []store.EventRow{{
				Seq:       1,
				Timestamp: base,
				Type:      string(event.TypeAgentAttachment),
				SessionID: "s1",
				AgentID:   "s1",
				Payload:   `{"version":1,"action":"attached","access":"read_only","client":"private"}`,
			}},
			wantErr: "validate attachment payload",
		},
		{
			name:    "unknown lifecycle reason",
			rows:    []store.EventRow{{Seq: 1, Timestamp: base, Type: string(event.TypeSessionLifecycle), SessionID: "s1", Reason: "deleted"}},
			wantErr: "unknown lifecycle reason",
		},
		{
			name:    "malformed metadata",
			rows:    []store.EventRow{{Seq: 1, Timestamp: base, Type: string(event.TypeSessionLifecycle), SessionID: "s1", Reason: "created", Payload: "{"}},
			wantErr: "decode creation metadata",
		},
		{
			name: "duplicate metadata",
			rows: []store.EventRow{
				created(1),
				created(2),
			},
			wantErr: "duplicate creation metadata",
		},
		{
			name: "unsupported metadata",
			rows: []store.EventRow{
				{Seq: 1, Timestamp: base, Type: string(event.TypeSessionLifecycle), SessionID: "s1", Reason: "created", Payload: `{"version":3,"name":"agent","vendor":"generic"}`},
			},
			wantErr: "unsupported",
		},
		{
			name: "partial injection metadata",
			rows: []store.EventRow{{
				Seq:       1,
				Timestamp: base,
				Type:      string(event.TypeSessionLifecycle),
				SessionID: "s1",
				Reason:    "created",
				Payload: `{"version":2,"name":"agent","vendor":"claude",` +
					`"mode":"interactive","hook_policy":"auto",` +
					`"signal_injection":"auto"}`,
			}},
			wantErr: "signal injection",
		},
		{
			name: "relative working directory",
			rows: []store.EventRow{{
				Seq:       1,
				Timestamp: base,
				Type:      string(event.TypeSessionLifecycle),
				SessionID: "s1",
				Reason:    "created",
				Payload: `{"version":2,"name":"agent","vendor":"claude",` +
					`"mode":"interactive","hook_policy":"auto",` +
					`"working_dir":"relative/project"}`,
			}},
			wantErr: "working directory",
		},
		{
			name: "empty metadata name",
			rows: []store.EventRow{
				{Seq: 1, Timestamp: base, Type: string(event.TypeSessionLifecycle), SessionID: "s1", Reason: "created", Payload: `{"version":1,"name":"","vendor":"generic"}`},
			},
			wantErr: "name is empty",
		},
		{
			name: "empty metadata vendor",
			rows: []store.EventRow{
				{Seq: 1, Timestamp: base, Type: string(event.TypeSessionLifecycle), SessionID: "s1", Reason: "created", Payload: `{"version":1,"name":"agent","vendor":""}`},
			},
			wantErr: "vendor is empty",
		},
		{
			name: "empty metadata mode",
			rows: []store.EventRow{
				{Seq: 1, Timestamp: base, Type: string(event.TypeSessionLifecycle), SessionID: "s1", Reason: "created", Payload: `{"version":1,"name":"agent","vendor":"generic","mode":""}`},
			},
			wantErr: "mode",
		},
		{
			name: "invalid metadata mode",
			rows: []store.EventRow{
				{Seq: 1, Timestamp: base, Type: string(event.TypeSessionLifecycle), SessionID: "s1", Reason: "created", Payload: `{"version":1,"name":"agent","vendor":"generic","mode":"batch"}`},
			},
			wantErr: "mode",
		},
		{
			name: "invalid from state",
			rows: []store.EventRow{
				{Seq: 1, Timestamp: base, Type: string(event.TypeStateChanged), SessionID: "s1", From: "bad", To: "working"},
			},
			wantErr: "invalid from state",
		},
		{
			name: "invalid to state",
			rows: []store.EventRow{
				{Seq: 1, Timestamp: base, Type: string(event.TypeStateChanged), SessionID: "s1", From: "working", To: "bad"},
			},
			wantErr: "invalid to state",
		},
		{
			name: "invalid transition",
			rows: []store.EventRow{
				{Seq: 1, Timestamp: base, Type: string(event.TypeStateChanged), SessionID: "s1", From: "stopped", To: "working"},
			},
			wantErr: "invalid state transition",
		},
		{
			name: "malformed known state evidence",
			rows: []store.EventRow{{
				Seq:       1,
				Timestamp: base,
				Type:      string(event.TypeStateChanged),
				SessionID: "s1",
				AgentID:   "s1",
				From:      "pending",
				To:        "stopped",
				Payload:   `{"version":1,"source":"unknown","event":"stop","confidence":1}`,
			}},
			wantErr: "validate state evidence",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projector := newRecoveryProjector()
			var err error
			for _, row := range test.rows {
				err = projector.Apply(row)
				if err != nil {
					break
				}
			}
			if err == nil {
				t.Fatal("projection accepted corrupt history")
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %q, want %q context", err, test.wantErr)
			}
			last := test.rows[len(test.rows)-1]
			if !strings.Contains(err.Error(), "seq") {
				t.Fatalf("error = %q, want sequence context", err)
			}
			if last.SessionID != "" && !strings.Contains(err.Error(), last.SessionID) {
				t.Fatalf("error = %q, want session context", err)
			}
		})
	}
}

func TestRecoveryProjectorBuildsSnapshotAndReconciliation(t *testing.T) {
	createdAt := time.Date(2026, time.October, 3, 4, 5, 6, 7, time.UTC)
	recoveryTime := createdAt.Add(time.Hour)
	projector := newRecoveryProjector()

	for _, row := range []store.EventRow{
		{
			Seq:       1,
			Timestamp: createdAt,
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Reason:    "created",
			Payload:   `{"version":1,"name":"build-api","vendor":"generic"}`,
		},
		{
			Seq:       2,
			Timestamp: createdAt.Add(time.Second),
			Type:      string(event.TypeStateChanged),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			From:      "pending",
			To:        "starting",
		},
		{
			Seq:       3,
			Timestamp: createdAt.Add(2 * time.Second),
			Type:      string(event.TypeStateChanged),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			From:      "starting",
			To:        "working",
		},
	} {
		if err := projector.Apply(row); err != nil {
			t.Fatalf("apply seq %d: %v", row.Seq, err)
		}
	}

	plan, err := projector.Finish(recoveryTime)
	if err != nil {
		t.Fatalf("finish projection: %v", err)
	}
	if plan.Report != (RecoveryReport{
		ScannedEvents: 3,
		Sessions:      1,
		Interrupted:   1,
		LastSeq:       5,
	}) {
		t.Fatalf("recovery report = %+v", plan.Report)
	}
	if len(plan.Snapshots) != 1 {
		t.Fatalf("snapshot count = %d, want 1", len(plan.Snapshots))
	}
	snapshot := plan.Snapshots[0]
	if snapshot.ID != agent.ID("agent-1") ||
		snapshot.Name != "build-api" ||
		snapshot.Vendor != "generic" ||
		snapshot.RunMode != agent.RunModeOneshot ||
		snapshot.State != agent.StateStopped ||
		snapshot.LastError != restartInterruptionError ||
		!snapshot.CreatedAt.Equal(createdAt) ||
		!snapshot.UpdatedAt.Equal(recoveryTime) {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if len(plan.Reconciliation) != 2 {
		t.Fatalf("reconciliation count = %d, want 2", len(plan.Reconciliation))
	}
	if got := plan.Reconciliation[0]; got.Seq != 4 ||
		got.Type != string(event.TypeError) ||
		got.Payload != restartInterruptionError ||
		!got.Timestamp.Equal(recoveryTime) {
		t.Fatalf("reconciliation error = %+v", got)
	}
	if got := plan.Reconciliation[1]; got.Seq != 5 ||
		got.Type != string(event.TypeStateChanged) ||
		got.From != "working" ||
		got.To != "stopped" ||
		got.Reason != restartStopReason ||
		!got.Timestamp.Equal(recoveryTime) {
		t.Fatalf("reconciliation state = %+v", got)
	}
}

func TestRecoveryProjectorRestoresPersistedRunMode(t *testing.T) {
	base := time.Date(2026, time.October, 3, 4, 5, 6, 7, time.UTC)
	for _, mode := range []agent.RunMode{agent.RunModeInteractive, agent.RunModeOneshot} {
		t.Run(string(mode), func(t *testing.T) {
			projector := newRecoveryProjector()
			if err := projector.Apply(store.EventRow{
				Seq:       1,
				Timestamp: base,
				Type:      string(event.TypeSessionLifecycle),
				SessionID: "agent-1",
				AgentID:   "agent-1",
				Reason:    "created",
				Payload:   `{"version":1,"name":"agent","vendor":"generic","mode":"` + string(mode) + `"}`,
			}); err != nil {
				t.Fatalf("apply creation: %v", err)
			}

			plan, err := projector.Finish(base.Add(time.Hour))
			if err != nil {
				t.Fatalf("finish projection: %v", err)
			}
			if len(plan.Snapshots) != 1 || plan.Snapshots[0].RunMode != mode {
				t.Fatalf("snapshots = %+v, want mode %q", plan.Snapshots, mode)
			}
		})
	}
}

func TestRecoveryProjectorRestoresPersistedWorkingDirectory(t *testing.T) {
	base := time.Date(2026, time.October, 3, 4, 5, 6, 7, time.UTC)
	projector := newRecoveryProjector()
	if err := projector.Apply(store.EventRow{
		Seq:       1,
		Timestamp: base,
		Type:      string(event.TypeSessionLifecycle),
		SessionID: "agent-1",
		AgentID:   "agent-1",
		Reason:    "created",
		Payload: `{
			"version": 2,
			"name": "agent",
			"vendor": "generic",
			"dir": "/workspace/api",
			"mode": "interactive",
			"hook_policy": "off"
		}`,
	}); err != nil {
		t.Fatalf("apply creation: %v", err)
	}

	plan, err := projector.Finish(base.Add(time.Hour))
	if err != nil {
		t.Fatalf("finish projection: %v", err)
	}
	if len(plan.Snapshots) != 1 ||
		plan.Snapshots[0].WorkingDir != "/workspace/api" {
		t.Fatalf("snapshots = %+v, want persisted working directory", plan.Snapshots)
	}
}

func TestCreationMetadataRemainsReadableByOldVersionTwoDecoder(t *testing.T) {
	mode := agent.RunModeInteractive
	policy := agent.HooksAuto
	injection := agent.SignalInjectionAuto
	injectionStatus := agent.InjectionInjected
	injectionReason := agent.InjectionReasonSessionConfig
	raw, err := json.Marshal(createdPayload{
		Version:               2,
		Name:                  "agent",
		Vendor:                "claude",
		Dir:                   "/workspace/api",
		Mode:                  &mode,
		HookPolicy:            &policy,
		SignalInjection:       &injection,
		SignalInjectionStatus: &injectionStatus,
		SignalInjectionReason: &injectionReason,
		WorkingDir:            "/private/project",
	})
	if err != nil {
		t.Fatalf("marshal creation metadata: %v", err)
	}

	var oldMetadata struct {
		Version    int              `json:"version"`
		Name       string           `json:"name"`
		Vendor     string           `json:"vendor"`
		Mode       agent.RunMode    `json:"mode"`
		HookPolicy agent.HookPolicy `json:"hook_policy"`
	}
	if err := json.Unmarshal(raw, &oldMetadata); err != nil {
		t.Fatalf("old decoder rejected new metadata: %v", err)
	}
	if oldMetadata.Version != 2 ||
		oldMetadata.Name != "agent" ||
		oldMetadata.Vendor != "claude" ||
		oldMetadata.Mode != mode ||
		oldMetadata.HookPolicy != policy {
		t.Fatalf("old metadata = %+v", oldMetadata)
	}
}
