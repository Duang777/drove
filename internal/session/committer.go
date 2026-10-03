package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
)

const commitQueueSize = 256

var (
	errCommitterClosed = errors.New("session: event committer closed")
	errCommitterFailed = errors.New("session: event committer failed")
)

type commitStore interface {
	AppendEvents(context.Context, uint64, []store.EventRow) (uint64, error)
	Replay(string) ([]store.EventRow, error)
}

type commitOperation interface {
	isCommitOperation()
}

type eventsOperation struct {
	drafts []event.Draft
}

func (eventsOperation) isCommitOperation() {}

type agentOperation struct {
	agent  *agent.Agent
	change agent.Change
	drafts []event.Draft
}

func (agentOperation) isCommitOperation() {}

type decisionOperation struct {
	agent    *agent.Agent
	state    *detect.State
	decision detect.Decision
	drafts   []event.Draft
}

func (decisionOperation) isCommitOperation() {}

type commitRequest struct {
	operation commitOperation
	result    chan commitResult
}

type commitResult struct {
	receipt commitReceipt
	err     error
}

type commitReceipt struct {
	FirstSeq uint64
	LastSeq  uint64
}

func newEventsOperation(drafts []event.Draft) eventsOperation {
	return eventsOperation{drafts: append([]event.Draft(nil), drafts...)}
}

func newAgentOperation(
	target *agent.Agent,
	change agent.Change,
	drafts []event.Draft,
) agentOperation {
	return agentOperation{
		agent:  target,
		change: change,
		drafts: append([]event.Draft(nil), drafts...),
	}
}

func newDecisionOperation(
	target *agent.Agent,
	state *detect.State,
	decision detect.Decision,
	drafts []event.Draft,
) decisionOperation {
	return decisionOperation{
		agent:    target,
		state:    state,
		decision: decision,
		drafts:   append([]event.Draft(nil), drafts...),
	}
}

// committer is the only runtime owner of event sequences and durable writes.
type committer struct {
	store commitStore
	hub   *event.Hub

	requests chan commitRequest
	stop     chan struct{}
	done     chan struct{}
	fatal    chan error

	admissionMu sync.RWMutex
	stateMu     sync.RWMutex
	closing     bool
	failure     error
	closeOnce   sync.Once
}

func newCommitter(initialSeq uint64, st commitStore, hub *event.Hub) *committer {
	c := &committer{
		store:    st,
		hub:      hub,
		requests: make(chan commitRequest, commitQueueSize),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		fatal:    make(chan error, 1),
	}
	go c.run(initialSeq)
	return c
}

func (c *committer) CommitEvents(
	ctx context.Context,
	drafts []event.Draft,
) (commitReceipt, error) {
	if len(drafts) == 0 {
		return commitReceipt{}, errors.New("session: commit requires at least one event")
	}
	result, err := c.submit(ctx, newEventsOperation(drafts))
	return result.receipt, err
}

func (c *committer) CommitAgent(
	ctx context.Context,
	target *agent.Agent,
	change agent.Change,
	drafts []event.Draft,
) (commitReceipt, error) {
	if target == nil {
		return commitReceipt{}, errors.New("session: agent commit requires an Agent")
	}
	result, err := c.submit(ctx, newAgentOperation(target, change, drafts))
	return result.receipt, err
}

func (c *committer) CommitDecision(
	ctx context.Context,
	target *agent.Agent,
	state *detect.State,
	decision detect.Decision,
	drafts []event.Draft,
) (commitReceipt, error) {
	if target == nil || state == nil {
		return commitReceipt{}, errors.New(
			"session: decision commit requires Agent and Detector state",
		)
	}
	if decision.Duplicate() {
		return commitReceipt{}, errors.New("session: duplicate decision cannot be committed")
	}
	result, err := c.submit(
		ctx,
		newDecisionOperation(target, state, decision, drafts),
	)
	return result.receipt, err
}

