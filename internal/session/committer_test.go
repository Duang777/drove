package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
)

func TestCommitterStoresOutputMetadataAndPublishesHydratedChunk(t *testing.T) {
	st := &memoryCommitStore{}
	hub := event.NewHub(0)
	subscription := hub.Subscribe(1)
	defer hub.Unsubscribe(subscription)
	committer := newCommitter(0, st, hub)
	defer committer.Close()

	data := []byte("prompt\x00")
	draft, err := event.NewOutputChunkDraft("agent-1", "agent-1", 12, data)
	if err != nil {
		t.Fatalf("new output chunk draft: %v", err)
	}
	if _, err := committer.CommitEvents(context.Background(), []event.Draft{draft}); err != nil {
		t.Fatalf("commit output chunk: %v", err)
	}

	rows := st.Rows()
	if len(rows) != 1 {
		t.Fatalf("stored row count = %d, want 1", len(rows))
	}
	stored, err := event.DecodeOutputChunkPayload(rows[0].Payload)
	if err != nil {
		t.Fatalf("decode stored output metadata: %v", err)
	}
	if stored.DataB64 != "" || stored.Offset != 12 || stored.Len != len(data) {
		t.Fatalf("stored payload = %+v", stored)
	}
	if !bytes.Equal(rows[0].OutputAttachment, data) {
		t.Fatalf("stored attachment = %q, want %q", rows[0].OutputAttachment, data)
	}

	select {
	case published := <-subscription.C():
		payload, err := event.DecodeOutputChunkPayload(published.Payload)
		if err != nil {
			t.Fatalf("decode published output payload: %v", err)
		}
		decoded, err := payload.DecodeData()
		if err != nil {
			t.Fatalf("decode published output data: %v", err)
		}
		if !bytes.Equal(decoded, data) {
			t.Fatalf("published data = %q, want %q", decoded, data)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for output chunk")
	}
}

func TestEncodeStateEvidenceSelectsVersionBySource(t *testing.T) {
	screen, err := agent.NewScreenAttribution(
		"claude.approval_prompt",
		agent.ScreenEdgeCleared,
		"viewport.bottom",
		4312,
		918,
		"approval prompt",
	)
	if err != nil {
		t.Fatalf("new screen attribution: %v", err)
	}
	terminal, err := agent.NewTerminalAttribution("osc9", 5312, 1018)
	if err != nil {
		t.Fatalf("new terminal attribution: %v", err)
	}
	tests := []struct {
		name     string
		evidence agent.Evidence
		version  int
	}{
		{
			name: "process v1",
			evidence: agent.Evidence{
				Source: agent.EvidenceProcess, Event: "process_started", Confidence: 1,
			},
			version: 1,
		},
		{
			name: "notify v2",
			evidence: agent.Evidence{
				Source: agent.EvidenceNotify, Event: "agent-turn-complete", Confidence: 1,
				DeliveryID: "550e8400-e29b-41d4-a716-446655440000",
			},
			version: 2,
		},
		{
			name: "screen v3",
			evidence: agent.Evidence{
				Source: agent.EvidenceScreen, Event: screen.Rule, Confidence: 1,
				Screen: &screen,
			},
			version: 3,
		},
		{
			name: "terminal notify v4",
			evidence: agent.Evidence{
				Source: agent.EvidenceNotify, Event: "tui_notification", Confidence: 1,
				Terminal: &terminal,
			},
			version: 4,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := encodeStateEvidence(test.evidence)
			if err != nil {
				t.Fatalf("encode state evidence: %v", err)
			}
			var version struct {
				Version int `json:"version"`
			}
			if err := json.Unmarshal(encoded, &version); err != nil {
				t.Fatalf("decode evidence version: %v", err)
			}
			if version.Version != test.version {
				t.Fatalf("version = %d, want %d", version.Version, test.version)
			}
			if test.version == 3 {
				var payload event.StateEvidencePayloadV3
				if err := json.Unmarshal(encoded, &payload); err != nil {
					t.Fatalf("decode screen evidence: %v", err)
				}
				if err := payload.Validate(); err != nil {
					t.Fatalf("validate screen evidence: %v", err)
				}
			}
			if test.version == 4 {
				var payload event.StateEvidencePayloadV4
				if err := json.Unmarshal(encoded, &payload); err != nil {
					t.Fatalf("decode terminal evidence: %v", err)
				}
				if err := payload.Validate(); err != nil {
					t.Fatalf("validate terminal evidence: %v", err)
				}
			}
		})
	}
}

