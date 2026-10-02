package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
)

func TestBootstrapEmptyStore(t *testing.T) {
	st := newTestStore(t)

	result, err := Bootstrap(context.Background(), adapter.NewRegistry(), st)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if got := result.Manager.List(); len(got) != 0 {
		t.Fatalf("restored %d sessions, want 0", len(got))
	}
	if result.Recovery != (RecoveryReport{}) {
		t.Fatalf("recovery report = %+v, want zero", result.Recovery)
	}
	if got := result.Hub.NextSeq(); got != 1 {
		t.Fatalf("first hub seq = %d, want 1", got)
	}
}

func TestBootstrapRestoresLegacySessionIdempotently(t *testing.T) {
	st := newTestStore(t)
	createdAt := time.Date(2026, time.October, 3, 6, 0, 0, 0, time.UTC)
	if err := st.AppendEvent(store.EventRow{
		Seq:       1,
		Timestamp: createdAt,
		Type:      string(event.TypeStateChanged),
		SessionID: "legacy-agent",
		AgentID:   "legacy-agent",
		From:      "working",
		To:        "blocked",
	}); err != nil {
		t.Fatalf("append legacy state: %v", err)
	}

	first, err := Bootstrap(context.Background(), adapter.NewRegistry(), st)
	if err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}
	if first.Recovery.ScannedEvents != 1 ||
		first.Recovery.Sessions != 1 ||
		first.Recovery.Interrupted != 1 ||
		first.Recovery.LegacyMetadata != 1 ||
		first.Recovery.LastSeq != 3 {
		t.Fatalf("first recovery report = %+v", first.Recovery)
	}
	firstStatus, err := first.Manager.Status("legacy-agent")
	if err != nil {
		t.Fatalf("first restored status: %v", err)
	}
	if firstStatus.Name != "legacy-agent" ||
		firstStatus.Vendor != "unknown" ||
		firstStatus.State != agent.StateStopped ||
		firstStatus.PID != 0 ||
		firstStatus.LastError != restartInterruptionError ||
		!firstStatus.CreatedAt.Equal(createdAt) {
		t.Fatalf("first restored status = %+v", firstStatus)
	}
	firstRows, err := first.Manager.Replay("legacy-agent")
	if err != nil {
		t.Fatalf("first replay: %v", err)
	}
	if len(firstRows) != 3 ||
		firstRows[1].Type != string(event.TypeError) ||
		firstRows[2].From != "blocked" ||
		firstRows[2].To != "stopped" {
		t.Fatalf("first replay = %+v", firstRows)
	}

	second, err := Bootstrap(context.Background(), adapter.NewRegistry(), st)
	if err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	if second.Recovery.ScannedEvents != 3 ||
		second.Recovery.Sessions != 1 ||
		second.Recovery.Interrupted != 0 ||
		second.Recovery.LegacyMetadata != 1 ||
		second.Recovery.LastSeq != 3 {
		t.Fatalf("second recovery report = %+v", second.Recovery)
	}
	secondRows, err := second.Manager.Replay("legacy-agent")
	if err != nil {
		t.Fatalf("second replay: %v", err)
	}
	if len(secondRows) != 3 {
		t.Fatalf("second replay event count = %d, want 3", len(secondRows))
	}
	if got := second.Hub.NextSeq(); got != 4 {
		t.Fatalf("next hub seq = %d, want 4", got)
	}
}

