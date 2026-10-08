package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/pty"
	"github.com/Duang777/drove/internal/term"
)

var (
	// ErrActionStale means the requested Blocked occurrence is no longer current.
	ErrActionStale = errors.New("session: action state sequence is stale")
	// ErrActionUnavailable means the current session or screen cannot accept the action.
	ErrActionUnavailable = errors.New("session: action is unavailable")
	// ErrActionAlreadyAnswered means another response fenced this Blocked occurrence.
	ErrActionAlreadyAnswered = errors.New("session: action was already answered")
	// ErrActionBackpressure means the per-session control gate or PTY is busy.
	ErrActionBackpressure = errors.New("session: action backpressure")
	// ErrActionWrite means the PTY did not accept the complete action.
	ErrActionWrite = errors.New("session: action write failed")
	// ErrActionAudit means the action reached the PTY but its audit batch failed.
	ErrActionAudit = errors.New("session: action delivered but audit failed")
)

// ActionRequest describes one stale-safe response to a live approval prompt.
type ActionRequest struct {
	ExpectedStateSeq StateSeq
	Kind             agent.ActionKind
	Reply            string
	Channel          string
	DeviceID         string
}

// ActionResult reports the exact write and adjacent audit event sequences.
type ActionResult struct {
	StateSeq     StateSeq
	BytesWritten int
	ActionSeq    uint64
	InputSeq     uint64
}

// ActionContext describes actions available for one live Blocked occurrence.
type ActionContext struct {
	StateSeq StateSeq           `json:"state_seq"`
	Actions  []agent.ActionKind `json:"actions"`
	Screen   *ExplainScreen     `json:"screen,omitempty"`
}

type actionRuntime struct {
	managed    *managedAgent
	running    *runningSession
	output     *outputProcessor
	terminal   *terminalActor
	classifier *adapter.ScreenClassifier
}

// ActionContext returns a bounded current screen and its available actions.
func (m *Manager) ActionContext(
	ctx context.Context,
	id agent.ID,
	expected StateSeq,
) (ActionContext, error) {
	if expected == 0 {
		return ActionContext{}, ErrActionStale
	}
	runtime, err := m.actionRuntime(id)
	if err != nil {
		return ActionContext{}, err
	}
	if err := runtime.output.Barrier(ctx); err != nil {
		return ActionContext{}, fmt.Errorf("%w: output barrier: %w", ErrActionUnavailable, err)
	}

	result := ActionContext{StateSeq: expected}
	err = runtime.terminal.WithCurrentSnapshot(
		ctx,
		func(snapshot term.Snapshot, capturedAt time.Time) error {
			actions := runtime.classifier.AvailableApprovalActions(snapshot)
			if len(actions) == 0 {
				return ErrActionUnavailable
			}
			if !runtime.running.controlMu.TryLock() {
				return ErrActionBackpressure
			}
			defer runtime.running.controlMu.Unlock()
			if _, stateErr := m.validateActionStateLocked(
				id,
				runtime,
				expected,
			); stateErr != nil {
				return stateErr
			}
			screen, screenErr := explainScreen(snapshot, capturedAt)
			if screenErr != nil {
				return fmt.Errorf("session: render action screen: %w", screenErr)
			}
			result.Actions = append([]agent.ActionKind(nil), actions...)
			result.Screen = &screen
			return nil
		},
	)
	if err != nil {
		if errors.Is(err, errTerminalSnapshotUnavailable) ||
			errors.Is(err, errTerminalActorClosed) {
			return ActionContext{}, ErrActionUnavailable
		}
		return ActionContext{}, err
	}
	return result, nil
}

