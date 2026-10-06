package session

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/pty"
	"github.com/Duang777/drove/internal/recording"
	"github.com/Duang777/drove/internal/store"
	"github.com/Duang777/drove/internal/term"
)

func TestReplayHydratesRetainedOutputAndLeavesExpiredMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drove.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	base := time.Date(2026, time.October, 4, 3, 0, 0, 0, time.UTC)
	var rows []store.EventRow
	for i, item := range []struct {
		offset uint64
		data   []byte
	}{
		{offset: 0, data: []byte("retained")},
		{offset: 8, data: []byte("expired")},
	} {
		draft, err := event.NewOutputChunkDraft("agent-1", "agent-1", item.offset, item.data)
		if err != nil {
			t.Fatalf("new output chunk %d: %v", i, err)
		}
		committed, err := event.Commit(uint64(i+1), base.Add(time.Duration(i)*time.Second), draft)
		if err != nil {
			t.Fatalf("commit output chunk %d: %v", i, err)
		}
		rows = append(rows, eventRow(committed))
	}
	if _, err := st.AppendEvents(context.Background(), 0, rows); err != nil {
		t.Fatalf("append output chunks: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close seeded store: %v", err)
	}

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM output_chunks WHERE event_seq = 2`); err != nil {
		t.Fatalf("expire output attachment: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}

	st, err = store.Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	manager := NewManager(adapter.NewRegistry(), event.NewHub(2), st, 2)
	defer func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()

	replayed, err := manager.Replay("agent-1")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(replayed) != 2 {
		t.Fatalf("replayed row count = %d, want 2", len(replayed))
	}
	retained, err := event.DecodeOutputChunkPayload(replayed[0].Payload)
	if err != nil {
		t.Fatalf("decode retained payload: %v", err)
	}
	retainedData, err := retained.DecodeData()
	if err != nil {
		t.Fatalf("decode retained data: %v", err)
	}
	if string(retainedData) != "retained" {
		t.Fatalf("retained data = %q, want retained", retainedData)
	}
	expired, err := event.DecodeOutputChunkPayload(replayed[1].Payload)
	if err != nil {
		t.Fatalf("decode expired metadata: %v", err)
	}
	if expired.DataB64 != "" || expired.Offset != 8 || expired.Len != len("expired") {
		t.Fatalf("expired payload = %+v", expired)
	}
	for _, row := range replayed {
		if row.OutputAttachment != nil {
			t.Fatalf("manager replay exposed attachment at seq %d", row.Seq)
		}
	}
}

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
	if got := result.Hub.LastSeq(); got != 0 {
		t.Fatalf("hub last seq = %d, want 0", got)
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
		firstStatus.Mode != agent.RunModeOneshot ||
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
	if got := second.Hub.LastSeq(); got != 3 {
		t.Fatalf("hub last seq = %d, want 3", got)
	}
}

func TestBootstrapAcceptsPreUpgradeUnicodeVendorSessionID(t *testing.T) {
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
			Payload: `{"version":2,"name":"legacy","vendor":"claude",` +
				`"mode":"interactive","hook_policy":"off"}`,
		},
		{
			Seq:       2,
			Timestamp: base.Add(time.Second),
			Type:      string(event.TypeAgentSignal),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Reason:    "observed",
			Payload: `{"version":1,"source":"hook","kind":"session_started","vendor":"claude",` +
				`"vendor_event":"SessionStart","scope":"root","vendor_session_id":"旧会话 ",` +
				`"confidence":1,"received_at":"2026-10-03T06:00:01Z",` +
				`"delivery_id":"550e8400-e29b-41d4-a716-446655440000","outcome":"observed"}`,
		},
		{
			Seq:       3,
			Timestamp: base.Add(2 * time.Second),
			Type:      string(event.TypeStateChanged),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			From:      "pending",
			To:        "stopped",
		},
	}
	if _, err := st.AppendEvents(context.Background(), 0, rows); err != nil {
		t.Fatalf("seed legacy session: %v", err)
	}

	result, err := Bootstrap(context.Background(), adapter.NewRegistry(), st)
	if err != nil {
		t.Fatalf("bootstrap legacy session: %v", err)
	}
	t.Cleanup(func() {
		if err := result.Manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})
	managed, ok := result.Manager.managed("agent-1")
	if !ok {
		t.Fatal("restored agent is missing")
	}
	if got := managed.vendorSessionReference(); got != "旧会话 " {
		t.Fatalf("restored vendor session reference = %q", got)
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
		status.Mode != agent.RunModeOneshot ||
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
	if err := result.Manager.Stop("missing"); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("stop unknown agent error = %v, want ErrUnknownAgent", err)
	}
	if _, err := result.Manager.SendInput("agent-1", []byte("input")); !errors.Is(err, ErrNotAttached) {
		t.Fatalf("send input to restored agent error = %v, want ErrNotAttached", err)
	}
	if _, err := result.Manager.SendInput("missing", []byte("input")); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("send input to unknown agent error = %v, want ErrUnknownAgent", err)
	}
}

func TestStopRejectsLiveAgentWithoutPTY(t *testing.T) {
	manager, _ := newTestManager(t)
	now := time.Now().UTC()
	restored, err := agent.Restore(agent.RestoreSnapshot{
		ID:        "agent-1",
		Name:      "agent",
		Vendor:    "generic",
		RunMode:   agent.RunModeOneshot,
		State:     agent.StateWorking,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("restore test agent: %v", err)
	}
	manager.agents[restored.ID()] = newManagedAgent(restored)

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

func TestStartCancellationAfterCreationKeepsProjectionConsistent(t *testing.T) {
	manager, st := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	manager.committer.store = &cancelAfterAppendStore{
		commitStore: manager.committer.store,
		cancel:      cancel,
	}

	status, err := manager.Start(ctx, StartRequest{
		Name:    "canceled-agent",
		Command: "/bin/cat",
	})
	if err != nil {
		t.Fatalf("start after durable creation: %v", err)
	}
	if status == nil || status.State != agent.StateWorking {
		t.Fatalf("start status = %+v, want working", status)
	}

	statuses := manager.List()
	if len(statuses) != 1 ||
		statuses[0].AgentID != status.AgentID ||
		statuses[0].State != agent.StateWorking {
		t.Fatalf("statuses = %+v, want the durable working session", statuses)
	}
	rows, replayErr := st.Replay(statuses[0].AgentID)
	if replayErr != nil {
		t.Fatalf("replay session: %v", replayErr)
	}
	if len(rows) != 4 ||
		rows[0].Type != string(event.TypeSessionLifecycle) ||
		rows[1].From != string(agent.StatePending) ||
		rows[1].To != string(agent.StateStarting) ||
		rows[2].Type != string(event.TypeAgentSignal) ||
		rows[3].From != string(agent.StateStarting) ||
		rows[3].To != string(agent.StateWorking) {
		t.Fatalf("session history = %+v", rows)
	}
}

func TestTransitionPersistenceFailureLeavesAgentAndHubUnchanged(t *testing.T) {
	manager, st := newTestManager(t)
	id := agent.ID("agent-1")
	a := agent.New(
		id,
		agent.WithName("agent"),
		agent.WithVendor("generic"),
		agent.WithRunMode(agent.RunModeInteractive),
	)
	manager.mu.Lock()
	manager.agents[id] = newManagedAgent(a)
	manager.mu.Unlock()
	subscription := manager.hub.Subscribe(1)
	defer manager.hub.Unsubscribe(subscription)

	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	_, err := manager.committer.CommitAgent(
		context.Background(),
		a,
		agent.MoveTo(agent.StateStarting, "test", agent.Evidence{
			Source: agent.EvidenceSession, Event: "session_start", Confidence: 1,
		}),
		nil,
	)
	if err == nil {
		t.Fatal("transition succeeded with closed store")
	}
	if a.State() != agent.StatePending {
		t.Fatalf("agent state = %s, want unchanged pending", a.State())
	}
	if manager.hub.LastSeq() != 0 {
		t.Fatalf("hub last sequence = %d, want 0", manager.hub.LastSeq())
	}
	select {
	case published := <-subscription.C():
		t.Fatalf("published event after failed append: %+v", published)
	default:
	}
	select {
	case fatalErr := <-manager.Fatal():
		if fatalErr == nil {
			t.Fatal("manager reported nil fatal error")
		}
	case <-time.After(time.Second):
		t.Fatal("manager did not report fatal commit error")
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
	if len(rows) != 5 {
		t.Fatalf("event count = %d, want 5: %+v", len(rows), rows)
	}
	if rows[0].Seq != 1 ||
		rows[0].Type != string(event.TypeSessionLifecycle) ||
		rows[0].Reason != "created" ||
		rows[0].SessionID != rows[0].AgentID {
		t.Fatalf("creation event = %+v", rows[0])
	}
	var creation createdPayload
	if err := json.Unmarshal([]byte(rows[0].Payload), &creation); err != nil {
		t.Fatalf("decode creation event: %v", err)
	}
	if creation.Version != 2 ||
		creation.Name != "broken-agent" ||
		creation.Vendor != "generic" ||
		creation.Mode == nil ||
		*creation.Mode != agent.RunModeInteractive ||
		creation.HookPolicy == nil ||
		*creation.HookPolicy != agent.HooksOff ||
		creation.WorkingDir == "" ||
		!filepath.IsAbs(creation.WorkingDir) {
		t.Fatalf("creation metadata = %+v", creation)
	}
	if rows[1].Seq != 2 ||
		rows[1].Type != string(event.TypeStateChanged) ||
		rows[1].From != "pending" ||
		rows[1].To != "starting" {
		t.Fatalf("starting event = %+v", rows[1])
	}
	if rows[2].Seq != 3 || rows[2].Type != string(event.TypeAgentSignal) {
		t.Fatalf("startup signal event = %+v", rows[2])
	}
	if rows[3].Seq != 4 || rows[3].Type != string(event.TypeError) || rows[3].Payload == "" {
		t.Fatalf("startup error event = %+v", rows[3])
	}
	if rows[4].Seq != 5 ||
		rows[4].Type != string(event.TypeStateChanged) ||
		rows[4].From != "starting" ||
		rows[4].To != "stopped" {
		t.Fatalf("stopped event = %+v", rows[4])
	}

	failedStatus, err := manager.Status(agent.ID(rows[0].SessionID))
	if err != nil {
		t.Fatalf("status of failed session: %v", err)
	}
	if failedStatus.State != agent.StateStopped {
		t.Fatalf("failed session state = %s, want stopped", failedStatus.State)
	}
	if failedStatus.LastError == "" || failedStatus.LastError != rows[3].Payload {
		t.Fatalf("failed session error = %q, event payload = %q", failedStatus.LastError, rows[3].Payload)
	}
}

func TestStatusDerivesResumableFromCommittedStateAndCapability(t *testing.T) {
	manager, _ := newTestManager(t)
	claude := addStoppedAgent(t, manager, "claude-agent", "claude", "vendor-ref")
	generic := addStoppedAgent(t, manager, "generic-agent", "generic", "vendor-ref")
	missingRef := addStoppedAgent(t, manager, "missing-ref", "codex", "")

	for _, test := range []struct {
		id   agent.ID
		want bool
	}{
		{id: claude.agent.ID(), want: true},
		{id: generic.agent.ID(), want: false},
		{id: missingRef.agent.ID(), want: false},
	} {
		status, err := manager.Status(test.id)
		if err != nil {
			t.Fatalf("status %q: %v", test.id, err)
		}
		if status.Resumable != test.want {
			t.Fatalf("status %q resumable = %t, want %t", test.id, status.Resumable, test.want)
		}
	}

	manager.mu.Lock()
	manager.resuming[claude.agent.ID()] = struct{}{}
	manager.mu.Unlock()
	status, err := manager.Status(claude.agent.ID())
	if err != nil {
		t.Fatalf("status reserved agent: %v", err)
	}
	if status.Resumable {
		t.Fatal("reserved agent is resumable")
	}
}

func TestResumeUsesNativeCommandAndKeepsAgentID(t *testing.T) {
	const terminationGrace = 3 * time.Second
	manager, st := newTestManager(t, WithTerminationGrace(terminationGrace))
	managed := addStoppedAgent(t, manager, "agent-1", "claude", "vendor-ref")
	var started pty.Config
	manager.startPTY = func(config pty.Config) (launchedSession, error) {
		started = config
		return &fakeProcessSession{}, nil
	}

	status, err := manager.Resume(context.Background(), managed.agent.ID())
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if status.AgentID != string(managed.agent.ID()) ||
		status.State != agent.StateWorking ||
		status.Resumable {
		t.Fatalf("resumed status = %+v", status)
	}
	if started.Command != "claude" ||
		len(started.Args) != 2 ||
		started.Args[0] != "--resume" ||
		started.Args[1] != "vendor-ref" {
		t.Fatalf("resume command = %q %q", started.Command, started.Args)
	}
	if started.TerminationGrace != terminationGrace {
		t.Fatalf(
			"termination grace = %v, want %v",
			started.TerminationGrace,
			terminationGrace,
		)
	}

	persisted, err := st.Replay(string(managed.agent.ID()))
	if err != nil {
		t.Fatalf("replay persisted resume: %v", err)
	}
	if len(persisted) == 0 || !strings.Contains(persisted[0].Payload, "vendor-ref") {
		t.Fatalf("persisted resume payload = %+v, want internal reference", persisted)
	}
	rows, err := manager.Replay(string(managed.agent.ID()))
	if err != nil {
		t.Fatalf("manager replay resume: %v", err)
	}
	if len(rows) != 4 ||
		rows[0].Type != string(event.TypeAgentResumed) ||
		rows[0].Payload != `{"version":1}` ||
		strings.Contains(rows[0].Payload, "vendor-ref") ||
		rows[1].From != string(agent.StateStopped) ||
		rows[1].To != string(agent.StateStarting) ||
		rows[2].Type != string(event.TypeAgentSignal) ||
		rows[3].From != string(agent.StateStarting) ||
		rows[3].To != string(agent.StateWorking) {
		t.Fatalf("resume history = %+v", rows)
	}
}

func TestResumeContinuesDurableOutputOffset(t *testing.T) {
	manager, st := newTestManager(t)
	managed := addStoppedAgent(t, manager, "agent-1", "claude", "vendor-ref")
	initial, err := event.NewOutputChunkDraft(
		"agent-1",
		"agent-1",
		0,
		[]byte("old"),
	)
	if err != nil {
		t.Fatalf("create initial output: %v", err)
	}
	if _, err := manager.committer.CommitEvents(
		context.Background(),
		[]event.Draft{initial},
	); err != nil {
		t.Fatalf("commit initial output: %v", err)
	}

	var started pty.Config
	manager.startPTY = func(config pty.Config) (launchedSession, error) {
		started = config
		return &fakeProcessSession{}, nil
	}
	if _, err := manager.Resume(context.Background(), managed.agent.ID()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	started.OnOutput([]byte("new"), 0)
	started.OnOutputEnd(3)

	rows, err := st.Replay("agent-1")
	if err != nil {
		t.Fatalf("replay stored output: %v", err)
	}
	var offsets []uint64
	for _, row := range rows {
		if row.Type != string(event.TypeOutputChunk) {
			continue
		}
		payload, err := event.DecodeOutputChunkPayload(row.Payload)
		if err != nil {
			t.Fatalf("decode output at seq %d: %v", row.Seq, err)
		}
		offsets = append(offsets, payload.Offset)
	}
	if len(offsets) != 2 || offsets[0] != 0 || offsets[1] != 3 {
		t.Fatalf("output offsets = %v, want [0 3]", offsets)
	}

	timeline, err := manager.Timeline(context.Background(), "agent-1")
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if timeline.Captured.NextOffset != 6 ||
		timeline.Output.Range != (recording.OutputRange{End: 6}) {
		t.Fatalf("timeline output = %+v", timeline.Output)
	}

	tail, err := manager.TailRaw(context.Background(), "agent-1", nil)
	if err != nil {
		t.Fatalf("tail raw: %v", err)
	}
	defer tail.Close()
	var tailed []byte
tailLoop:
	for {
		item, err := tail.Next()
		if err != nil {
			t.Fatalf("tail next: %v", err)
		}
		switch typed := item.(type) {
		case recording.RawOutput:
			tailed = append(tailed, typed.Data...)
		case recording.CaughtUp:
			if typed.Cursor.NextOffset != 6 {
				t.Fatalf("caught-up cursor = %+v", typed.Cursor)
			}
			break tailLoop
		}
	}
	if string(tailed) != "oldnew" {
		t.Fatalf("tailed output = %q, want oldnew", tailed)
	}

	offset := recording.OutputOffset(6)
	selector, err := recording.NewSelector(recording.SelectorInput{Offset: &offset})
	if err != nil {
		t.Fatalf("frame selector: %v", err)
	}
	frame, err := manager.Frame(context.Background(), "agent-1", selector)
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if frame.Cursor.NextOffset != 6 ||
		len(frame.Lines) == 0 ||
		!strings.Contains(strings.Join(frame.Lines, "\n"), "oldnew") {
		t.Fatalf("frame = %+v", frame)
	}
}

func TestResumeWaitsForPreviousOutputToDrain(t *testing.T) {
	manager, st := newTestManager(t)
	var started []pty.Config
	manager.startPTY = func(config pty.Config) (launchedSession, error) {
		started = append(started, config)
		return &fakeProcessSession{}, nil
	}
	status, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "claude",
		Command: "fake-claude",
		Hooks:   agent.HooksOff,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := agent.ID(status.AgentID)
	managed, ok := manager.managed(id)
	if !ok {
		t.Fatal("started agent is missing")
	}
	managed.setVendorSessionReference("vendor-ref")
	previous := started[0]
	previous.OnOutput([]byte("old"), 0)

	exited := make(chan struct{})
	go func() {
		previous.OnExit(pty.ExitInfo{PID: 1, Code: 0})
		close(exited)
	}()
	waitForState(t, manager, id, agent.StateStopped)
	draining, err := manager.Status(id)
	if err != nil {
		t.Fatalf("status while draining: %v", err)
	}
	if draining.Resumable {
		t.Fatalf("draining session is resumable: %+v", draining)
	}
	if _, err := manager.Resume(context.Background(), id); !errors.Is(err, ErrResumeConflict) {
		t.Fatalf("resume while draining error = %v, want ErrResumeConflict", err)
	}

	previous.OnOutputEnd(3)
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("exit callback did not finish after output drained")
	}
	drained := waitForDetachedState(t, manager, id, agent.StateStopped)
	if !drained.Resumable {
		t.Fatalf("drained session is not resumable: %+v", drained)
	}

	if _, err := manager.Resume(context.Background(), id); err != nil {
		t.Fatalf("resume after drain: %v", err)
	}
	resumed := started[1]
	resumed.OnOutput([]byte("new"), 0)
	resumed.OnOutputEnd(3)

	rows, err := st.Replay(status.AgentID)
	if err != nil {
		t.Fatalf("replay output: %v", err)
	}
	var offsets []uint64
	for _, row := range rows {
		if row.Type != string(event.TypeOutputChunk) {
			continue
		}
		payload, err := event.DecodeOutputChunkPayload(row.Payload)
		if err != nil {
			t.Fatalf("decode output at seq %d: %v", row.Seq, err)
		}
		offsets = append(offsets, payload.Offset)
	}
	if len(offsets) != 2 || offsets[0] != 0 || offsets[1] != 3 {
		t.Fatalf("output offsets = %v, want [0 3]", offsets)
	}
}

func TestBootstrapResumeUsesPersistedWorkingDirectory(t *testing.T) {
	st := newTestStore(t)
	base := time.Date(2026, time.October, 4, 15, 0, 0, 0, time.UTC)
	workingDir := t.TempDir()
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
				`"working_dir":` + strconv.Quote(workingDir) + `}`,
		},
		{
			Seq:       2,
			Timestamp: base.Add(time.Second),
			Type:      string(event.TypeAgentSignal),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Reason:    "observed",
			Payload: `{"version":1,"source":"hook","kind":"session_started","vendor":"claude",` +
				`"vendor_event":"SessionStart","scope":"root","vendor_session_ref":"vendor-ref",` +
				`"confidence":1,"received_at":"2026-10-04T15:00:01Z",` +
				`"delivery_id":"550e8400-e29b-41d4-a716-446655440000","outcome":"observed"}`,
		},
		{
			Seq:       3,
			Timestamp: base.Add(2 * time.Second),
			Type:      string(event.TypeStateChanged),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			From:      "pending",
			To:        "stopped",
			Reason:    "test stopped",
			Payload: `{"version":1,"source":"session","event":"session_stop",` +
				`"confidence":1}`,
		},
	}
	if _, err := st.AppendEvents(context.Background(), 0, rows); err != nil {
		t.Fatalf("seed events: %v", err)
	}

	result, err := Bootstrap(context.Background(), adapter.NewRegistry(), st)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := result.Manager.ConfigureSignalOrigin(&url.URL{
		Scheme: "http",
		Host:   "127.0.0.1:7373",
	}); err != nil {
		t.Fatalf("configure signal origin: %v", err)
	}
	t.Cleanup(func() {
		if err := result.Manager.Close(); err != nil {
			t.Errorf("close restored manager: %v", err)
		}
	})
	var started pty.Config
	result.Manager.startPTY = func(config pty.Config) (launchedSession, error) {
		started = config
		return &fakeProcessSession{}, nil
	}

	if _, err := result.Manager.Resume(context.Background(), "agent-1"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if started.Dir != workingDir {
		t.Fatalf("resumed directory = %q, want %q", started.Dir, workingDir)
	}
}

func TestResumeFailureReturnsAgentToResumableStoppedState(t *testing.T) {
	manager, st := newTestManager(t)
	managed := addStoppedAgent(t, manager, "agent-1", "codex", "thread-ref")
	manager.startPTY = func(pty.Config) (launchedSession, error) {
		return nil, errors.New("exec unavailable")
	}

	status, err := manager.Resume(context.Background(), managed.agent.ID())
	if err == nil || !strings.Contains(err.Error(), "start pty") {
		t.Fatalf("resume error = %v, want PTY startup error", err)
	}
	if status != nil {
		t.Fatalf("resume status = %+v, want nil", status)
	}
	current, err := manager.Status(managed.agent.ID())
	if err != nil {
		t.Fatalf("status after failure: %v", err)
	}
	if current.State != agent.StateStopped || !current.Resumable || current.PID != 0 {
		t.Fatalf("status after failed resume = %+v", current)
	}

	rows, err := st.Replay(string(managed.agent.ID()))
	if err != nil {
		t.Fatalf("replay failed resume: %v", err)
	}
	if len(rows) != 5 ||
		rows[0].Type != string(event.TypeAgentResumed) ||
		rows[1].To != string(agent.StateStarting) ||
		rows[2].Type != string(event.TypeAgentSignal) ||
		rows[3].Type != string(event.TypeError) ||
		rows[4].To != string(agent.StateStopped) {
		t.Fatalf("failed resume history = %+v", rows)
	}
}

func TestResumeCommitFailureClosesPreparedOutputProcessor(t *testing.T) {
	manager, _ := newTestManager(t)
	managed := addStoppedAgent(t, manager, "agent-1", "claude", "vendor-ref")
	manager.committer.Close()
	before := outputProcessorGoroutines()

	if _, err := manager.Resume(context.Background(), managed.agent.ID()); err == nil {
		t.Fatal("resume succeeded with a closed committer")
	}
	deadline := time.Now().Add(time.Second)
	for outputProcessorGoroutines() != before {
		if time.Now().After(deadline) {
			t.Fatalf(
				"output processor goroutines = %d, want %d",
				outputProcessorGoroutines(),
				before,
			)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestResumeRejectsUnknownUnsupportedAndReservedAgents(t *testing.T) {
	manager, _ := newTestManager(t)
	if _, err := manager.Resume(context.Background(), "missing"); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("unknown resume error = %v, want ErrUnknownAgent", err)
	}
	generic := addStoppedAgent(t, manager, "generic-agent", "generic", "vendor-ref")
	if _, err := manager.Resume(context.Background(), generic.agent.ID()); !errors.Is(err, ErrResumeConflict) {
		t.Fatalf("unsupported resume error = %v, want ErrResumeConflict", err)
	}
	claude := addStoppedAgent(t, manager, "claude-agent", "claude", "vendor-ref")
	manager.mu.Lock()
	manager.resuming[claude.agent.ID()] = struct{}{}
	manager.mu.Unlock()
	if _, err := manager.Resume(context.Background(), claude.agent.ID()); !errors.Is(err, ErrResumeConflict) {
		t.Fatalf("reserved resume error = %v, want ErrResumeConflict", err)
	}
}

func TestResumeOnStartRunsEligibleAgentsInCreationOrderOnce(t *testing.T) {
	manager, _ := newTestManager(t)
	older := addStoppedAgent(t, manager, "older", "claude", "older-ref")
	time.Sleep(time.Millisecond)
	newer := addStoppedAgent(t, manager, "newer", "codex", "newer-ref")
	manual := addStoppedAgent(t, manager, "manual", "claude", "manual-ref")
	olderState := older.workspaceState()
	olderState.resumeOnStart = true
	older.setWorkspaceState(olderState)
	newerState := newer.workspaceState()
	newerState.resumeOnStart = true
	newer.setWorkspaceState(newerState)
	var commands []string
	manager.startPTY = func(config pty.Config) (launchedSession, error) {
		commands = append(commands, strings.Join(append([]string{config.Command}, config.Args...), " "))
		return &fakeProcessSession{}, nil
	}

	results := manager.ResumeOnStart(context.Background())
	if len(results) != 2 ||
		results[0].AgentID != older.agent.ID() ||
		results[0].Err != nil ||
		results[1].AgentID != newer.agent.ID() ||
		results[1].Err != nil {
		t.Fatalf("startup resume results = %+v", results)
	}
	if len(commands) != 2 ||
		commands[0] != "claude --resume older-ref" ||
		commands[1] != "codex resume newer-ref" {
		t.Fatalf("startup resume commands = %#v", commands)
	}
	if second := manager.ResumeOnStart(context.Background()); len(second) != 0 {
		t.Fatalf("second startup resume results = %+v, want none", second)
	}
	manualStatus, err := manager.Status(manual.agent.ID())
	if err != nil {
		t.Fatalf("manual status: %v", err)
	}
	if !manualStatus.Resumable {
		t.Fatalf("manual status = %+v, want resumable", manualStatus)
	}
}

func TestResumeOnStartRetriesFailedCandidate(t *testing.T) {
	manager, _ := newTestManager(t)
	managed := addStoppedAgent(t, manager, "agent-1", "claude", "vendor-ref")
	state := managed.workspaceState()
	state.resumeOnStart = true
	managed.setWorkspaceState(state)

	attempts := 0
	manager.startPTY = func(pty.Config) (launchedSession, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("exec unavailable")
		}
		return &fakeProcessSession{}, nil
	}

	first := manager.ResumeOnStart(context.Background())
	if len(first) != 1 ||
		first[0].AgentID != managed.agent.ID() ||
		first[0].Err == nil {
		t.Fatalf("first startup resume results = %+v", first)
	}
	if !managed.shouldResumeOnStart() {
		t.Fatal("failed startup resume consumed candidate")
	}

	second := manager.ResumeOnStart(context.Background())
	if len(second) != 1 ||
		second[0].AgentID != managed.agent.ID() ||
		second[0].Err != nil {
		t.Fatalf("second startup resume results = %+v", second)
	}
	if managed.shouldResumeOnStart() {
		t.Fatal("successful startup resume retained candidate")
	}
	if third := manager.ResumeOnStart(context.Background()); len(third) != 0 {
		t.Fatalf("third startup resume results = %+v, want none", third)
	}
}

func TestResumeOnStartPersistsCompletionAfterContextCancellation(t *testing.T) {
	manager, st := newTestManager(t)
	managed := addStoppedAgent(t, manager, "agent-1", "claude", "vendor-ref")
	state := managed.workspaceState()
	state.resumeOnStart = true
	managed.setWorkspaceState(state)

	ctx, cancel := context.WithCancel(context.Background())
	manager.startPTY = func(pty.Config) (launchedSession, error) {
		cancel()
		return &fakeProcessSession{}, nil
	}

	results := manager.ResumeOnStart(ctx)
	if len(results) != 1 ||
		results[0].AgentID != managed.agent.ID() ||
		results[0].Err != nil {
		t.Fatalf("startup resume results = %+v", results)
	}
	if managed.shouldResumeOnStart() {
		t.Fatal("durably completed startup resume retained candidate")
	}
	rows, err := st.Replay(string(managed.agent.ID()))
	if err != nil {
		t.Fatalf("replay startup resume: %v", err)
	}
	last := rows[len(rows)-1]
	if last.Type != string(event.TypeSessionLifecycle) ||
		last.Reason != startupResumeCompletedReason ||
		last.Payload != `{"version":1}` {
		t.Fatalf("startup resume completion = %+v", last)
	}
}

func TestManualResumeCompletesStartupResumeIntent(t *testing.T) {
	manager, st := newTestManager(t)
	managed := addStoppedAgent(t, manager, "agent-1", "claude", "vendor-ref")
	state := managed.workspaceState()
	state.resumeOnStart = true
	managed.setWorkspaceState(state)
	manager.committer.Close()
	commitStore := &resumeReservationCommitStore{
		commitStore: st,
		manager:     manager,
		id:          managed.agent.ID(),
	}
	manager.committer = newCommitter(0, commitStore, manager.hub)
	manager.startPTY = func(pty.Config) (launchedSession, error) {
		return &fakeProcessSession{}, nil
	}

	status, err := manager.Resume(context.Background(), managed.agent.ID())
	if err != nil {
		t.Fatalf("manual resume: %v", err)
	}
	if status.State != agent.StateWorking {
		t.Fatalf("manual resume status = %+v", status)
	}
	if managed.shouldResumeOnStart() {
		t.Fatal("manual resume retained startup resume intent")
	}
	if !commitStore.observedCompletion {
		t.Fatal("startup resume completion was not committed")
	}
	rows, err := st.Replay(string(managed.agent.ID()))
	if err != nil {
		t.Fatalf("replay manual resume: %v", err)
	}
	last := rows[len(rows)-1]
	if last.Type != string(event.TypeSessionLifecycle) ||
		last.Reason != startupResumeCompletedReason ||
		last.Payload != `{"version":1}` {
		t.Fatalf("startup resume completion = %+v", last)
	}
}

func TestNormalizeRunMode(t *testing.T) {
	tests := []struct {
		name    string
		input   agent.RunMode
		want    agent.RunMode
		wantErr bool
	}{
		{name: "omitted", want: agent.RunModeInteractive},
		{name: "interactive", input: agent.RunModeInteractive, want: agent.RunModeInteractive},
		{name: "oneshot", input: agent.RunModeOneshot, want: agent.RunModeOneshot},
		{name: "unknown", input: "batch", wantErr: true},
		{name: "whitespace", input: " interactive ", wantErr: true},
		{name: "case sensitive", input: "Interactive", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeRunMode(test.input)
			if test.wantErr {
				if !errors.Is(err, ErrInvalidMode) {
					t.Fatalf("normalize error = %v, want ErrInvalidMode", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			if got != test.want {
				t.Fatalf("normalized mode = %q, want %q", got, test.want)
			}
		})
	}
}

func TestStartRejectsInvalidModeWithoutHistory(t *testing.T) {
	manager, st := newTestManager(t)

	status, err := manager.Start(context.Background(), StartRequest{
		Command: "/bin/true",
		Mode:    "batch",
	})
	if !errors.Is(err, ErrInvalidMode) {
		t.Fatalf("start error = %v, want ErrInvalidMode", err)
	}
	if status != nil {
		t.Fatalf("start status = %+v, want nil", status)
	}
	lastSeq, err := st.LastSeq()
	if err != nil {
		t.Fatalf("last seq: %v", err)
	}
	if lastSeq != 0 {
		t.Fatalf("last seq = %d, want 0", lastSeq)
	}
}

func TestStartPersistsAndReportsLaunchMetadata(t *testing.T) {
	manager, _ := newTestManager(t)
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})
	workingDir := t.TempDir()

	status, err := manager.Start(context.Background(), StartRequest{
		Name:    "mode-agent",
		Command: "/bin/cat",
		Dir:     workingDir,
		Mode:    agent.RunModeOneshot,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if status.Mode != agent.RunModeOneshot {
		t.Fatalf("status mode = %q, want %q", status.Mode, agent.RunModeOneshot)
	}
	if status.Dir != workingDir {
		t.Fatalf("status dir = %q, want %q", status.Dir, workingDir)
	}

	rows, err := manager.Replay(status.AgentID)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("replay contains no creation event")
	}
	var metadata createdPayload
	if err := json.Unmarshal([]byte(rows[0].Payload), &metadata); err != nil {
		t.Fatalf("decode creation metadata: %v", err)
	}
	if metadata.Mode == nil || *metadata.Mode != agent.RunModeOneshot {
		t.Fatalf("creation mode = %v, want %q", metadata.Mode, agent.RunModeOneshot)
	}
	if metadata.Dir != "" || metadata.WorkingDir != "" {
		t.Fatalf("public creation metadata exposed working directory: %+v", metadata)
	}
}

func TestStartPersistsWorkingDirectoryPrivately(t *testing.T) {
	manager, st := newTestManager(t)
	workingDir := t.TempDir()
	var started pty.Config
	manager.startPTY = func(config pty.Config) (launchedSession, error) {
		started = config
		return &fakeProcessSession{}, nil
	}
	subscription := manager.hub.Subscribe(8)
	defer manager.hub.Unsubscribe(subscription)

	status, err := manager.Start(context.Background(), StartRequest{
		Name:    "directory-agent",
		Command: "/bin/cat",
		Dir:     workingDir,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if started.Dir != workingDir {
		t.Fatalf("started directory = %q, want %q", started.Dir, workingDir)
	}

	stored, err := st.Replay(status.AgentID)
	if err != nil {
		t.Fatalf("replay stored events: %v", err)
	}
	if len(stored) == 0 || !strings.Contains(stored[0].Payload, workingDir) {
		t.Fatalf("stored creation payload = %q, want private working directory", stored[0].Payload)
	}

	replayed, err := manager.Replay(status.AgentID)
	if err != nil {
		t.Fatalf("manager replay: %v", err)
	}
	if len(replayed) == 0 || strings.Contains(replayed[0].Payload, workingDir) {
		t.Fatalf("public replay exposed working directory: %+v", replayed)
	}

	select {
	case published := <-subscription.C():
		if published.Type != event.TypeSessionLifecycle ||
			strings.Contains(published.Payload, workingDir) {
			t.Fatalf("published creation event exposed working directory: %+v", published)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for published creation event")
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

func TestStartImmediateProcessRecordsWorkingBeforeCallbacks(t *testing.T) {
	manager, _ := newTestManager(t)

	status, err := manager.Start(context.Background(), StartRequest{
		Name:    "immediate-agent",
		Command: "/bin/sh",
		Args:    []string{"-c", "printf fast"},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if status.Mode != agent.RunModeInteractive {
		t.Fatalf("status mode = %q, want %q", status.Mode, agent.RunModeInteractive)
	}
	id := agent.ID(status.AgentID)
	t.Cleanup(func() {
		if stopErr := manager.Stop(id); stopErr != nil {
			t.Errorf("stop immediate agent: %v", stopErr)
		}
	})

	var rows []store.EventRow
	deadline := time.Now().Add(2 * time.Second)
	for {
		rows, err = manager.Replay(status.AgentID)
		if err != nil {
			t.Fatalf("replay: %v", err)
		}
		hasWorking := false
		hasOutput := false
		hasStopped := false
		for _, row := range rows {
			hasWorking = hasWorking ||
				(row.Type == string(event.TypeStateChanged) &&
					row.From == string(agent.StateStarting) &&
					row.To == string(agent.StateWorking))
			if row.Type == string(event.TypeOutputChunk) {
				hasOutput = string(outputChunkData(t, row)) == "fast"
			}
			hasStopped = hasStopped ||
				(row.Type == string(event.TypeStateChanged) &&
					row.To == string(agent.StateStopped))
		}
		if hasWorking && hasOutput && hasStopped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"timed out waiting for working, output, and stopped events: %+v",
				rows,
			)
		}
		time.Sleep(time.Millisecond)
	}

	workingIndex := -1
	outputIndex := -1
	stoppedIndex := -1
	outputCount := 0
	for i, row := range rows {
		switch {
		case row.Type == string(event.TypeStateChanged) &&
			row.From == string(agent.StateStarting) &&
			row.To == string(agent.StateWorking):
			workingIndex = i
		case row.Type == string(event.TypeOutputChunk):
			if string(outputChunkData(t, row)) == "fast" {
				outputIndex = i
				outputCount++
			}
		case row.Type == string(event.TypeStateChanged) &&
			row.To == string(agent.StateStopped):
			stoppedIndex = i
		}
	}
	if workingIndex < 0 || outputIndex < 0 || stoppedIndex < 0 {
		t.Fatalf(
			"event indexes: working=%d output=%d stopped=%d rows=%+v",
			workingIndex,
			outputIndex,
			stoppedIndex,
			rows,
		)
	}
	if outputCount != 1 {
		t.Fatalf("output event count = %d, want 1", outputCount)
	}
	if workingIndex >= outputIndex || workingIndex >= stoppedIndex {
		t.Fatalf(
			"working event index %d must precede output %d and stopped %d",
			workingIndex,
			outputIndex,
			stoppedIndex,
		)
	}
	if final := waitForDetachedState(t, manager, id, agent.StateStopped); final.PID != 0 {
		t.Fatalf("final status = %+v, want no PID", final)
	}
}

func TestStartUsesDeclaredInitialTerminalSize(t *testing.T) {
	stty, err := exec.LookPath("stty")
	if err != nil {
		t.Fatalf("find stty: %v", err)
	}
	manager, _ := newTestManager(t)

	status, err := manager.Start(context.Background(), StartRequest{
		Name:    "terminal-size",
		Command: stty,
		Args:    []string{"size"},
		Mode:    agent.RunModeOneshot,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := agent.ID(status.AgentID)
	waitForDetachedState(t, manager, id, agent.StateDone)

	rows, err := manager.Replay(status.AgentID)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	var output []byte
	for _, row := range rows {
		if row.Type == string(event.TypeOutputChunk) {
			output = append(output, outputChunkData(t, row)...)
		}
	}
	if observed := strings.TrimSpace(string(output)); observed != "40 120" {
		t.Fatalf("initial terminal size = %q, want %q", observed, "40 120")
	}
}

func TestManagerCloseWaitsForInFlightStart(t *testing.T) {
	manager, _ := newTestManager(t)

	if err := manager.beginStart(); err != nil {
		t.Fatalf("begin start: %v", err)
	}
	closeResult := make(chan error, 1)
	go func() {
		closeResult <- manager.Close()
	}()

	deadline := time.Now().Add(time.Second)
	for {
		manager.mu.RLock()
		closed := manager.closed
		manager.mu.RUnlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("manager did not enter closing state")
		}
		time.Sleep(time.Millisecond)
	}

	select {
	case err := <-closeResult:
		t.Fatalf("close returned before in-flight start completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	_, err := manager.Start(context.Background(), StartRequest{
		Command: "/bin/cat",
	})
	if !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("start while closing error = %v, want ErrManagerClosed", err)
	}

	manager.endStart()
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not finish after in-flight start completed")
	}
}

func TestManagerCloseStopsSessionsAndRejectsNewStarts(t *testing.T) {
	manager, st := newTestManager(t)

	started, err := manager.Start(context.Background(), StartRequest{
		Name:    "close-agent",
		Command: "/bin/cat",
		Mode:    agent.RunModeOneshot,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := agent.ID(started.AgentID)
	t.Cleanup(func() {
		if closeErr := manager.Close(); closeErr != nil {
			t.Errorf("cleanup close: %v", closeErr)
		}
	})

	if err := manager.Close(); err != nil {
		t.Fatalf("close manager: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("close manager again: %v", err)
	}

	status, err := manager.Status(id)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.State != agent.StateStopped || status.PID != 0 {
		t.Fatalf("status after close = %+v, want stopped without PID", status)
	}
	if status.LastError != "" {
		t.Fatalf("last error after requested stop = %q, want empty", status.LastError)
	}
	if err := manager.Stop(id); err != nil {
		t.Fatalf("stop after close: %v", err)
	}
	if _, err := manager.SendInput(id, []byte("input")); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("send input after close error = %v, want ErrManagerClosed", err)
	}

	rows, err := manager.Replay(started.AgentID)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	hasStopped := false
	for _, row := range rows {
		if row.Type == string(event.TypeStateChanged) &&
			row.From == string(agent.StateWorking) &&
			row.To == string(agent.StateStopped) {
			hasStopped = true
		}
	}
	if !hasStopped {
		t.Fatalf("replay has no working -> stopped event: %+v", rows)
	}

	lastSeq, err := st.LastSeq()
	if err != nil {
		t.Fatalf("last seq before rejected start: %v", err)
	}
	rejected, err := manager.Start(context.Background(), StartRequest{
		Name:    "rejected-agent",
		Command: "/bin/cat",
	})
	if !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("start after close error = %v, want ErrManagerClosed", err)
	}
	if rejected != nil {
		t.Fatalf("start after close status = %+v, want nil", rejected)
	}
	afterSeq, err := st.LastSeq()
	if err != nil {
		t.Fatalf("last seq after rejected start: %v", err)
	}
	if afterSeq != lastSeq {
		t.Fatalf("last seq after rejected start = %d, want %d", afterSeq, lastSeq)
	}
}

func TestSendInputValidatesBeforeLookingUpAgent(t *testing.T) {
	manager, _ := newTestManager(t)

	tests := []struct {
		name string
		data []byte
		want error
	}{
		{name: "empty", want: ErrInputEmpty},
		{name: "too large", data: []byte(strings.Repeat("x", MaxInputBytes+1)), want: ErrInputTooLarge},
		{name: "invalid UTF-8", data: []byte{0xff}, want: ErrInputNotUTF8},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := manager.SendInput("missing", test.data)
			if !errors.Is(err, test.want) {
				t.Fatalf("send input error = %v, want %v", err, test.want)
			}
			if result.BytesWritten != 0 {
				t.Fatalf("bytes written = %d, want 0", result.BytesWritten)
			}
		})
	}
}

func TestSendInputRejectsConcurrentAdmission(t *testing.T) {
	manager, _ := newTestManager(t)
	id := agent.ID("agent-1")
	a := agent.New(
		id,
		agent.WithName("agent"),
		agent.WithVendor("generic"),
		agent.WithRunMode(agent.RunModeInteractive),
	)
	running := &runningSession{process: &fakeProcessSession{writeN: 5}}
	manager.mu.Lock()
	manager.agents[id] = newManagedAgent(a)
	manager.sessions[id] = running
	manager.mu.Unlock()

	running.inputMu.Lock()
	result, err := manager.SendInput(id, []byte("input"))
	running.inputMu.Unlock()
	if !errors.Is(err, ErrInputBackpressure) {
		t.Fatalf("send input error = %v, want ErrInputBackpressure", err)
	}
	if result.BytesWritten != 0 {
		t.Fatalf("bytes written = %d, want 0", result.BytesWritten)
	}
}

func TestClassifyInputWriteBackpressure(t *testing.T) {
	t.Run("no bytes", func(t *testing.T) {
		err := classifyInputWrite(
			"agent-1",
			0,
			5,
			pty.ErrWriteBackpressure,
		)
		if !errors.Is(err, ErrInputBackpressure) {
			t.Fatalf("error = %v, want ErrInputBackpressure", err)
		}
		if errors.Is(err, ErrInputWrite) {
			t.Fatalf("zero-byte backpressure also reported ErrInputWrite: %v", err)
		}
	})

	t.Run("partial delivery", func(t *testing.T) {
		err := classifyInputWrite(
			"agent-1",
			2,
			5,
			pty.ErrWriteBackpressure,
		)
		if !errors.Is(err, ErrInputBackpressure) ||
			!errors.Is(err, ErrInputWrite) {
			t.Fatalf(
				"error = %v, want ErrInputBackpressure and ErrInputWrite",
				err,
			)
		}
		if !strings.Contains(err.Error(), "do not retry") {
			t.Fatalf("partial-delivery error = %v, want do-not-retry guidance", err)
		}
	})
}

func TestSendInputReportsPartialWriteWithoutAudit(t *testing.T) {
	manager, _ := newTestManager(t)
	id := agent.ID("agent-1")
	a := agent.New(
		id,
		agent.WithName("agent"),
		agent.WithVendor("generic"),
		agent.WithRunMode(agent.RunModeInteractive),
	)
	process := &fakeProcessSession{writeN: 2, writeErr: errors.New("broken pipe")}
	manager.mu.Lock()
	manager.agents[id] = newManagedAgent(a)
	manager.sessions[id] = &runningSession{process: process}
	manager.mu.Unlock()

	result, err := manager.SendInput(id, []byte("input"))
	if !errors.Is(err, ErrInputWrite) {
		t.Fatalf("send input error = %v, want ErrInputWrite", err)
	}
	if result.BytesWritten != 2 {
		t.Fatalf("bytes written = %d, want 2", result.BytesWritten)
	}
	if !strings.Contains(err.Error(), "do not retry") {
		t.Fatalf("partial write error = %v, want do-not-retry guidance", err)
	}
	rows, err := manager.Replay(string(id))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("events = %+v, want no successful input audit", rows)
	}
}

func TestSendInputAuditsWithoutPersistingContentBeforeExit(t *testing.T) {
	manager, _ := newTestManager(t)
	started, err := manager.Start(context.Background(), StartRequest{
		Name:    "input-agent",
		Command: "/bin/sh",
		Args:    []string{"-c", "IFS= read -r line"},
		Mode:    agent.RunModeInteractive,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := agent.ID(started.AgentID)
	t.Cleanup(func() {
		if closeErr := manager.Close(); closeErr != nil {
			t.Errorf("close manager: %v", closeErr)
		}
	})

	input := []byte("secret prompt\n")
	result, err := manager.SendInput(id, input)
	if err != nil {
		t.Fatalf("send input: %v", err)
	}
	if result.BytesWritten != len(input) {
		t.Fatalf("bytes written = %d, want %d", result.BytesWritten, len(input))
	}
	waitForDetachedState(t, manager, id, agent.StateStopped)

	rows, err := manager.Replay(started.AgentID)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	inputIndex := -1
	stoppedIndex := -1
	for i, row := range rows {
		if row.Type == string(event.TypeAgentInput) {
			inputIndex = i
			if row.Reason != "accepted" || row.Payload != `{"version":1,"bytes":14}` {
				t.Fatalf("input audit = %+v", row)
			}
			if strings.Contains(row.Payload, "secret") {
				t.Fatalf("input audit leaked content: %+v", row)
			}
		}
		if row.Type == string(event.TypeStateChanged) && row.To == string(agent.StateStopped) {
			stoppedIndex = i
		}
	}
	if inputIndex < 0 || stoppedIndex < 0 || inputIndex >= stoppedIndex {
		t.Fatalf("input index = %d, stopped index = %d; rows=%+v", inputIndex, stoppedIndex, rows)
	}
}

func TestSendInputReportsDeliveredButUnaudited(t *testing.T) {
	manager, st := newTestManager(t)
	id := agent.ID("agent-1")
	a := agent.New(
		id,
		agent.WithName("agent"),
		agent.WithVendor("generic"),
		agent.WithRunMode(agent.RunModeInteractive),
	)
	process := &fakeProcessSession{writeN: 5}
	manager.mu.Lock()
	manager.agents[id] = newManagedAgent(a)
	manager.sessions[id] = &runningSession{process: process}
	manager.mu.Unlock()
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	result, err := manager.SendInput(id, []byte("input"))
	if !errors.Is(err, ErrInputAudit) {
		t.Fatalf("send input error = %v, want ErrInputAudit", err)
	}
	if result.BytesWritten != 5 || !strings.Contains(err.Error(), "do not retry") {
		t.Fatalf("send input result = %+v, error = %v", result, err)
	}
}

func TestOneshotNaturalSuccessEndsDoneAndDetaches(t *testing.T) {
	manager, _ := newTestManager(t)

	started, err := manager.Start(context.Background(), StartRequest{
		Name:    "oneshot-success",
		Command: "/bin/sh",
		Args:    []string{"-c", "exit 0"},
		Mode:    agent.RunModeOneshot,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	status := waitForDetachedState(t, manager, agent.ID(started.AgentID), agent.StateDone)
	if status.PID != 0 {
		t.Fatalf("status after natural exit = %+v, want no PID", status)
	}
	if err := manager.Stop(agent.ID(started.AgentID)); err != nil {
		t.Fatalf("stop completed oneshot: %v", err)
	}
}

func TestNaturalFailurePersistsErrorAndDetaches(t *testing.T) {
	manager, _ := newTestManager(t)

	started, err := manager.Start(context.Background(), StartRequest{
		Name:    "oneshot-failure",
		Command: "/bin/sh",
		Args:    []string{"-c", "exit 7"},
		Mode:    agent.RunModeOneshot,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	status := waitForDetachedState(t, manager, agent.ID(started.AgentID), agent.StateStopped)
	if status.PID != 0 || status.LastError == "" {
		t.Fatalf("status after failed exit = %+v, want error without PID", status)
	}

	rows, err := manager.Replay(started.AgentID)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	errorIndex := -1
	stoppedIndex := -1
	for i, row := range rows {
		if row.Type == string(event.TypeError) && row.Payload == status.LastError {
			errorIndex = i
		}
		if row.Type == string(event.TypeStateChanged) && row.To == string(agent.StateStopped) {
			stoppedIndex = i
		}
	}
	if errorIndex < 0 || stoppedIndex < 0 || errorIndex >= stoppedIndex {
		t.Fatalf("error index = %d, stopped index = %d, rows = %+v", errorIndex, stoppedIndex, rows)
	}
}

func TestProcessGroupCleanupFailurePersistsErrorAndStopsOneshot(t *testing.T) {
	for _, test := range []struct {
		name       string
		code       int
		processErr error
	}{
		{name: "successful leader", code: 0},
		{
			name:       "failed leader",
			code:       7,
			processErr: errors.New("process exit failed"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, _ := newTestManager(t)
			id := agent.ID("cleanup-failure")
			a := agent.New(
				id,
				agent.WithName(string(id)),
				agent.WithVendor("generic"),
				agent.WithRunMode(agent.RunModeOneshot),
				agent.WithHookPolicy(agent.HooksOff),
			)
			manager.mu.Lock()
			manager.agents[id] = newManagedAgent(a)
			manager.mu.Unlock()
			commitTestState(t, manager, a, agent.StateStarting, "test start")
			commitTestState(t, manager, a, agent.StateWorking, "test working")
			running := attachTestRuntime(t, manager, a, manager.reg.For("generic"))

			cleanupErr := errors.New("process group remains after permission error")
			manager.onExit(id, running, pty.ExitInfo{
				Code:       test.code,
				Err:        test.processErr,
				CleanupErr: cleanupErr,
			})

			status, err := manager.Status(id)
			if err != nil {
				t.Fatalf("status: %v", err)
			}
			if status.State != agent.StateStopped ||
				!strings.Contains(status.LastError, cleanupErr.Error()) {
				t.Fatalf(
					"status = %+v, want stopped with cleanup error %q",
					status,
					cleanupErr,
				)
			}
			if test.processErr != nil &&
				!strings.Contains(status.LastError, test.processErr.Error()) {
				t.Fatalf(
					"last error = %q, want process error %q",
					status.LastError,
					test.processErr,
				)
			}

			rows, err := manager.Replay(string(id))
			if err != nil {
				t.Fatalf("replay: %v", err)
			}
			errorIndex := -1
			stoppedIndex := -1
			for index, row := range rows {
				if row.Type == string(event.TypeError) &&
					strings.Contains(row.Payload, cleanupErr.Error()) {
					errorIndex = index
				}
				if row.Type == string(event.TypeStateChanged) &&
					row.To == string(agent.StateStopped) {
					stoppedIndex = index
				}
			}
			if errorIndex < 0 || stoppedIndex < 0 || errorIndex >= stoppedIndex {
				t.Fatalf(
					"error index = %d, stopped index = %d, rows = %+v",
					errorIndex,
					stoppedIndex,
					rows,
				)
			}
		})
	}
}

func TestUserStopOneshotEndsStoppedWithoutProcessError(t *testing.T) {
	manager, _ := newTestManager(t)

	started, err := manager.Start(context.Background(), StartRequest{
		Name:    "oneshot-stop",
		Command: "/bin/cat",
		Mode:    agent.RunModeOneshot,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := agent.ID(started.AgentID)
	if err := manager.Stop(id); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := manager.Stop(id); err != nil {
		t.Fatalf("stop again: %v", err)
	}

	status, err := manager.Status(id)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.State != agent.StateStopped || status.PID != 0 || status.LastError != "" {
		t.Fatalf("status after stop = %+v, want stopped without PID or error", status)
	}

	rows, err := manager.Replay(started.AgentID)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	for _, row := range rows {
		if row.Type == string(event.TypeError) {
			t.Fatalf("requested stop persisted unexpected error: %+v", row)
		}
	}
}

func commitTestState(
	t *testing.T,
	manager *Manager,
	a *agent.Agent,
	target agent.State,
	reason string,
) {
	t.Helper()
	source := agent.EvidenceProcess
	eventName := "process_started"
	if target == agent.StateStarting {
		source = agent.EvidenceSession
		eventName = "session_start"
	}
	if _, err := manager.committer.CommitAgent(
		context.Background(),
		a,
		agent.MoveTo(target, reason, agent.Evidence{
			Source: source, Event: eventName, Confidence: 1,
		}),
		nil,
	); err != nil {
		t.Fatalf("transition %s: %v", target, err)
	}
}

func TestStopCauseAndExitClaimHaveOneWinner(t *testing.T) {
	t.Run("stop first", func(t *testing.T) {
		manager, _ := newTestManager(t)
		id := agent.ID("agent-1")
		running := &runningSession{process: &fakeProcessSession{}}
		manager.sessions[id] = running

		manager.requestStop(id, running, stopCauseUser)
		cause, ok := manager.claimExit(id, running)
		if !ok || cause != stopCauseUser {
			t.Fatalf("claim = (%d, %v), want user stop", cause, ok)
		}
		if _, ok := manager.claimExit(id, running); ok {
			t.Fatal("second exit claim succeeded")
		}
	})

	t.Run("exit first", func(t *testing.T) {
		manager, _ := newTestManager(t)
		id := agent.ID("agent-1")
		running := &runningSession{process: &fakeProcessSession{}}
		manager.sessions[id] = running

		cause, ok := manager.claimExit(id, running)
		if !ok || cause != stopCauseNone {
			t.Fatalf("claim = (%d, %v), want natural exit", cause, ok)
		}
		manager.requestStop(id, running, stopCauseUser)
		if running.stopCause != stopCauseNone {
			t.Fatalf("stop cause = %d, want unchanged", running.stopCause)
		}
	})
}

func TestNaturalExitRacingStopRecordsOneTerminalTransition(t *testing.T) {
	for i := range 10 {
		manager, _ := newTestManager(t)
		started, err := manager.Start(context.Background(), StartRequest{
			Name:    "racing-agent",
			Command: "/bin/sh",
			Args:    []string{"-c", "exit 0"},
			Mode:    agent.RunModeOneshot,
		})
		if err != nil {
			t.Fatalf("iteration %d start: %v", i, err)
		}
		id := agent.ID(started.AgentID)

		stopResult := make(chan error, 1)
		go func() {
			stopResult <- manager.Stop(id)
		}()
		select {
		case stopErr := <-stopResult:
			if stopErr != nil {
				t.Fatalf("iteration %d stop: %v", i, stopErr)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d stop timed out", i)
		}

		status, err := manager.Status(id)
		if err != nil {
			t.Fatalf("iteration %d status: %v", i, err)
		}
		if status.PID != 0 ||
			(status.State != agent.StateDone && status.State != agent.StateStopped) {
			t.Fatalf("iteration %d final status = %+v", i, status)
		}

		rows, err := manager.Replay(started.AgentID)
		if err != nil {
			t.Fatalf("iteration %d replay: %v", i, err)
		}
		terminalTransitions := 0
		for _, row := range rows {
			if row.Type == string(event.TypeStateChanged) &&
				(row.To == string(agent.StateDone) || row.To == string(agent.StateStopped)) {
				terminalTransitions++
			}
		}
		if terminalTransitions != 1 {
			t.Fatalf(
				"iteration %d terminal transitions = %d, want 1; rows=%+v",
				i,
				terminalTransitions,
				rows,
			)
		}
	}
}

func TestOutputAfterTerminalStateRemainsReplayable(t *testing.T) {
	manager, _ := newTestManager(t)
	id := agent.ID("trailing-output")
	a := agent.New(
		id,
		agent.WithName("trailing-output"),
		agent.WithVendor("claude"),
		agent.WithRunMode(agent.RunModeInteractive),
		agent.WithHookPolicy(agent.HooksOff),
	)
	manager.mu.Lock()
	manager.agents[id] = newManagedAgent(a)
	manager.mu.Unlock()
	commitTestState(t, manager, a, agent.StateStarting, "test start")
	commitTestState(t, manager, a, agent.StateWorking, "test working")
	running := attachTestRuntime(t, manager, a, manager.reg.For("claude"))
	terminalActor, err := newTerminalActor(
		mustInitialTerminalSize(t),
		&terminalTestProcess{},
		running.classifier,
		nil,
		running.observer,
		running.vendor,
		manager.clock,
		nil,
	)
	if err != nil {
		t.Fatalf("new terminal actor: %v", err)
	}
	running.terminal = terminalActor
	defer terminalActor.Close()

	manager.onExit(id, running, pty.ExitInfo{Code: 0})
	output := []byte("\x1b[2J\x1b[30;1HDo you want to proceed?\r\nEsc to cancel")
	if err := running.output.Feed(output, 0); err != nil {
		t.Fatalf("feed trailing output: %v", err)
	}
	if err := running.output.End(uint64(len(output))); err != nil {
		t.Fatalf("end trailing output: %v", err)
	}
	if _, available := terminalActor.Snapshot(); available {
		t.Fatal("terminal snapshot remained available after process exit")
	}

	rows, err := manager.Replay(string(id))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	stoppedIndex := -1
	outputIndex := -1
	for index, row := range rows {
		if row.Type == string(event.TypeStateChanged) &&
			row.To == string(agent.StateStopped) {
			stoppedIndex = index
		}
		if row.Type == string(event.TypeOutputChunk) {
			payload, decodeErr := event.DecodeOutputChunkPayload(row.Payload)
			if decodeErr != nil {
				t.Fatalf("decode output chunk: %v", decodeErr)
			}
			data, decodeErr := payload.DecodeData()
			if decodeErr != nil {
				t.Fatalf("decode output data: %v", decodeErr)
			}
			if bytes.Equal(data, output) {
				outputIndex = index
			}
		}
		if signalPayloadVersion(row.Payload) == 3 {
			t.Fatalf("trailing screen produced a screen signal: %+v", row)
		}
	}
	if stoppedIndex < 0 || outputIndex <= stoppedIndex {
		t.Fatalf(
			"stopped index = %d, output index = %d; rows=%+v",
			stoppedIndex,
			outputIndex,
			rows,
		)
	}
}

func TestManagerCloseClosesAttachedTerminalActor(t *testing.T) {
	manager, _ := newTestManager(t)
	id := agent.ID("close-terminal")
	running := &runningSession{process: &fakeProcessSession{}}
	terminalActor := newTerminalTestActor(
		t,
		"generic",
		newTerminalTestClock(time.Unix(1_000, 0).UTC()),
		&terminalTestProcess{},
		&recordingTerminalObserver{},
	)
	running.terminal = terminalActor
	manager.mu.Lock()
	manager.sessions[id] = running
	manager.mu.Unlock()

	if err := manager.Close(); err != nil {
		t.Fatalf("close manager: %v", err)
	}
	chunk, err := term.NewCommittedChunk([]byte("x"), 1, 1, time.Now().UTC())
	if err != nil {
		t.Fatalf("new committed chunk: %v", err)
	}
	if _, err := terminalActor.FeedCommitted(context.Background(), chunk); !errors.Is(
		err,
		errTerminalActorClosed,
	) {
		t.Fatalf("feed after manager close = %v, want actor closed", err)
	}
}

func waitForDetachedState(t *testing.T, manager *Manager, id agent.ID, want agent.State) *Status {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		status, err := manager.Status(id)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if status.State == want && status.PID == 0 {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("status = %+v, want state %s without PID", status, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func newTestManager(
	t *testing.T,
	options ...ManagerOption,
) (*Manager, *store.Store) {
	t.Helper()

	st := newTestStore(t)
	manager := NewManager(adapter.NewRegistry(), event.NewHub(0), st, 0, options...)
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})
	return manager, st
}

func addStoppedAgent(
	t *testing.T,
	manager *Manager,
	id agent.ID,
	vendor string,
	ref string,
) *managedAgent {
	t.Helper()
	target := agent.New(
		id,
		agent.WithName(string(id)),
		agent.WithVendor(vendor),
		agent.WithRunMode(agent.RunModeInteractive),
		agent.WithHookPolicy(agent.HooksOff),
	)
	prepared, err := target.Prepare(agent.MoveTo(
		agent.StateStopped,
		"test stopped",
		agent.Evidence{
			Source: agent.EvidenceSession, Event: "session_stop", Confidence: 1,
		},
	))
	if err != nil {
		t.Fatalf("prepare stopped agent: %v", err)
	}
	if err := target.ApplyCommitted(prepared); err != nil {
		t.Fatalf("apply stopped agent: %v", err)
	}
	managed := newManagedAgent(target)
	managed.setVendorSessionReference(ref)
	manager.mu.Lock()
	manager.agents[id] = managed
	manager.mu.Unlock()
	return managed
}

func attachTestRuntime(
	t *testing.T,
	manager *Manager,
	a *agent.Agent,
	entry adapter.Entry,
	terminalNotifications ...bool,
) *runningSession {
	t.Helper()

	managed, ok := manager.managed(a.ID())
	if !ok {
		t.Fatalf("managed agent %q is not registered", a.ID())
	}
	notificationsEnabled := len(terminalNotifications) > 0 &&
		terminalNotifications[0]
	running, _, _, err := manager.prepareManagedRuntimeWithTerminalNotifications(
		managed,
		entry,
		notificationsEnabled,
	)
	if err != nil {
		t.Fatalf("prepare runtime: %v", err)
	}
	running.process = &fakeProcessSession{}
	close(running.signalReady)
	close(running.callbacksReady)
	manager.mu.Lock()
	manager.sessions[a.ID()] = running
	manager.mu.Unlock()
	return running
}

func outputChunkData(t *testing.T, row store.EventRow) []byte {
	t.Helper()

	payload, err := event.DecodeOutputChunkPayload(row.Payload)
	if err != nil {
		t.Fatalf("decode output chunk at seq %d: %v", row.Seq, err)
	}
	data, err := payload.DecodeData()
	if err != nil {
		t.Fatalf("decode output chunk data at seq %d: %v", row.Seq, err)
	}
	return data
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

type fakeProcessSession struct {
	writeN   int
	writeErr error
}

func (s *fakeProcessSession) Write([]byte) (int, error) {
	return s.writeN, s.writeErr
}

func (s *fakeProcessSession) Close() error {
	return nil
}

func (s *fakeProcessSession) PID() int {
	return 1
}

func (s *fakeProcessSession) Resize(uint16, uint16) error {
	return nil
}

func outputProcessorGoroutines() int {
	size := 64 * 1024
	for {
		stack := make([]byte, size)
		n := runtime.Stack(stack, true)
		if n < len(stack) {
			return strings.Count(
				string(stack[:n]),
				"github.com/Duang777/drove/internal/session.(*outputProcessor).run",
			)
		}
		size *= 2
	}
}

type cancelAfterAppendStore struct {
	commitStore
	cancel context.CancelFunc
	once   sync.Once
}

type resumeReservationCommitStore struct {
	commitStore
	manager            *Manager
	id                 agent.ID
	observedCompletion bool
}

func (s *resumeReservationCommitStore) AppendEvents(
	ctx context.Context,
	expectedLastSeq uint64,
	rows []store.EventRow,
) (uint64, error) {
	for _, row := range rows {
		if row.Type != string(event.TypeSessionLifecycle) ||
			row.Reason != startupResumeCompletedReason {
			continue
		}
		s.manager.mu.RLock()
		_, reserved := s.manager.resuming[s.id]
		s.manager.mu.RUnlock()
		if !reserved {
			return expectedLastSeq, errors.New(
				"startup resume completion committed without reservation",
			)
		}
		s.observedCompletion = true
	}
	return s.commitStore.AppendEvents(ctx, expectedLastSeq, rows)
}

func (s *cancelAfterAppendStore) AppendEvents(
	ctx context.Context,
	expectedLastSeq uint64,
	rows []store.EventRow,
) (uint64, error) {
	lastSeq, err := s.commitStore.AppendEvents(ctx, expectedLastSeq, rows)
	if err == nil {
		s.once.Do(s.cancel)
	}
	return lastSeq, err
}
