package clitui

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Duang777/drove/internal/client"
)

var previewRetryDelays = [...]time.Duration{
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	2 * time.Second,
}

type previewTarget struct {
	AgentID    string
	Generation uint64
}

type previewEvent struct {
	Target   previewTarget
	Snapshot *client.TerminalSnapshot
	Err      error
	RetryIn  time.Duration
}

type previewController interface {
	Replace(previewTarget)
	Events() <-chan previewEvent
	Close() error
}

type previewStream interface {
	Subscribe(context.Context, client.TerminalSubscription) error
	Next(context.Context, func(client.TerminalMessage) error) error
	Close() error
}

type previewDependencies struct {
	open func(context.Context) (previewStream, error)
	wait func(context.Context, time.Duration) bool
}

type previewActor struct {
	ctx     context.Context
	cancel  context.CancelFunc
	targets chan previewTarget
	events  chan previewEvent
	done    chan struct{}

	closeOnce sync.Once
	closeErr  error
}

func newPreviewActor(
	ctx context.Context,
	open func(context.Context) (previewStream, error),
) *previewActor {
	return newPreviewActorWithDependencies(ctx, previewDependencies{
		open: open,
		wait: waitForPreviewRetry,
	})
}

func newPreviewActorWithDependencies(
	parent context.Context,
	deps previewDependencies,
) *previewActor {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	actor := &previewActor{
		ctx:     ctx,
		cancel:  cancel,
		targets: make(chan previewTarget, 1),
		events:  make(chan previewEvent, 1),
		done:    make(chan struct{}),
	}
	go actor.run(deps)
	return actor
}

func (a *previewActor) Replace(target previewTarget) {
	offerLatest(a.ctx, a.targets, target)
}

func (a *previewActor) Events() <-chan previewEvent {
	return a.events
}

func (a *previewActor) Close() error {
	a.closeOnce.Do(a.cancel)
	<-a.done
	return a.closeErr
}

func (a *previewActor) run(deps previewDependencies) {
	defer close(a.done)
	defer close(a.events)

	var (
		current previewTarget
		cancel  context.CancelFunc
		done    <-chan error
	)
	stopWorker := func() {
		if cancel == nil {
			return
		}
		cancel()
		a.closeErr = errors.Join(a.closeErr, <-done)
		cancel = nil
		done = nil
	}

	for {
		select {
		case <-a.ctx.Done():
			stopWorker()
			return
		case target := <-a.targets:
			if target == current {
				continue
			}
			stopWorker()
			target = newestPreviewTarget(target, a.targets)
			current = target
			if target.AgentID == "" {
				continue
			}

			workerCtx, workerCancel := context.WithCancel(a.ctx)
			workerDone := make(chan error, 1)
			cancel = workerCancel
			done = workerDone
			go func() {
				workerDone <- runPreviewWorker(
					workerCtx,
					target,
					deps,
					a.events,
				)
			}()
		}
	}
}

func newestPreviewTarget(
	target previewTarget,
	targets <-chan previewTarget,
) previewTarget {
	for {
		select {
		case target = <-targets:
		default:
			return target
		}
	}
}

func runPreviewWorker(
	ctx context.Context,
	target previewTarget,
	deps previewDependencies,
	events chan previewEvent,
) error {
	if deps.open == nil {
		offerLatest(ctx, events, previewEvent{
			Target: target,
			Err:    errors.New("clitui: preview opener is required"),
		})
		return nil
	}
	if deps.wait == nil {
		deps.wait = waitForPreviewRetry
	}

	var closeErr error
	retryIndex := 0
	for {
		if ctx.Err() != nil {
			return closeErr
		}

		stream, err := deps.open(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return closeErr
			}
			retryIn := previewRetryDelays[retryIndex]
			offerLatest(ctx, events, previewEvent{
				Target:  target,
				Err:     fmt.Errorf("clitui: open terminal preview: %w", err),
				RetryIn: retryIn,
			})
			if !deps.wait(ctx, retryIn) {
				return closeErr
			}
			retryIndex = nextPreviewRetryIndex(retryIndex)
			continue
		}

		err = stream.Subscribe(ctx, client.TerminalSubscription{
			AgentID: target.AgentID,
			Mode:    client.TerminalModeSnapshot,
		})
		if err != nil {
			closeErr = errors.Join(closeErr, stream.Close())
			if ctx.Err() != nil {
				return closeErr
			}
			retryIn := previewRetryDelays[retryIndex]
			offerLatest(ctx, events, previewEvent{
				Target:  target,
				Err:     fmt.Errorf("clitui: subscribe terminal preview: %w", err),
				RetryIn: retryIn,
			})
			if !deps.wait(ctx, retryIn) {
				return closeErr
			}
			retryIndex = nextPreviewRetryIndex(retryIndex)
			continue
		}

		for {
			var snapshot *client.TerminalSnapshot
			err = stream.Next(ctx, func(message client.TerminalMessage) error {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				value, ok := message.(client.TerminalSnapshot)
				if !ok {
					return fmt.Errorf(
						"clitui: terminal preview received %T",
						message,
					)
				}
				if value.AgentID != target.AgentID {
					return fmt.Errorf(
						"clitui: terminal preview agent %q, want %q",
						value.AgentID,
						target.AgentID,
					)
				}
				snapshot = cloneTerminalSnapshot(value)
				return nil
			})
			if err != nil {
				closeErr = errors.Join(closeErr, stream.Close())
				if ctx.Err() != nil {
					return closeErr
				}
				retryIn := previewRetryDelays[retryIndex]
				offerLatest(ctx, events, previewEvent{
					Target:  target,
					Err:     fmt.Errorf("clitui: read terminal preview: %w", err),
					RetryIn: retryIn,
				})
				if !deps.wait(ctx, retryIn) {
					return closeErr
				}
				retryIndex = nextPreviewRetryIndex(retryIndex)
				break
			}
			if snapshot == nil {
				continue
			}

			retryIndex = 0
			offerLatest(ctx, events, previewEvent{
				Target:   target,
				Snapshot: snapshot,
			})
		}
	}
}

func cloneTerminalSnapshot(snapshot client.TerminalSnapshot) *client.TerminalSnapshot {
	snapshot.Lines = append([]string(nil), snapshot.Lines...)
	return &snapshot
}

func nextPreviewRetryIndex(index int) int {
	if index < len(previewRetryDelays)-1 {
		return index + 1
	}
	return index
}

func waitForPreviewRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func offerLatest[T any](ctx context.Context, ch chan T, value T) {
	select {
	case <-ctx.Done():
		return
	default:
	}

	select {
	case ch <- value:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- value:
	case <-ctx.Done():
	}
}
