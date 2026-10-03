package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
)

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

	receipt, err := committer.CommitAgent(
		context.Background(),
		a,
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
	if receipt != (commitReceipt{FirstSeq: 1, LastSeq: 2}) {
		t.Fatalf("receipt = %+v", receipt)
	}
	if a.State() != agent.StateStarting ||
		a.LastTransition() == nil ||
		a.LastTransition().Event != "session_start" {
		t.Fatalf("agent projection = state %s evidence %+v", a.State(), a.LastTransition())
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

	_, err := committer.CommitAgent(
		context.Background(),
		a,
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

	_, err = committer.CommitAgent(
		context.Background(),
		a,
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

	_, err := committer.CommitEvents(
		context.Background(),
		[]event.Draft{event.NewOutputDraft("agent-1", "agent-1", "line")},
	)
	if !errors.Is(err, event.ErrHubClosed) {
		t.Fatalf("commit error = %v, want closed Hub", err)
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

	_, err := committer.CommitAgent(
		context.Background(),
		a,
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
	if receipt != (commitReceipt{FirstSeq: 1, LastSeq: 1}) {
		t.Fatalf("receipt = %+v, want sequence 1", receipt)
	}
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
