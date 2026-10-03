package session

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
)

var (
	errCommitterClosed = errors.New("session: event committer closed")
	errCommitterFailed = errors.New("session: event committer failed")
)

type commitStore interface {
	AppendEvents(context.Context, uint64, []store.EventRow) (uint64, error)
	Replay(string) ([]store.EventRow, error)
}

type projectionChange struct {
	validate func() error
	apply    func([]event.Event) error
}

type commitRequest struct {
	drafts []event.Event
	change projectionChange
	result chan commitResult
}

type commitResult struct {
	events []event.Event
	err    error
}

// committer 是运行时事件序号、持久化、投影和发布的单一写入者。
type committer struct {
	store commitStore
	hub   *event.Hub

	requests chan commitRequest
	stop     chan struct{}
	done     chan struct{}
	fatal    chan error
	close    sync.Once
}

func newCommitter(initialSeq uint64, st commitStore, hub *event.Hub) *committer {
	c := &committer{
		store:    st,
		hub:      hub,
		requests: make(chan commitRequest),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		fatal:    make(chan error, 1),
	}
	go c.run(initialSeq)
	return c
}

func (c *committer) Commit(
	ctx context.Context,
	drafts []event.Event,
	change projectionChange,
) ([]event.Event, error) {
	if len(drafts) == 0 {
		return nil, errors.New("session: commit requires at least one event")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("session: enqueue event commit: %w", err)
	}
	request := commitRequest{
		drafts: append([]event.Event(nil), drafts...),
		change: change,
		result: make(chan commitResult, 1),
	}
	select {
	case c.requests <- request:
	case <-ctx.Done():
		return nil, fmt.Errorf("session: enqueue event commit: %w", ctx.Err())
	case <-c.done:
		return nil, errCommitterClosed
	}

	select {
	case result := <-request.result:
		return result.events, result.err
	case <-c.done:
		return nil, errCommitterClosed
	}
}

func (c *committer) Fatal() <-chan error {
	return c.fatal
}

func (c *committer) Close() {
	c.close.Do(func() {
		close(c.stop)
		<-c.done
	})
}

func (c *committer) run(lastSeq uint64) {
	defer close(c.done)

	var fatalErr error
	for {
		select {
		case request := <-c.requests:
			if fatalErr != nil {
				request.result <- commitResult{err: fmt.Errorf("%w: %v", errCommitterFailed, fatalErr)}
				continue
			}
			events, err := c.commit(lastSeq, request)
			if err != nil {
				var rejected agentPlanRejectedError
				if !errors.As(err, &rejected) {
					fatalErr = err
					select {
					case c.fatal <- err:
					default:
					}
				}
				request.result <- commitResult{err: err}
				continue
			}
			lastSeq += uint64(len(events))
			request.result <- commitResult{events: events}
		case <-c.stop:
			return
		}
	}
}

// agentPlanRejectedError marks a pre-persistence validation failure.
type agentPlanRejectedError struct {
	err error
}

func (e agentPlanRejectedError) Error() string {
	return e.err.Error()
}

func (e agentPlanRejectedError) Unwrap() error {
	return e.err
}

func (c *committer) commit(lastSeq uint64, request commitRequest) ([]event.Event, error) {
	if request.change.validate != nil {
		if err := request.change.validate(); err != nil {
			return nil, agentPlanRejectedError{err: err}
		}
	}

	committed := append([]event.Event(nil), request.drafts...)
	rows := make([]store.EventRow, len(committed))
	for i := range committed {
		committed[i].Seq = lastSeq + uint64(i) + 1
		rows[i] = eventRow(committed[i])
	}
	newLastSeq, err := c.store.AppendEvents(context.Background(), lastSeq, rows)
	if err != nil {
		return nil, fmt.Errorf("session: append event batch after seq %d: %w", lastSeq, err)
	}
	wantLastSeq := lastSeq + uint64(len(committed))
	if newLastSeq != wantLastSeq {
		return nil, fmt.Errorf(
			"session: store returned last seq %d, want %d",
			newLastSeq,
			wantLastSeq,
		)
	}
	if request.change.apply != nil {
		if err := request.change.apply(committed); err != nil {
			return nil, fmt.Errorf("session: apply committed projection at seq %d: %w", newLastSeq, err)
		}
	}
	for _, committedEvent := range committed {
		if err := c.hub.Publish(committedEvent); err != nil {
			return nil, fmt.Errorf("session: publish committed event seq %d: %w", committedEvent.Seq, err)
		}
	}
	return committed, nil
}

func eventRow(ev event.Event) store.EventRow {
	return store.EventRow{
		Seq:       ev.Seq,
		Timestamp: ev.Timestamp,
		Type:      string(ev.Type),
		SessionID: ev.SessionID,
		AgentID:   ev.AgentID,
		From:      ev.From,
		To:        ev.To,
		Reason:    ev.Reason,
		Payload:   ev.Payload,
	}
}