func (c *committer) submit(ctx context.Context, operation commitOperation) (commitResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	request := commitRequest{
		operation: operation,
		result:    make(chan commitResult, 1),
	}

	if failure := c.rootFailure(); failure != nil {
		err := fmt.Errorf("%w: %v", errCommitterFailed, failure)
		return commitResult{}, err
	}
	c.admissionMu.RLock()
	if c.closing {
		c.admissionMu.RUnlock()
		return commitResult{}, errCommitterClosed
	}
	select {
	case c.requests <- request:
		c.admissionMu.RUnlock()
	case <-ctx.Done():
		c.admissionMu.RUnlock()
		return commitResult{}, fmt.Errorf("session: enqueue event commit: %w", ctx.Err())
	}

	result := <-request.result
	return result, result.err
}

func (c *committer) Fatal() <-chan error {
	return c.fatal
}

func (c *committer) Close() {
	c.closeOnce.Do(func() {
		c.admissionMu.Lock()
		c.closing = true
		close(c.stop)
		c.admissionMu.Unlock()
		<-c.done
	})
}

func (c *committer) run(lastSeq uint64) {
	defer close(c.done)
	for {
		select {
		case request := <-c.requests:
			if failure := c.rootFailure(); failure != nil {
				request.result <- commitResult{
					err: fmt.Errorf("%w: %v", errCommitterFailed, failure),
				}
				continue
			}
			newLastSeq, result := c.execute(lastSeq, request.operation)
			if newLastSeq > lastSeq {
				lastSeq = newLastSeq
			}
			request.result <- result
			if result.err != nil {
				var rejected agentPlanRejectedError
				if !errors.As(result.err, &rejected) {
					c.poison(result.err)
					c.rejectQueued(result.err)
				}
			}
		case <-c.stop:
			c.drain(lastSeq)
			return
		}
	}
}

func (c *committer) drain(lastSeq uint64) {
	for {
		select {
		case request := <-c.requests:
			failure := c.rootFailure()
			if failure != nil {
				request.result <- commitResult{
					err: fmt.Errorf("%w: %v", errCommitterFailed, failure),
				}
				continue
			}
			newLastSeq, result := c.execute(lastSeq, request.operation)
			if newLastSeq > lastSeq {
				lastSeq = newLastSeq
			}
			request.result <- result
			if result.err != nil {
				var rejected agentPlanRejectedError
				if !errors.As(result.err, &rejected) {
					c.poison(result.err)
				}
			}
		default:
			return
		}
	}
}

func (c *committer) rejectQueued(root error) {
	for {
		select {
		case request := <-c.requests:
			request.result <- commitResult{
				err: fmt.Errorf("%w: %v", errCommitterFailed, root),
			}
		default:
			return
		}
	}
}

func (c *committer) poison(root error) {
	c.stateMu.Lock()
	if c.failure == nil {
		c.failure = root
		select {
		case c.fatal <- root:
		default:
		}
	}
	c.stateMu.Unlock()
}