// Respond validates and writes one action, then commits its redacted audit batch.
func (m *Manager) Respond(
	ctx context.Context,
	id agent.ID,
	request ActionRequest,
) (ActionResult, error) {
	if request.ExpectedStateSeq == 0 {
		return ActionResult{}, ErrActionStale
	}
	runtime, err := m.actionRuntime(id)
	if err != nil {
		return ActionResult{}, err
	}
	if err := runtime.output.Barrier(ctx); err != nil {
		return ActionResult{}, fmt.Errorf("%w: output barrier: %w", ErrActionUnavailable, err)
	}

	result := ActionResult{StateSeq: request.ExpectedStateSeq}
	err = runtime.terminal.WithCurrentSnapshot(
		ctx,
		func(snapshot term.Snapshot, _ time.Time) error {
			plan, planErr := runtime.classifier.PlanApprovalAction(
				snapshot,
				request.Kind,
				request.Reply,
			)
			if planErr != nil {
				if errors.Is(planErr, adapter.ErrApprovalPromptUnavailable) {
					return ErrActionUnavailable
				}
				return planErr
			}
			input := plan.Bytes()
			inputPayload, inputErr := validateInput(input)
			if inputErr != nil {
				return fmt.Errorf("session: validate adapter action input: %w", inputErr)
			}
			actionDraft, draftErr := event.NewAgentActionDraft(
				string(id),
				string(id),
				event.AgentActionPayloadV1{
					Version:    event.AgentActionPayloadVersion,
					Action:     string(plan.Kind()),
					Channel:    request.Channel,
					DeviceID:   request.DeviceID,
					BlockedSeq: request.ExpectedStateSeq.String(),
					ReplyBytes: plan.ReplyBytes(),
					PromptRule: plan.Rule(),
				},
			)
			if draftErr != nil {
				return fmt.Errorf("session: create action audit: %w", draftErr)
			}
			inputDraft := event.NewAgentInputDraft(
				string(id),
				string(id),
				inputPayload,
			)

			if !runtime.running.controlMu.TryLock() {
				return ErrActionBackpressure
			}
			defer runtime.running.controlMu.Unlock()
			process, stateErr := m.validateActionStateLocked(
				id,
				runtime,
				request.ExpectedStateSeq,
			)
			if stateErr != nil {
				return stateErr
			}

			written, writeErr := process.Write(input)
			result.BytesWritten = written
			if written > 0 {
				runtime.running.responseFence = request.ExpectedStateSeq
			}
			if actionErr := classifyActionWrite(
				id,
				written,
				len(input),
				writeErr,
			); actionErr != nil {
				return actionErr
			}
			receipt, commitErr := m.committer.CommitEvents(
				context.Background(),
				[]event.Draft{actionDraft, inputDraft},
			)
			if commitErr != nil {
				return fmt.Errorf(
					"%w: agent %q received %d bytes; do not retry: %w",
					ErrActionAudit,
					id,
					written,
					commitErr,
				)
			}
			result.ActionSeq = receipt.FirstSeq
			result.InputSeq = receipt.LastSeq
			return nil
		},
	)
	if err != nil {
		if errors.Is(err, errTerminalSnapshotUnavailable) ||
			errors.Is(err, errTerminalActorClosed) {
			return result, ErrActionUnavailable
		}
		return result, err
	}
	return result, nil
}

func (m *Manager) actionRuntime(id agent.ID) (actionRuntime, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return actionRuntime{}, ErrManagerClosed
	}
	managed, known := m.agents[id]
	if !known {
		return actionRuntime{}, fmt.Errorf("%w: %q", ErrUnknownAgent, id)
	}
	running, attached := m.sessions[id]
	if !attached || running.output == nil || running.terminal == nil {
		return actionRuntime{}, fmt.Errorf("%w: %q", ErrNotAttached, id)
	}
	return actionRuntime{
		managed:    managed,
		running:    running,
		output:     running.output,
		terminal:   running.terminal,
		classifier: running.classifier,
	}, nil
}

func (m *Manager) validateActionStateLocked(
	id agent.ID,
	runtime actionRuntime,
	expected StateSeq,
) (processSession, error) {
	m.mu.RLock()
	current, attached := m.sessions[id]
	process := runtime.running.process
	unavailable := m.closed ||
		!attached ||
		current != runtime.running ||
		runtime.running.exitClaimed ||
		runtime.running.stopCause != stopCauseNone ||
		process == nil
	m.mu.RUnlock()
	if unavailable {
		return nil, fmt.Errorf("%w: %q", ErrNotAttached, id)
	}

	state := runtime.managed.stateView()
	if state.stateSeq != expected {
		return nil, fmt.Errorf(
			"%w: agent %q expected %s",
			ErrActionStale,
			id,
			expected,
		)
	}
	if state.state != agent.StateBlocked {
		return nil, fmt.Errorf("%w: agent %q is %s", ErrActionUnavailable, id, state.state)
	}
	if runtime.running.responseFence == expected {
		return nil, fmt.Errorf("%w: agent %q", ErrActionAlreadyAnswered, id)
	}
	return process, nil
}

func classifyActionWrite(
	id agent.ID,
	written int,
	total int,
	err error,
) error {
	if err == nil && written == total {
		return nil
	}
	if err == nil {
		err = io.ErrShortWrite
	}
	if written > 0 {
		return fmt.Errorf(
			"%w: agent %q wrote %d/%d bytes; do not retry: %w",
			ErrActionWrite,
			id,
			written,
			total,
			err,
		)
	}
	if errors.Is(err, pty.ErrClosed) {
		return fmt.Errorf("%w: %q", ErrNotAttached, id)
	}
	if errors.Is(err, pty.ErrWriteBackpressure) {
		return fmt.Errorf(
			"%w: agent %q wrote 0/%d bytes: %w",
			ErrActionBackpressure,
			id,
			total,
			err,
		)
	}
	return fmt.Errorf(
		"%w: agent %q wrote 0/%d bytes: %w",
		ErrActionWrite,
		id,
		total,
		err,
	)
}