func TestBootstrapRestoresVersionedDoneSession(t *testing.T) {
	st := newTestStore(t)
	base := time.Date(2026, time.October, 3, 6, 0, 0, 0, time.UTC)
	rows := []store.EventRow{
		{
			Seq:       1,
			Timestamp: base,
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Reason:    "created",
			Payload:   `{"version":1,"name":"build-api","vendor":"codex"}`,
		},
		{Seq: 2, Timestamp: base.Add(time.Second), Type: string(event.TypeStateChanged), SessionID: "agent-1", AgentID: "agent-1", From: "pending", To: "starting"},
		{Seq: 3, Timestamp: base.Add(2 * time.Second), Type: string(event.TypeStateChanged), SessionID: "agent-1", AgentID: "agent-1", From: "starting", To: "working"},
		{Seq: 4, Timestamp: base.Add(3 * time.Second), Type: string(event.TypeStateChanged), SessionID: "agent-1", AgentID: "agent-1", From: "working", To: "done"},
	}
	for _, row := range rows {
		if err := st.AppendEvent(row); err != nil {
			t.Fatalf("append seq %d: %v", row.Seq, err)
		}
	}

	result, err := Bootstrap(context.Background(), adapter.NewRegistry(), st)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if result.Recovery.Interrupted != 0 ||
		result.Recovery.LegacyMetadata != 0 ||
		result.Recovery.LastSeq != 5 {
		t.Fatalf("recovery report = %+v", result.Recovery)
	}
	status, err := result.Manager.Status("agent-1")
	if err != nil {
		t.Fatalf("restored status: %v", err)
	}
	if status.Name != "build-api" ||
		status.Vendor != "codex" ||
		status.State != agent.StateStopped ||
		status.LastError != "" {
		t.Fatalf("restored status = %+v", status)
	}
	replayed, err := result.Manager.Replay("agent-1")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(replayed) != 5 ||
		replayed[4].Type != string(event.TypeStateChanged) ||
		replayed[4].From != "done" ||
		replayed[4].To != "stopped" {
		t.Fatalf("replayed events = %+v", replayed)
	}
}

func TestBootstrapRejectsCorruptProjectionWithoutWriting(t *testing.T) {
	st := newTestStore(t)
	if err := st.AppendEvent(store.EventRow{
		Seq:       1,
		Timestamp: time.Now().UTC(),
		Type:      string(event.TypeSessionLifecycle),
		SessionID: "agent-1",
		AgentID:   "agent-1",
		Reason:    "created",
		Payload:   "{",
	}); err != nil {
		t.Fatalf("append corrupt event: %v", err)
	}

	result, err := Bootstrap(context.Background(), adapter.NewRegistry(), st)
	if err == nil {
		t.Fatal("bootstrap succeeded with corrupt metadata")
	}
	if result != nil {
		t.Fatalf("bootstrap result = %+v, want nil", result)
	}
	if !strings.Contains(err.Error(), "seq 1") || !strings.Contains(err.Error(), "agent-1") {
		t.Fatalf("error = %q, want sequence and session context", err)
	}
	lastSeq, lastErr := st.LastSeq()
	if lastErr != nil {
		t.Fatalf("last seq: %v", lastErr)
	}
	if lastSeq != 1 {
		t.Fatalf("last seq after failed bootstrap = %d, want 1", lastSeq)
	}
}

func TestBootstrapValidatesSnapshotsBeforeReconciliation(t *testing.T) {
	st := newTestStore(t)
	if err := st.AppendEvent(store.EventRow{
		Seq:       1,
		Timestamp: time.Time{},
		Type:      string(event.TypeSessionLifecycle),
		SessionID: "agent-1",
		AgentID:   "agent-1",
		Reason:    "created",
		Payload:   `{"version":1,"name":"agent","vendor":"generic"}`,
	}); err != nil {
		t.Fatalf("append zero timestamp event: %v", err)
	}

	result, err := Bootstrap(context.Background(), adapter.NewRegistry(), st)
	if err == nil {
		t.Fatal("bootstrap succeeded with an invalid snapshot timestamp")
	}
	if result != nil {
		t.Fatalf("bootstrap result = %+v, want nil", result)
	}
	if !strings.Contains(err.Error(), "agent-1") || !strings.Contains(err.Error(), "creation time") {
		t.Fatalf("error = %q, want agent and validation context", err)
	}
	lastSeq, lastErr := st.LastSeq()
	if lastErr != nil {
		t.Fatalf("last seq: %v", lastErr)
	}
	if lastSeq != 1 {
		t.Fatalf("last seq after failed validation = %d, want 1", lastSeq)
	}
}

