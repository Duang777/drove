package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/pty"
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
	if err := result.Manager.Stop("missing"); err == nil {
		t.Fatal("stop unknown agent succeeded")
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
	if len(rows) != 3 ||
		rows[0].Type != string(event.TypeSessionLifecycle) ||
		rows[1].From != string(agent.StatePending) ||
		rows[1].To != string(agent.StateStarting) ||
		rows[2].From != string(agent.StateStarting) ||
		rows[2].To != string(agent.StateWorking) {
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
	manager.agents[id] = a
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
	if len(rows) != 4 {
		t.Fatalf("event count = %d, want 4: %+v", len(rows), rows)
	}
	if rows[0].Seq != 1 ||
		rows[0].Type != string(event.TypeSessionLifecycle) ||
		rows[0].Reason != "created" ||
		rows[0].SessionID != rows[0].AgentID ||
		rows[0].Payload != `{"version":1,"name":"broken-agent","vendor":"generic","mode":"interactive"}` {
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

func TestStartPersistsAndReportsRunMode(t *testing.T) {
	manager, _ := newTestManager(t)
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})

	status, err := manager.Start(context.Background(), StartRequest{
		Name:    "mode-agent",
		Command: "/bin/cat",
		Mode:    agent.RunModeOneshot,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if status.Mode != agent.RunModeOneshot {
		t.Fatalf("status mode = %q, want %q", status.Mode, agent.RunModeOneshot)
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
			hasOutput = hasOutput ||
				(row.Type == string(event.TypeOutput) && row.Payload == "fast")
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
		case row.Type == string(event.TypeOutput) && row.Payload == "fast":
			outputIndex = i
			outputCount++
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
	manager.agents[id] = a
	manager.sessions[id] = &runningSession{process: process}
	manager.mu.Unlock()

	result, err := manager.SendInput(id, []byte("input"))
	if !errors.Is(err, ErrInputWrite) {
		t.Fatalf("send input error = %v, want ErrInputWrite", err)
	}
	if result.BytesWritten != 2 {
		t.Fatalf("bytes written = %d, want 2", result.BytesWritten)
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
	manager.agents[id] = a
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

func TestDecideExit(t *testing.T) {
	exitErr := errors.New("exit status 7")
	tests := []struct {
		name      string
		mode      agent.RunMode
		cause     stopCause
		info      pty.ExitInfo
		wantState agent.State
		wantError string
	}{
		{
			name:      "natural interactive success",
			mode:      agent.RunModeInteractive,
			info:      pty.ExitInfo{Code: 0},
			wantState: agent.StateStopped,
		},
		{
			name:      "natural oneshot success",
			mode:      agent.RunModeOneshot,
			info:      pty.ExitInfo{Code: 0},
			wantState: agent.StateDone,
		},
		{
			name:      "natural interactive failure",
			mode:      agent.RunModeInteractive,
			info:      pty.ExitInfo{Code: 7, Err: exitErr},
			wantState: agent.StateStopped,
			wantError: exitErr.Error(),
		},
		{
			name:      "natural oneshot failure",
			mode:      agent.RunModeOneshot,
			info:      pty.ExitInfo{Code: 7, Err: exitErr},
			wantState: agent.StateStopped,
			wantError: exitErr.Error(),
		},
		{
			name:      "user stop suppresses process error",
			mode:      agent.RunModeOneshot,
			cause:     stopCauseUser,
			info:      pty.ExitInfo{Code: -1, Err: errors.New("signal: killed")},
			wantState: agent.StateStopped,
		},
		{
			name:      "shutdown suppresses process error",
			mode:      agent.RunModeOneshot,
			cause:     stopCauseShutdown,
			info:      pty.ExitInfo{Code: -1, Err: errors.New("signal: killed")},
			wantState: agent.StateStopped,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := decideExit(test.mode, test.cause, test.info)
			if got.target != test.wantState || got.errorMessage != test.wantError {
				t.Fatalf("decision = %+v, want state=%s error=%q", got, test.wantState, test.wantError)
			}
		})
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

func TestInteractiveIgnoresDoneHint(t *testing.T) {
	manager, _ := newTestManager(t)
	id := agent.ID("interactive-agent")
	a := agent.New(
		id,
		agent.WithName("interactive-agent"),
		agent.WithVendor("claude"),
		agent.WithRunMode(agent.RunModeInteractive),
	)
	manager.mu.Lock()
	manager.agents[id] = a
	manager.mu.Unlock()
	commitTestState(t, manager, a, agent.StateStarting, "test start")
	commitTestState(t, manager, a, agent.StateWorking, "test working")
	attachTestRuntime(t, manager, a, manager.reg.For("claude"))

	manager.onOutput(id, "Task complete!", manager.reg.For("claude"), "")

	if got := a.State(); got != agent.StateWorking {
		t.Fatalf("interactive state = %s, want working", got)
	}
}

func TestOnOutputSanitizesOnlyHeuristicView(t *testing.T) {
	manager, _ := newTestManager(t)
	id := agent.ID("ansi-agent")
	a := agent.New(
		id,
		agent.WithName("ansi-agent"),
		agent.WithVendor("claude"),
		agent.WithRunMode(agent.RunModeInteractive),
	)
	manager.mu.Lock()
	manager.agents[id] = a
	manager.mu.Unlock()
	commitTestState(t, manager, a, agent.StateStarting, "test start")
	commitTestState(t, manager, a, agent.StateWorking, "test working")
	attachTestRuntime(t, manager, a, manager.reg.For("claude"))

	subscription := manager.hub.Subscribe(4)
	defer manager.hub.Unsubscribe(subscription)
	raw := "Waiting \x1b[2K\x1b[1Gfor your input"
	manager.onOutput(id, raw, manager.reg.For("claude"), "")

	if got := a.State(); got != agent.StateBlocked {
		t.Fatalf("state = %s, want blocked from sanitized heuristic text", got)
	}
	rows, err := manager.Replay(string(id))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	var persistedOutput string
	for _, row := range rows {
		if row.Type == string(event.TypeOutput) {
			persistedOutput = row.Payload
		}
	}
	if persistedOutput != raw {
		t.Fatalf("persisted output = %q, want raw %q", persistedOutput, raw)
	}
	select {
	case streamed := <-subscription.C():
		if streamed.Type != event.TypeOutput || streamed.Payload != raw {
			t.Fatalf("streamed event = %+v, want raw output", streamed)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for streamed output")
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

func newTestManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()

	st := newTestStore(t)
	manager := NewManager(adapter.NewRegistry(), event.NewHub(0), st, 0)
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})
	return manager, st
}

func attachTestRuntime(
	t *testing.T,
	manager *Manager,
	a *agent.Agent,
	entry adapter.Entry,
) *runningSession {
	t.Helper()

	running, _, err := manager.prepareRuntime(a, entry)
	if err != nil {
		t.Fatalf("prepare runtime: %v", err)
	}
	running.process = &fakeProcessSession{}
	close(running.ready)
	manager.mu.Lock()
	manager.sessions[a.ID()] = running
	manager.mu.Unlock()
	return running
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

type cancelAfterAppendStore struct {
	commitStore
	cancel context.CancelFunc
	once   sync.Once
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
