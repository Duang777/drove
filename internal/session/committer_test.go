package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
)

func TestCommitterSerializesConcurrentProducers(t *testing.T) {
	const producerCount = 100

	st := &memoryCommitStore{}
	hub := event.NewHub(0)
	subscription := hub.Subscribe(producerCount)
	defer hub.Unsubscribe(subscription)
	committer := newCommitter(0, st, hub)
	defer committer.Close()

	var applied atomic.Int32
	sequences := make(chan uint64, producerCount)
	errorsCh := make(chan error, producerCount)
	var producers sync.WaitGroup
	for i := 0; i < producerCount; i++ {
		producers.Add(1)
		go func(index int) {
			defer producers.Done()
			events, err := committer.Commit(
				context.Background(),
				[]event.Event{event.NewOutput(0, "session", "agent", fmt.Sprintf("line-%d", index))},
				projectionChange{apply: func([]event.Event) error {
					applied.Add(1)
					return nil
				}},
			)
			if err != nil {
				errorsCh <- err
				return
			}
			sequences <- events[0].Seq
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
	if applied.Load() != producerCount {
		t.Fatalf("applied changes = %d, want %d", applied.Load(), producerCount)
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
			_, err := committer.Commit(
				context.Background(),
				[]event.Event{event.NewOutput(0, "session", "agent", "line")},
				projectionChange{},
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

	var applied atomic.Bool
	_, err := committer.Commit(
		context.Background(),
		[]event.Event{event.NewOutput(0, "session", "agent", "line")},
		projectionChange{apply: func([]event.Event) error {
			applied.Store(true)
			return nil
		}},
	)
	if !errors.Is(err, storageErr) {
		t.Fatalf("commit error = %v, want storage error", err)
	}
	if applied.Load() {
		t.Fatal("projection applied after storage failure")
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

	_, err = committer.Commit(
		context.Background(),
		[]event.Event{event.NewOutput(0, "session", "agent", "later")},
		projectionChange{},
	)
	if !errors.Is(err, errCommitterFailed) {
		t.Fatalf("second commit error = %v, want errCommitterFailed", err)
	}
}

func TestCommitterRejectsStalePlanWithoutFailing(t *testing.T) {
	st := &memoryCommitStore{}
	hub := event.NewHub(0)
	committer := newCommitter(0, st, hub)
	defer committer.Close()

	staleErr := errors.New("stale plan")
	_, err := committer.Commit(
		context.Background(),
		[]event.Event{event.NewStateChanged(0, "session", "agent", "working", "done", "done")},
		projectionChange{validate: func() error { return staleErr }},
	)
	if !errors.Is(err, staleErr) {
		t.Fatalf("validation error = %v, want stale plan", err)
	}
	select {
	case fatalErr := <-committer.Fatal():
		t.Fatalf("validation failure became fatal: %v", fatalErr)
	default:
	}

	events, err := committer.Commit(
		context.Background(),
		[]event.Event{event.NewOutput(0, "session", "agent", "still healthy")},
		projectionChange{},
	)
	if err != nil {
		t.Fatalf("commit after validation rejection: %v", err)
	}
	if len(events) != 1 || events[0].Seq != 1 {
		t.Fatalf("committed events = %+v, want sequence 1", events)
	}
}

type memoryCommitStore struct {
	mu        sync.Mutex
	lastSeq   uint64
	rows      []store.EventRow
	appendErr error
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