func (c *committer) rootFailure() error {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.failure
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

func (c *committer) execute(
	lastSeq uint64,
	operation commitOperation,
) (uint64, commitResult) {
	drafts, apply, at, err := prepareCommitOperation(operation)
	if err != nil {
		return lastSeq, commitResult{err: agentPlanRejectedError{err: err}}
	}

	committed := make([]event.Event, len(drafts))
	for i := range drafts {
		committed[i], err = event.Commit(lastSeq+uint64(i)+1, at, drafts[i])
		if err != nil {
			return lastSeq, commitResult{err: agentPlanRejectedError{err: err}}
		}
	}

	rows := make([]store.EventRow, len(committed))
	for i := range committed {
		rows[i] = eventRow(committed[i])
	}
	newLastSeq, err := c.store.AppendEvents(context.Background(), lastSeq, rows)
	if err != nil {
		return lastSeq, commitResult{
			err: fmt.Errorf("session: append event batch after seq %d: %w", lastSeq, err),
		}
	}
	wantLastSeq := lastSeq + uint64(len(committed))
	if newLastSeq != wantLastSeq {
		return newLastSeq, commitResult{
			err: fmt.Errorf(
				"session: store returned last seq %d, want %d",
				newLastSeq,
				wantLastSeq,
			),
		}
	}
	if apply != nil {
		if err := apply(committed); err != nil {
			return newLastSeq, commitResult{
				err: fmt.Errorf(
					"session: apply committed projection at seq %d: %w",
					newLastSeq,
					err,
				),
			}
		}
	}
	if err := c.hub.PublishBatch(committed); err != nil {
		return newLastSeq, commitResult{
			err: fmt.Errorf("session: publish committed batch at seq %d: %w", newLastSeq, err),
		}
	}
	return newLastSeq, commitResult{
		receipt: commitReceipt{
			FirstSeq: committed[0].Seq,
			LastSeq:  committed[len(committed)-1].Seq,
		},
	}
}

func prepareCommitOperation(
	operation commitOperation,
) (
	drafts []event.Draft,
	apply func([]event.Event) error,
	at time.Time,
	err error,
) {
	switch typed := operation.(type) {
	case eventsOperation:
		if len(typed.drafts) == 0 {
			return nil, nil, time.Time{}, errors.New("session: empty events operation")
		}
		return append([]event.Draft(nil), typed.drafts...), nil, time.Now().UTC(), nil
	case agentOperation:
		prepared, prepareErr := typed.agent.Prepare(typed.change)
		if prepareErr != nil {
			return nil, nil, time.Time{}, prepareErr
		}
		drafts = append([]event.Draft(nil), typed.drafts...)
		if message, ok := prepared.ErrorMessage(); ok {
			drafts = append(drafts, event.NewErrorDraft(
				string(typed.agent.ID()),
				string(typed.agent.ID()),
				message,
			))
		}
		if from, to, reason, evidence, ok := prepared.Transition(); ok {
			payload, encodeErr := json.Marshal(event.StateEvidencePayloadV1{
				Version:    1,
				Source:     string(evidence.Source),
				Event:      evidence.Event,
				Confidence: evidence.Confidence,
				DeliveryID: evidence.DeliveryID,
			})
			if encodeErr != nil {
				return nil, nil, time.Time{}, fmt.Errorf(
					"session: encode state evidence: %w",
					encodeErr,
				)
			}
			drafts = append(drafts, event.NewStateChangedDraft(
				string(typed.agent.ID()),
				string(typed.agent.ID()),
				string(from),
				string(to),
				reason,
				string(payload),
			))
		}
		if len(drafts) == 0 {
			return nil, nil, time.Time{}, errors.New("session: empty agent operation")
		}
		return drafts, func([]event.Event) error {
			return typed.agent.ApplyCommitted(prepared)
		}, prepared.Timestamp(), nil
	case decisionOperation:
		signal, _, ok := typed.decision.Signal()
		if !ok {
			return nil, nil, time.Time{}, errors.New(
				"session: decision operation requires a signal",
			)
		}
		drafts = append([]event.Draft(nil), typed.drafts...)
		var prepared agent.PreparedChange
		change, hasChange := typed.decision.Change()
		if hasChange {
			prepared, err = typed.agent.Prepare(change)
			if err != nil {
				return nil, nil, time.Time{}, err
			}
			if message, hasError := prepared.ErrorMessage(); hasError {
				drafts = append(drafts, event.NewErrorDraft(
					string(typed.agent.ID()),
					string(typed.agent.ID()),
					message,
				))
			}
			if from, to, reason, evidence, hasTransition := prepared.Transition(); hasTransition {
				payload, encodeErr := json.Marshal(event.StateEvidencePayloadV1{
					Version:    1,
					Source:     string(evidence.Source),
					Event:      evidence.Event,
					Confidence: evidence.Confidence,
					DeliveryID: evidence.DeliveryID,
				})
				if encodeErr != nil {
					return nil, nil, time.Time{}, fmt.Errorf(
						"session: encode decision state evidence: %w",
						encodeErr,
					)
				}
				drafts = append(drafts, event.NewStateChangedDraft(
					string(typed.agent.ID()),
					string(typed.agent.ID()),
					string(from),
					string(to),
					reason,
					string(payload),
				))
			}
		}
		if len(drafts) == 0 {
			return nil, nil, time.Time{}, errors.New("session: empty decision operation")
		}
		return drafts, func([]event.Event) error {
			if hasChange {
				if applyErr := typed.agent.ApplyCommitted(prepared); applyErr != nil {
					return applyErr
				}
			}
			return typed.state.ApplyCommitted(typed.decision)
		}, signal.ReceivedAt, nil
	default:
		return nil, nil, time.Time{}, fmt.Errorf(
			"session: unknown commit operation %T",
			operation,
		)
	}
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