func TestStopRestoredStoppedAgentIsIdempotent(t *testing.T) {
	st := newTestStore(t)
	base := time.Date(2026, time.October, 3, 7, 0, 0, 0, time.UTC)
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
		{Seq: 2, Timestamp: base.Add(time.Second), Type: string(event.TypeStateChanged), SessionID: "agent-1", AgentID: "agent-1", From: "pending", To: "stopped"},
	} {
		if err := st.AppendEvent(row); err != nil {
			t.Fatalf("append seq %d: %v", row.Seq, err)
		}
	}
	result, err := Bootstrap(context.Background(), adapter.NewRegistry(), st)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	if err := result.Manager.Stop("agent-1"); err != nil {
		t.Fatalf("stop restored agent: %v", err)
	}
	if err := result.Manager.Stop("missing"); err == nil {
		t.Fatal("stop unknown agent succeeded")
	}
	if err := result.Manager.Write("agent-1", []byte("input")); !errors.Is(err, ErrNotAttached) {
		t.Fatalf("write restored agent error = %v, want ErrNotAttached", err)
	}
	if err := result.Manager.Write("missing", []byte("input")); err == nil || errors.Is(err, ErrNotAttached) {
		t.Fatalf("write unknown agent error = %v, want unknown-agent error", err)
	}
}

func TestStopRejectsLiveAgentWithoutPTY(t *testing.T) {
	manager, _ := newTestManager(t)
	now := time.Now().UTC()
	restored, err := agent.Restore(agent.RestoreSnapshot{
		ID:        "agent-1",
		Name:      "agent",
		Vendor:    "generic",
		State:     agent.StateWorking,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("restore test agent: %v", err)
	}
	manager.agents[restored.ID()] = restored

	err = manager.Stop(restored.ID())
	if err == nil || !strings.Contains(err.Error(), "working without a PTY") {
		t.Fatalf("stop live unattached agent error = %v", err)
	}
}

func TestListSortsByCreationTimeThenAgentID(t *testing.T) {
	st := newTestStore(t)
	base := time.Date(2026, time.October, 3, 7, 0, 0, 0, time.UTC)
	rows := []store.EventRow{
		{
			Seq:       1,
			Timestamp: base.Add(time.Minute),
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "agent-b",
			AgentID:   "agent-b",
			Reason:    "created",
			Payload:   `{"version":1,"name":"b","vendor":"generic"}`,
		},
		{Seq: 2, Timestamp: base.Add(time.Minute), Type: string(event.TypeStateChanged), SessionID: "agent-b", AgentID: "agent-b", From: "pending", To: "stopped"},
		{
			Seq:       3,
			Timestamp: base,
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "agent-z",
			AgentID:   "agent-z",
			Reason:    "created",
			Payload:   `{"version":1,"name":"z","vendor":"generic"}`,
		},
		{Seq: 4, Timestamp: base, Type: string(event.TypeStateChanged), SessionID: "agent-z", AgentID: "agent-z", From: "pending", To: "stopped"},
		{
			Seq:       5,
			Timestamp: base,
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "agent-a",
			AgentID:   "agent-a",
			Reason:    "created",
			Payload:   `{"version":1,"name":"a","vendor":"generic"}`,
		},
		{Seq: 6, Timestamp: base, Type: string(event.TypeStateChanged), SessionID: "agent-a", AgentID: "agent-a", From: "pending", To: "stopped"},
	}
	for _, row := range rows {
		if err := st.AppendEvent(row); err != nil {
			t.Fatalf("append seq %d: %v", row.Seq, err)
		}
	}
	result, err := Bootstrap(context.Background(), adapter.NewRegistry(), st)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	statuses := result.Manager.List()
	if len(statuses) != 3 {
		t.Fatalf("status count = %d, want 3", len(statuses))
	}
	got := []string{statuses[0].AgentID, statuses[1].AgentID, statuses[2].AgentID}
	want := []string{"agent-a", "agent-z", "agent-b"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("list order = %v, want %v", got, want)
		}
	}
}