func TestTypedCommitterSealsDraftsAndAppliesAgentAfterStore(t *testing.T) {
	a := agent.New(
		"agent-1",
		agent.WithRunMode(agent.RunModeOneshot),
		agent.WithHookPolicy(agent.HooksAuto),
	)
	st := &memoryCommitStore{}
	st.onAppend = func() {
		if a.State() != agent.StatePending {
			t.Errorf("agent state changed before SQLite append: %s", a.State())
		}
	}
	hub := event.NewHub(0)
	subscription := hub.Subscribe(4)
	defer hub.Unsubscribe(subscription)
	committer := newCommitter(0, st, hub)
	defer committer.Close()
	managed := newManagedAgent(a)

	receipt, err := committer.CommitAgent(
		context.Background(),
		managed,
		agent.MoveTo(agent.StateStarting, "session start", agent.Evidence{
			Source:     agent.EvidenceSession,
			Event:      "session_start",
			Confidence: 1,
		}),
		[]event.Draft{
			event.NewSessionLifecycleDraft(
				"agent-1",
				"agent-1",
				"created",
				`{"version":2}`,
			),
		},
	)
	if err != nil {
		t.Fatalf("commit agent: %v", err)
	}
	if receipt.FirstSeq != 1 || receipt.LastSeq != 2 || receipt.Timestamp.IsZero() {
		t.Fatalf("receipt = %+v", receipt)
	}
	if a.State() != agent.StateStarting ||
		a.LastTransition() == nil ||
		a.LastTransition().Event != "session_start" {
		t.Fatalf("agent projection = state %s evidence %+v", a.State(), a.LastTransition())
	}
	if got := managed.currentStateSeq(); got != 2 {
		t.Fatalf("state sequence = %d, want 2", got)
	}
	rows := st.Rows()
	if len(rows) != 2 ||
		rows[0].Type != string(event.TypeSessionLifecycle) ||
		rows[1].Type != string(event.TypeStateChanged) ||
		rows[1].Payload == "" {
		t.Fatalf("stored rows = %+v", rows)
	}
	for want := uint64(1); want <= 2; want++ {
		select {
		case published := <-subscription.C():
			if published.Seq != want || a.State() != agent.StateStarting {
				t.Fatalf("published event = %+v, agent state = %s", published, a.State())
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for sequence %d", want)
		}
	}
}

func TestTypedCommitterStoreFailureLeavesAgentUnchanged(t *testing.T) {
	storageErr := errors.New("disk unavailable")
	st := &memoryCommitStore{appendErr: storageErr}
	hub := event.NewHub(0)
	committer := newCommitter(0, st, hub)
	defer committer.Close()
	a := agent.New("agent-1")
	managed := newManagedAgent(a)

	_, err := committer.CommitAgent(
		context.Background(),
		managed,
		agent.MoveTo(agent.StateStarting, "session start", agent.Evidence{
			Source:     agent.EvidenceSession,
			Event:      "session_start",
			Confidence: 1,
		}),
		nil,
	)
	if !errors.Is(err, storageErr) {
		t.Fatalf("commit error = %v, want storage error", err)
	}
	if snapshot := a.Snapshot(); snapshot.State != agent.StatePending || snapshot.Revision != 0 {
		t.Fatalf("agent changed after failed append: %+v", snapshot)
	}
	if got := managed.currentStateSeq(); got != 0 {
		t.Fatalf("state sequence = %d after failed append, want 0", got)
	}
}

func TestWorkspaceRemovalCommitAppliesManagedProjectionAfterStore(t *testing.T) {
	managed := newManagedAgent(agent.New("agent-1"))
	managed.setWorkspaceState(workspaceRuntimeState{
		workingDir:     "/tmp/worktree",
		resumeOnStart:  true,
		removalPending: true,
	})
	st := &memoryCommitStore{}
	st.onAppend = func() {
		state := managed.workspaceState()
		if state.removed || state.workingDir == "" {
			t.Errorf("workspace projection changed before append: %+v", state)
		}
	}
	hub := event.NewHub(0)
	committer := newCommitter(0, st, hub)
	defer committer.Close()

	receipt, err := committer.CommitWorkspaceRemoved(
		context.Background(),
		managed,
	)
	if err != nil {
		t.Fatalf("commit workspace removal: %v", err)
	}
	if !receipt.Durable || receipt.FirstSeq != 1 || receipt.LastSeq != 1 {
		t.Fatalf("receipt = %+v", receipt)
	}
	state := managed.workspaceState()
	if !state.removed ||
		state.removalPending ||
		state.resumeOnStart ||
		state.workingDir != "" {
		t.Fatalf("workspace projection = %+v", state)
	}
	rows := st.Rows()
	if len(rows) != 1 ||
		rows[0].Type != string(event.TypeSessionLifecycle) ||
		rows[0].Reason != workspaceRemovedReason ||
		rows[0].Payload != `{"version":1}` {
		t.Fatalf("stored rows = %+v", rows)
	}
}

func TestDecisionCommitAppliesBothProjectionsAfterStore(t *testing.T) {
	a, err := agent.Restore(agent.RestoreSnapshot{
		ID:         "agent-1",
		Name:       "test",
		Vendor:     "claude",
		RunMode:    agent.RunModeInteractive,
		HookPolicy: agent.HooksAuto,
		State:      agent.StateWorking,
		CreatedAt:  time.Now().Add(-time.Minute),
		UpdatedAt:  time.Now(),
	})
	if err != nil {
		t.Fatalf("restore Agent: %v", err)
	}
	managed := newManagedAgent(a)
	detector, err := detect.New(detect.Config{})
	if err != nil {
		t.Fatalf("new Detector: %v", err)
	}
	detectorState := detect.NewState(agent.HooksAuto)
	signal, err := detect.NewHookSignal(detect.Signal{
		Kind:             detect.KindHumanInputRequired,
		Vendor:           "claude",
		VendorEvent:      "Elicitation",
		Scope:            detect.ScopeRoot,
		VendorSessionRef: "vendor-session",
		Confidence:       1,
		ReceivedAt:       time.Now().UTC(),
		DeliveryID:       "550e8400-e29b-41d4-a716-446655440000",
	})
	if err != nil {
		t.Fatalf("new signal: %v", err)
	}
	observation, err := detect.ObserveSignal(signal)
	if err != nil {
		t.Fatalf("observe signal: %v", err)
	}
	decision, err := detector.Decide(
		detectorState.Snapshot(),
		a.Snapshot(),
		observation,
	)
	if err != nil {
		t.Fatalf("decide: %v", err)
	}

	st := &memoryCommitStore{}
	st.onAppend = func() {
		if a.State() != agent.StateWorking ||
			detectorState.Snapshot().HookStatus() != detect.HookAwaiting ||
			managed.vendorSessionReference() != "" {
			t.Errorf(
				"projections changed before append: Agent=%s Detector=%s ref=%q",
				a.State(),
				detectorState.Snapshot().HookStatus(),
				managed.vendorSessionReference(),
			)
		}
	}
	hub := event.NewHub(0)
	subscription := hub.Subscribe(2)
	defer hub.Unsubscribe(subscription)
	committer := newCommitter(0, st, hub)
	defer committer.Close()

	receipt, err := committer.CommitDecision(
		context.Background(),
		managed,
		&detectorState,
		decision,
		[]event.Draft{
			event.NewAgentSignalDraft("agent-1", "agent-1", `{"version":1}`),
		},
	)
	if err != nil {
		t.Fatalf("commit decision: %v", err)
	}
	if receipt.FirstSeq != 1 || receipt.LastSeq != 2 || receipt.Timestamp.IsZero() {
		t.Fatalf("receipt = %+v", receipt)
	}
	if a.State() != agent.StateBlocked ||
		detectorState.Snapshot().HookStatus() != detect.HookActive ||
		managed.vendorSessionReference() != "vendor-session" {
		t.Fatalf(
			"projections after commit: Agent=%s Detector=%s ref=%q",
			a.State(),
			detectorState.Snapshot().HookStatus(),
			managed.vendorSessionReference(),
		)
	}
	if got := managed.currentStateSeq(); got != 2 {
		t.Fatalf("state sequence = %d, want 2", got)
	}
	for want := uint64(1); want <= 2; want++ {
		select {
		case published := <-subscription.C():
			if published.Seq != want ||
				a.State() != agent.StateBlocked ||
				detectorState.Snapshot().HookStatus() != detect.HookActive ||
				managed.vendorSessionReference() != "vendor-session" {
				t.Fatalf(
					"published=%+v Agent=%s Detector=%s ref=%q",
					published,
					a.State(),
					detectorState.Snapshot().HookStatus(),
					managed.vendorSessionReference(),
				)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for sequence %d", want)
		}
	}
}

func TestDecisionCommitStoreFailureLeavesBothProjectionsUnchanged(t *testing.T) {
	a, err := agent.Restore(agent.RestoreSnapshot{
		ID:         "agent-1",
		Name:       "test",
		Vendor:     "claude",
		RunMode:    agent.RunModeInteractive,
		HookPolicy: agent.HooksAuto,
		State:      agent.StateWorking,
		CreatedAt:  time.Now().Add(-time.Minute),
		UpdatedAt:  time.Now(),
	})
	if err != nil {
		t.Fatalf("restore Agent: %v", err)
	}
	managed := newManagedAgent(a)
	detector, err := detect.New(detect.Config{})
	if err != nil {
		t.Fatalf("new Detector: %v", err)
	}
	detectorState := detect.NewState(agent.HooksAuto)
	signal, err := detect.NewHookSignal(detect.Signal{
		Kind:             detect.KindHumanInputRequired,
		Vendor:           "claude",
		VendorEvent:      "Elicitation",
		Scope:            detect.ScopeRoot,
		VendorSessionRef: "vendor-session",
		Confidence:       1,
		ReceivedAt:       time.Now().UTC(),
		DeliveryID:       "550e8400-e29b-41d4-a716-446655440000",
	})
	if err != nil {
		t.Fatalf("new signal: %v", err)
	}
	observation, err := detect.ObserveSignal(signal)
	if err != nil {
		t.Fatalf("observe signal: %v", err)
	}
	decision, err := detector.Decide(
		detectorState.Snapshot(),
		a.Snapshot(),
		observation,
	)
	if err != nil {
		t.Fatalf("decide: %v", err)
	}

	storageErr := errors.New("disk unavailable")
	committer := newCommitter(
		0,
		&memoryCommitStore{appendErr: storageErr},
		event.NewHub(0),
	)
	defer committer.Close()
	_, err = committer.CommitDecision(
		context.Background(),
		managed,
		&detectorState,
		decision,
		[]event.Draft{
			event.NewAgentSignalDraft("agent-1", "agent-1", `{"version":1}`),
		},
	)
	if !errors.Is(err, storageErr) {
		t.Fatalf("commit error = %v, want storage error", err)
	}
	if a.State() != agent.StateWorking ||
		detectorState.Snapshot().HookStatus() != detect.HookAwaiting ||
		managed.vendorSessionReference() != "" {
		t.Fatalf(
			"projections changed after append failure: Agent=%s Detector=%s ref=%q",
			a.State(),
			detectorState.Snapshot().HookStatus(),
			managed.vendorSessionReference(),
		)
	}
	if got := managed.currentStateSeq(); got != 0 {
		t.Fatalf("state sequence = %d after failed decision, want 0", got)
	}
}

func TestTypedCommitterPoisonsAfterPostCommitInvariantFailure(t *testing.T) {
	a := agent.New("agent-1")
	conflicting, err := a.Prepare(agent.MoveTo(
		agent.StateStarting,
		"conflict",
		agent.Evidence{
			Source: agent.EvidenceSession, Event: "session_start", Confidence: 1,
		},
	))
	if err != nil {
		t.Fatalf("prepare conflict: %v", err)
	}
	st := &memoryCommitStore{}
	st.onAppend = func() {
		if applyErr := a.ApplyCommitted(conflicting); applyErr != nil {
			t.Errorf("apply conflict: %v", applyErr)
		}
	}
	hub := event.NewHub(0)
	committer := newCommitter(0, st, hub)
	defer committer.Close()
	managed := newManagedAgent(a)

	_, err = committer.CommitAgent(
		context.Background(),
		managed,
		agent.MoveTo(agent.StateStarting, "session start", agent.Evidence{
			Source: agent.EvidenceSession, Event: "session_start", Confidence: 1,
		}),
		nil,
	)
	if !errors.Is(err, agent.ErrStaleTransitionPlan) {
		t.Fatalf("commit error = %v, want stale prepared change", err)
	}
	if len(st.Rows()) != 1 {
		t.Fatalf("stored rows = %+v, want committed state row", st.Rows())
	}
	if hub.LastSeq() != 0 {
		t.Fatalf("hub sequence = %d, want no publication", hub.LastSeq())
	}
	select {
	case fatalErr := <-committer.Fatal():
		if !errors.Is(fatalErr, agent.ErrStaleTransitionPlan) {
			t.Fatalf("fatal error = %v", fatalErr)
		}
	case <-time.After(time.Second):
		t.Fatal("committer did not report post-commit invariant failure")
	}
}

func TestTypedCommitterPoisonsAfterPublishFailure(t *testing.T) {
	st := &memoryCommitStore{}
	hub := event.NewHub(0)
	hub.Close()
	committer := newCommitter(0, st, hub)
	defer committer.Close()

	receipt, err := committer.CommitEvents(
		context.Background(),
		[]event.Draft{event.NewOutputDraft("agent-1", "agent-1", "line")},
	)
	if !errors.Is(err, event.ErrHubClosed) {
		t.Fatalf("commit error = %v, want closed Hub", err)
	}
	if !receipt.Durable || receipt.FirstSeq != 1 || receipt.LastSeq != 1 {
		t.Fatalf("receipt = %+v, want durable sequence 1", receipt)
	}
	rows := st.Rows()
	if len(rows) != 1 || rows[0].Seq != 1 {
		t.Fatalf("stored rows = %+v, want durable sequence 1", rows)
	}
	_, err = committer.CommitEvents(
		context.Background(),
		[]event.Draft{event.NewOutputDraft("agent-1", "agent-1", "later")},
	)
	if !errors.Is(err, errCommitterFailed) {
		t.Fatalf("second commit error = %v, want failed committer", err)
	}
}

func TestCommitterSerializesConcurrentProducers(t *testing.T) {
	const producerCount = 100

	st := &memoryCommitStore{}
	hub := event.NewHub(0)
	subscription := hub.Subscribe(producerCount)
	defer hub.Unsubscribe(subscription)
	committer := newCommitter(0, st, hub)
	defer committer.Close()

	sequences := make(chan uint64, producerCount)
	errorsCh := make(chan error, producerCount)
	var producers sync.WaitGroup
	for i := 0; i < producerCount; i++ {
		producers.Add(1)
		go func(index int) {
			defer producers.Done()
			receipt, err := committer.CommitEvents(
				context.Background(),
				[]event.Draft{
					event.NewOutputDraft("session", "session", fmt.Sprintf("line-%d", index)),
				},
			)
			if err != nil {
				errorsCh <- err
				return
			}
			sequences <- receipt.FirstSeq
		}(i)
	}
	producers.Wait()
	close(errorsCh)
	close(sequences)

	for err := range errorsCh {
		t.Errorf("commit: %v", err)
	}
	seen := make([]bool, producerCount+1)
	for seq := range sequences {
		if seq == 0 || seq > producerCount || seen[seq] {
			t.Fatalf("invalid or duplicate committed sequence %d", seq)
		}
		seen[seq] = true
	}
	rows := st.Rows()
	if len(rows) != producerCount {
		t.Fatalf("stored rows = %d, want %d", len(rows), producerCount)
	}
	for i, row := range rows {
		if row.Seq != uint64(i+1) {
			t.Fatalf("stored sequence at %d = %d, want %d", i, row.Seq, i+1)
		}
	}
	for seq := uint64(1); seq <= producerCount; seq++ {
		select {
		case got := <-subscription.C():
			if got.Seq != seq {
				t.Fatalf("published sequence = %d, want %d", got.Seq, seq)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for published sequence %d", seq)
		}
	}
	if hub.LastSeq() != producerCount {
		t.Fatalf("hub last sequence = %d, want %d", hub.LastSeq(), producerCount)
	}
}

func TestCommitterReturnsAcceptedResultDuringClose(t *testing.T) {
	for iteration := range 20 {
		st := &blockingCommitStore{
			started: make(chan struct{}),
			release: make(chan struct{}),
		}
		committer := newCommitter(0, st, event.NewHub(0))
		result := make(chan error, 1)
		go func() {
			_, err := committer.CommitEvents(
				context.Background(),
				[]event.Draft{event.NewOutputDraft("session", "session", "line")},
			)
			result <- err
		}()

		<-st.started
		closed := make(chan struct{})
		go func() {
			committer.Close()
			close(closed)
		}()
		time.Sleep(time.Millisecond)
		close(st.release)

		if err := <-result; err != nil {
			t.Fatalf("iteration %d accepted commit error = %v", iteration, err)
		}
		<-closed
		if rows := st.Rows(); len(rows) != 1 {
			t.Fatalf("iteration %d rows = %+v, want durable commit", iteration, rows)
		}
	}
}

func TestCommitterStoreFailureDoesNotApplyOrPublish(t *testing.T) {
	storageErr := errors.New("disk unavailable")
	st := &memoryCommitStore{appendErr: storageErr}
	hub := event.NewHub(0)
	subscription := hub.Subscribe(1)
	defer hub.Unsubscribe(subscription)
	committer := newCommitter(0, st, hub)
	defer committer.Close()

	_, err := committer.CommitEvents(
		context.Background(),
		[]event.Draft{event.NewOutputDraft("session", "session", "line")},
	)
	if !errors.Is(err, storageErr) {
		t.Fatalf("commit error = %v, want storage error", err)
	}
	if rows := st.Rows(); len(rows) != 0 {
		t.Fatalf("stored rows = %+v, want none", rows)
	}
	if hub.LastSeq() != 0 {
		t.Fatalf("hub last sequence = %d, want 0", hub.LastSeq())
	}
	select {
	case published := <-subscription.C():
		t.Fatalf("published event after storage failure: %+v", published)
	default:
	}
	select {
	case fatalErr := <-committer.Fatal():
		if !errors.Is(fatalErr, storageErr) {
			t.Fatalf("fatal error = %v, want storage error", fatalErr)
		}
	case <-time.After(time.Second):
		t.Fatal("committer did not report fatal storage failure")
	}

	_, err = committer.CommitEvents(
		context.Background(),
		[]event.Draft{event.NewOutputDraft("session", "session", "later")},
	)
	if !errors.Is(err, errCommitterFailed) {
		t.Fatalf("second commit error = %v, want errCommitterFailed", err)
	}
}

func TestCommitterRejectsInvalidAgentChangeWithoutFailing(t *testing.T) {
	st := &memoryCommitStore{}
	hub := event.NewHub(0)
	committer := newCommitter(0, st, hub)
	defer committer.Close()
	a := agent.New("agent-1")
	managed := newManagedAgent(a)

	_, err := committer.CommitAgent(
		context.Background(),
		managed,
		agent.MoveTo(agent.StateDone, "invalid", agent.Evidence{
			Source:     agent.EvidenceSession,
			Event:      "invalid",
			Confidence: 1,
		}),
		nil,
	)
	if !errors.Is(err, agent.ErrInvalidTransition) {
		t.Fatalf("validation error = %v, want invalid transition", err)
	}
	select {
	case fatalErr := <-committer.Fatal():
		t.Fatalf("validation failure became fatal: %v", fatalErr)
	default:
	}

	receipt, err := committer.CommitEvents(
		context.Background(),
		[]event.Draft{event.NewOutputDraft("session", "session", "still healthy")},
	)
	if err != nil {
		t.Fatalf("commit after validation rejection: %v", err)
	}
	if receipt.FirstSeq != 1 || receipt.LastSeq != 1 || receipt.Timestamp.IsZero() {
		t.Fatalf("receipt = %+v, want sequence 1", receipt)
	}
}

func TestCommitterClockAdvancesOnlyAfterDurableAppend(t *testing.T) {
	st := &blockingCommitStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	committer := newCommitter(0, st, event.NewHub(0))
	defer committer.Close()

	result := make(chan error, 1)
	go func() {
		_, err := committer.CommitEvents(
			context.Background(),
			[]event.Draft{event.NewOutputDraft("session", "session", "durable")},
		)
		result <- err
	}()

	select {
	case <-st.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for append")
	}
	if high := committer.HighWatermark(); high != 0 {
		t.Fatalf("high watermark before append commit = %d, want 0", high)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := committer.WaitForCommit(waitCtx, 0); !errors.Is(
		err,
		context.DeadlineExceeded,
	) {
		t.Fatalf("wait before durable append error = %v, want deadline", err)
	}

	close(st.release)
	if err := <-result; err != nil {
		t.Fatalf("commit events: %v", err)
	}
	high, err := committer.WaitForCommit(context.Background(), 0)
	if err != nil {
		t.Fatalf("wait after durable append: %v", err)
	}
	if high != 1 || committer.HighWatermark() != 1 {
		t.Fatalf("clock high watermark = %d / %d, want 1", high, committer.HighWatermark())
	}
}

func TestCommitClockDoesNotMissRegistrationRace(t *testing.T) {
	for iteration := 0; iteration < 200; iteration++ {
		clock := newCommitClock(0)
		result := make(chan error, 1)
		go func() {
			high, err := clock.Wait(context.Background(), 0)
			if err == nil && high != 1 {
				err = fmt.Errorf("high watermark = %d, want 1", high)
			}
			result <- err
		}()
		clock.advance(1)
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("iteration %d: %v", iteration, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("iteration %d missed commit wakeup", iteration)
		}
	}
}

func TestCommitterClockClosesWaitersOnCloseAndFailure(t *testing.T) {
	t.Run("close", func(t *testing.T) {
		committer := newCommitter(7, &memoryCommitStore{lastSeq: 7}, event.NewHub(0))
		result := make(chan error, 1)
		go func() {
			_, err := committer.WaitForCommit(context.Background(), 7)
			result <- err
		}()
		committer.Close()
		if err := <-result; !errors.Is(err, errCommitterClosed) {
			t.Fatalf("close waiter error = %v, want errCommitterClosed", err)
		}
	})

	t.Run("failure", func(t *testing.T) {
		st := &memoryCommitStore{appendErr: errors.New("disk unavailable")}
		committer := newCommitter(0, st, event.NewHub(0))
		defer committer.Close()
		result := make(chan error, 1)
		go func() {
			_, err := committer.WaitForCommit(context.Background(), 0)
			result <- err
		}()
		if _, err := committer.CommitEvents(
			context.Background(),
			[]event.Draft{event.NewOutputDraft("session", "session", "failed")},
		); err == nil {
			t.Fatal("commit unexpectedly succeeded")
		}
		if err := <-result; !errors.Is(err, errCommitterFailed) {
			t.Fatalf("failure waiter error = %v, want errCommitterFailed", err)
		}
	})
}

type memoryCommitStore struct {
	mu        sync.Mutex
	lastSeq   uint64
	rows      []store.EventRow
	appendErr error
	onAppend  func()
}

func (s *memoryCommitStore) AppendEvents(
	_ context.Context,
	expectedLastSeq uint64,
	rows []store.EventRow,
) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appendErr != nil {
		return s.lastSeq, s.appendErr
	}
	if expectedLastSeq != s.lastSeq {
		return s.lastSeq, fmt.Errorf("stale boundary %d, want %d", expectedLastSeq, s.lastSeq)
	}
	if s.onAppend != nil {
		s.onAppend()
	}
	for i, row := range rows {
		wantSeq := s.lastSeq + uint64(i) + 1
		if row.Seq != wantSeq {
			return s.lastSeq, fmt.Errorf("sequence %d, want %d", row.Seq, wantSeq)
		}
	}
	s.rows = append(s.rows, rows...)
	s.lastSeq += uint64(len(rows))
	return s.lastSeq, nil
}

func (s *memoryCommitStore) Replay(sessionID string) ([]store.EventRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []store.EventRow
	for _, row := range s.rows {
		if row.SessionID == sessionID {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (s *memoryCommitStore) Rows() []store.EventRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.EventRow(nil), s.rows...)
}

type blockingCommitStore struct {
	memoryCommitStore
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingCommitStore) AppendEvents(
	ctx context.Context,
	expectedLastSeq uint64,
	rows []store.EventRow,
) (uint64, error) {
	s.once.Do(func() { close(s.started) })
	<-s.release
	return s.memoryCommitStore.AppendEvents(ctx, expectedLastSeq, rows)
}
