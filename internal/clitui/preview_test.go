package clitui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/client"
)

func TestPreviewPublishesCopiedSnapshotWithExactSubscription(t *testing.T) {
	t.Parallel()

	subscribed := make(chan client.TerminalSubscription, 1)
	source := client.TerminalSnapshot{
		AgentID:    "agent-1",
		Rows:       24,
		Columns:    80,
		Lines:      []string{"first", "second"},
		Truncated:  true,
		Restorable: true,
		CapturedAt: time.Date(2026, time.October, 5, 13, 0, 0, 0, time.UTC),
	}
	var nextCalls atomic.Int32
	stream := &scriptedPreviewStream{
		subscribeFn: func(_ context.Context, subscription client.TerminalSubscription) error {
			subscribed <- subscription
			return nil
		},
		nextFn: func(ctx context.Context, apply func(client.TerminalMessage) error) error {
			if nextCalls.Add(1) == 1 {
				err := apply(source)
				source.Lines[0] = "mutated"
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		},
	}
	actor := newPreviewActor(context.Background(), func(context.Context) (previewStream, error) {
		return stream, nil
	})
	target := previewTarget{AgentID: "agent-1", Generation: 7}
	actor.Replace(target)

	wantSubscription := client.TerminalSubscription{
		AgentID: "agent-1",
		Mode:    client.TerminalModeSnapshot,
	}
	if got := receivePreviewValue(t, subscribed); !reflect.DeepEqual(got, wantSubscription) {
		t.Fatalf("subscription = %+v, want %+v", got, wantSubscription)
	}
	event := receivePreviewValue(t, actor.Events())
	if event.Target != target || event.Err != nil || event.RetryIn != 0 {
		t.Fatalf("preview event = %+v", event)
	}
	if event.Snapshot == nil {
		t.Fatal("preview event omitted snapshot")
	}
	if got, want := event.Snapshot.Lines, []string{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot lines = %q, want %q", got, want)
	}

	if err := actor.Close(); err != nil {
		t.Fatalf("close preview: %v", err)
	}
	if got := stream.closeCalls.Load(); got != 1 {
		t.Fatalf("stream close calls = %d, want 1", got)
	}
}

func TestPreviewReplacementJoinsWorkerAndCoalescesTarget(t *testing.T) {
	t.Parallel()

	subscriptions := make(chan client.TerminalSubscription, 3)
	firstStarted := make(chan struct{})
	firstCanceled := make(chan struct{})
	releaseFirst := make(chan struct{})
	overlap := make(chan struct{}, 1)
	var (
		activeNext atomic.Int32
		openCalls  atomic.Int32
	)

	guardNext := func(run func() error) error {
		if activeNext.Add(1) != 1 {
			select {
			case overlap <- struct{}{}:
			default:
			}
		}
		defer activeNext.Add(-1)
		return run()
	}
	first := &scriptedPreviewStream{
		subscribeFn: func(_ context.Context, subscription client.TerminalSubscription) error {
			subscriptions <- subscription
			return nil
		},
		nextFn: func(ctx context.Context, _ func(client.TerminalMessage) error) error {
			return guardNext(func() error {
				close(firstStarted)
				<-ctx.Done()
				close(firstCanceled)
				<-releaseFirst
				return ctx.Err()
			})
		},
	}
	var secondNextCalls atomic.Int32
	second := &scriptedPreviewStream{
		subscribeFn: func(_ context.Context, subscription client.TerminalSubscription) error {
			subscriptions <- subscription
			return nil
		},
		nextFn: func(ctx context.Context, apply func(client.TerminalMessage) error) error {
			return guardNext(func() error {
				if secondNextCalls.Add(1) == 1 {
					return apply(client.TerminalSnapshot{
						AgentID: "agent-3",
						Lines:   []string{"latest"},
					})
				}
				<-ctx.Done()
				return ctx.Err()
			})
		},
	}
	actor := newPreviewActor(context.Background(), func(context.Context) (previewStream, error) {
		switch openCalls.Add(1) {
		case 1:
			return first, nil
		case 2:
			return second, nil
		default:
			return nil, errors.New("unexpected preview open")
		}
	})
	actor.Replace(previewTarget{AgentID: "agent-1", Generation: 1})
	if got := receivePreviewValue(t, subscriptions).AgentID; got != "agent-1" {
		t.Fatalf("first subscription agent = %q, want agent-1", got)
	}
	receivePreviewSignal(t, firstStarted)

	actor.Replace(previewTarget{AgentID: "agent-2", Generation: 2})
	actor.Replace(previewTarget{AgentID: "agent-3", Generation: 3})
	receivePreviewSignal(t, firstCanceled)
	select {
	case subscription := <-subscriptions:
		t.Fatalf("opened %q before old worker completed", subscription.AgentID)
	case <-time.After(5 * time.Millisecond):
	}

	close(releaseFirst)
	if got := receivePreviewValue(t, subscriptions).AgentID; got != "agent-3" {
		t.Fatalf("replacement subscription agent = %q, want agent-3", got)
	}
	event := receivePreviewValue(t, actor.Events())
	if event.Target != (previewTarget{AgentID: "agent-3", Generation: 3}) {
		t.Fatalf("replacement event target = %+v", event.Target)
	}
	if event.Snapshot == nil || !reflect.DeepEqual(event.Snapshot.Lines, []string{"latest"}) {
		t.Fatalf("replacement snapshot = %+v", event.Snapshot)
	}
	select {
	case <-overlap:
		t.Fatal("preview generations called Next concurrently")
	default:
	}

	if err := actor.Close(); err != nil {
		t.Fatalf("close preview: %v", err)
	}
	if got := first.closeCalls.Load(); got != 1 {
		t.Fatalf("first stream close calls = %d, want 1", got)
	}
	if got := second.closeCalls.Load(); got != 1 {
		t.Fatalf("second stream close calls = %d, want 1", got)
	}
}

func TestPreviewReconnectBackoffAndSnapshotReset(t *testing.T) {
	t.Parallel()

	delays := make(chan time.Duration, 5)
	sixthOpen := make(chan struct{})
	var openCalls atomic.Int32
	open := func(context.Context) (previewStream, error) {
		switch openCalls.Add(1) {
		case 1, 2:
			return nil, errors.New("dial failed")
		case 3:
			return &scriptedPreviewStream{
				subscribeFn: func(context.Context, client.TerminalSubscription) error {
					return errors.New("subscribe failed")
				},
			}, nil
		case 4:
			return &scriptedPreviewStream{
				nextFn: func(context.Context, func(client.TerminalMessage) error) error {
					return errors.New("read failed")
				},
			}, nil
		case 5:
			var calls atomic.Int32
			return &scriptedPreviewStream{
				nextFn: func(_ context.Context, apply func(client.TerminalMessage) error) error {
					if calls.Add(1) == 1 {
						return apply(client.TerminalSnapshot{
							AgentID: "agent-1",
							Lines:   []string{"recovered"},
						})
					}
					return errors.New("read failed again")
				},
			}, nil
		case 6:
			close(sixthOpen)
			return &scriptedPreviewStream{
				nextFn: func(ctx context.Context, _ func(client.TerminalMessage) error) error {
					<-ctx.Done()
					return ctx.Err()
				},
			}, nil
		default:
			return nil, errors.New("unexpected preview open")
		}
	}
	actor := newPreviewActorWithDependencies(context.Background(), previewDependencies{
		open: open,
		wait: func(_ context.Context, delay time.Duration) bool {
			delays <- delay
			return true
		},
	})
	actor.Replace(previewTarget{AgentID: "agent-1", Generation: 1})

	want := []time.Duration{
		250 * time.Millisecond,
		500 * time.Millisecond,
		time.Second,
		2 * time.Second,
		250 * time.Millisecond,
	}
	got := make([]time.Duration, len(want))
	for index := range got {
		got[index] = receivePreviewValue(t, delays)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("retry delays = %v, want %v", got, want)
	}
	receivePreviewSignal(t, sixthOpen)

	if err := actor.Close(); err != nil {
		t.Fatalf("close preview: %v", err)
	}
}

func TestPreviewRejectsMismatchedAgentAndCarriesGeneration(t *testing.T) {
	t.Parallel()

	waiting := make(chan struct{})
	var waitOnce sync.Once
	stream := &scriptedPreviewStream{
		nextFn: func(_ context.Context, apply func(client.TerminalMessage) error) error {
			return apply(client.TerminalSnapshot{
				AgentID: "agent-other",
				Lines:   []string{"wrong"},
			})
		},
	}
	actor := newPreviewActorWithDependencies(context.Background(), previewDependencies{
		open: func(context.Context) (previewStream, error) {
			return stream, nil
		},
		wait: func(ctx context.Context, _ time.Duration) bool {
			waitOnce.Do(func() { close(waiting) })
			<-ctx.Done()
			return false
		},
	})
	target := previewTarget{AgentID: "agent-1", Generation: 42}
	actor.Replace(target)

	event := receivePreviewValue(t, actor.Events())
	if event.Target != target {
		t.Fatalf("event target = %+v, want %+v", event.Target, target)
	}
	if event.Err == nil || !strings.Contains(event.Err.Error(), `agent "agent-other", want "agent-1"`) {
		t.Fatalf("event error = %v", event.Err)
	}
	if event.Snapshot != nil || event.RetryIn != 250*time.Millisecond {
		t.Fatalf("mismatched event = %+v", event)
	}
	receivePreviewSignal(t, waiting)

	if err := actor.Close(); err != nil {
		t.Fatalf("close preview: %v", err)
	}
}

func TestPreviewCloseIsIdempotentAndWaitsForWorker(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	closeErr := errors.New("close failed")
	stream := &scriptedPreviewStream{
		nextFn: func(ctx context.Context, _ func(client.TerminalMessage) error) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
		closeFn: func() error {
			return closeErr
		},
	}
	actor := newPreviewActor(context.Background(), func(context.Context) (previewStream, error) {
		return stream, nil
	})
	actor.Replace(previewTarget{AgentID: "agent-1", Generation: 1})
	receivePreviewSignal(t, started)

	results := make(chan error, 2)
	go func() { results <- actor.Close() }()
	go func() { results <- actor.Close() }()
	for range 2 {
		if err := receivePreviewValue(t, results); !errors.Is(err, closeErr) {
			t.Fatalf("close error = %v, want %v", err, closeErr)
		}
	}
	if got := stream.closeCalls.Load(); got != 1 {
		t.Fatalf("stream close calls = %d, want 1", got)
	}
	if _, ok := <-actor.Events(); ok {
		t.Fatal("preview events remained open after close")
	}
}

type scriptedPreviewStream struct {
	subscribeFn func(context.Context, client.TerminalSubscription) error
	nextFn      func(context.Context, func(client.TerminalMessage) error) error
	closeFn     func() error
	closeCalls  atomic.Int32
}

func (s *scriptedPreviewStream) Subscribe(
	ctx context.Context,
	subscription client.TerminalSubscription,
) error {
	if s.subscribeFn == nil {
		return nil
	}
	return s.subscribeFn(ctx, subscription)
}

func (s *scriptedPreviewStream) Next(
	ctx context.Context,
	apply func(client.TerminalMessage) error,
) error {
	if s.nextFn == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.nextFn(ctx, apply)
}

func (s *scriptedPreviewStream) Close() error {
	s.closeCalls.Add(1)
	if s.closeFn == nil {
		return nil
	}
	return s.closeFn()
}

func receivePreviewValue[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before value")
		}
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for value")
		var zero T
		return zero
	}
}

func receivePreviewSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for signal")
	}
}