func TestStartDoesNotLaunchProcessWhenCreationCannotPersist(t *testing.T) {
	manager, st := newTestManager(t)
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	marker := filepath.Join(t.TempDir(), "process-started")
	status, err := manager.Start(context.Background(), StartRequest{
		Name:    "must-not-start",
		Command: "/usr/bin/touch",
		Args:    []string{marker},
	})
	if err == nil {
		t.Fatal("start succeeded with a closed store")
	}
	if !strings.Contains(err.Error(), "persist creation") {
		t.Fatalf("error = %q, want creation persistence context", err)
	}
	if status != nil {
		t.Fatalf("start status = %+v, want nil", status)
	}
	if got := manager.List(); len(got) != 0 {
		t.Fatalf("manager retained %d agents, want 0", len(got))
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("process marker error = %v, want not exist", statErr)
	}
}

func TestStartPreservesFailedPTYStartupHistory(t *testing.T) {
	manager, st := newTestManager(t)

	status, startErr := manager.Start(context.Background(), StartRequest{
		Name:    "broken-agent",
		Command: filepath.Join(t.TempDir(), "missing-agent"),
	})
	if startErr == nil {
		t.Fatal("start succeeded with a missing executable")
	}
	if status != nil {
		t.Fatalf("start status = %+v, want nil", status)
	}

	var rows []store.EventRow
	_, err := st.ScanEvents(context.Background(), func(row store.EventRow) error {
		rows = append(rows, row)
		return nil
	})
	if err != nil {
		t.Fatalf("scan events: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("event count = %d, want 4: %+v", len(rows), rows)
	}
	if rows[0].Seq != 1 ||
		rows[0].Type != string(event.TypeSessionLifecycle) ||
		rows[0].Reason != "created" ||
		rows[0].SessionID != rows[0].AgentID ||
		rows[0].Payload != `{"version":1,"name":"broken-agent","vendor":"generic"}` {
		t.Fatalf("creation event = %+v", rows[0])
	}
	if rows[1].Seq != 2 ||
		rows[1].Type != string(event.TypeStateChanged) ||
		rows[1].From != "pending" ||
		rows[1].To != "starting" {
		t.Fatalf("starting event = %+v", rows[1])
	}
	if rows[2].Seq != 3 || rows[2].Type != string(event.TypeError) || rows[2].Payload == "" {
		t.Fatalf("startup error event = %+v", rows[2])
	}
	if rows[3].Seq != 4 ||
		rows[3].Type != string(event.TypeStateChanged) ||
		rows[3].From != "starting" ||
		rows[3].To != "stopped" {
		t.Fatalf("stopped event = %+v", rows[3])
	}

	failedStatus, err := manager.Status(agent.ID(rows[0].SessionID))
	if err != nil {
		t.Fatalf("status of failed session: %v", err)
	}
	if failedStatus.State != agent.StateStopped {
		t.Fatalf("failed session state = %s, want stopped", failedStatus.State)
	}
	if failedStatus.LastError == "" || failedStatus.LastError != rows[2].Payload {
		t.Fatalf("failed session error = %q, event payload = %q", failedStatus.LastError, rows[2].Payload)
	}
}

func TestStartRejectsInvalidGenericRequestWithoutHistory(t *testing.T) {
	manager, st := newTestManager(t)

	status, err := manager.Start(context.Background(), StartRequest{})
	if err == nil {
		t.Fatal("start succeeded without a generic command")
	}
	if status != nil {
		t.Fatalf("start status = %+v, want nil", status)
	}
	if got := manager.List(); len(got) != 0 {
		t.Fatalf("manager retained %d agents, want 0", len(got))
	}

	lastSeq, err := st.ScanEvents(context.Background(), func(store.EventRow) error { return nil })
	if err != nil {
		t.Fatalf("scan events: %v", err)
	}
	if lastSeq != 0 {
		t.Fatalf("last seq = %d, want 0", lastSeq)
	}
}

func newTestManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()

	st := newTestStore(t)
	return NewManager(adapter.NewRegistry(), event.NewHub(0), st), st
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "drove.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return st
}
