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
	if plan.Report.LegacyMetadata != 1 || plan.Report.Sessions != 1 || plan.Report.LastSeq != 4 {
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

func TestRecoveryProjectorIgnoresOutputAndErrorOnlySessions(t *testing.T) {
	base := time.Date(2026, time.October, 3, 5, 0, 0, 0, time.UTC)
	projector := newRecoveryProjector()
	for _, row := range []store.EventRow{
		{Seq: 1, Timestamp: base, Type: string(event.TypeOutput), SessionID: "orphan", Payload: "line"},
		{Seq: 2, Timestamp: base, Type: string(event.TypeError), SessionID: "orphan", Payload: "failure"},
		{Seq: 3, Timestamp: base, Type: string(event.TypeOutput), Payload: "daemon output"},
		{Seq: 4, Timestamp: base, Type: string(event.TypeError), Payload: "daemon error"},
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
	if plan.Report.ScannedEvents != 4 || plan.Report.Sessions != 0 || plan.Report.LastSeq != 4 {
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
		snapshot.LastTransition == nil ||
		snapshot.LastTransition.Source != agent.EvidenceProcess ||
		snapshot.LastTransition.Event != "process_start_failed" {
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
			Payload:   `{"version":2}`,
		},
		{
			Seq:       3,
			Timestamp: base.Add(2 * time.Second),
			Type:      string(event.TypeStateChanged),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			From:      "pending",
			To:        "stopped",
			Payload:   `{"version":2}`,
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

func TestCreationMetadataRemainsReadableByOldVersionOneDecoder(t *testing.T) {
	mode := agent.RunModeInteractive
	raw, err := json.Marshal(createdPayload{
		Version: 1,
		Name:    "agent",
		Vendor:  "generic",
		Mode:    &mode,
	})
	if err != nil {
		t.Fatalf("marshal creation metadata: %v", err)
	}

	var oldMetadata struct {
		Version int    `json:"version"`
		Name    string `json:"name"`
		Vendor  string `json:"vendor"`
	}
	if err := json.Unmarshal(raw, &oldMetadata); err != nil {
		t.Fatalf("old decoder rejected new metadata: %v", err)
	}
	if oldMetadata.Version != 1 || oldMetadata.Name != "agent" || oldMetadata.Vendor != "generic" {
		t.Fatalf("old metadata = %+v", oldMetadata)
	}
}
