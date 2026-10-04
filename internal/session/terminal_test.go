package session

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/term"
)

func TestTerminalActorUsesFixedSampleWindow(t *testing.T) {
	clock := newTerminalTestClock(time.Unix(100, 0).UTC())
	observer := &recordingTerminalObserver{}
	actor := newTerminalTestActor(t, "claude", clock, &terminalTestProcess{}, observer)
	t.Cleanup(func() {
		if err := actor.Close(); err != nil {
			t.Errorf("close actor: %v", err)
		}
	})

	first := []byte("\x1b[2J\x1b[30;1HDo you want to ")
	second := []byte("proceed?\r\nEsc to cancel")
	feedTerminalTestChunk(t, actor, first, uint64(len(first)), 1, clock.Now())

	timer := clock.onlyTimer(t)
	if delay, resets := timer.state(); delay != terminalSampleWait || resets != 0 {
		t.Fatalf("first timer = (%s, %d resets), want (%s, 0)", delay, resets, terminalSampleWait)
	}

	clock.advance(50 * time.Millisecond)
	feedTerminalTestChunk(
		t,
		actor,
		second,
		uint64(len(first)+len(second)),
		2,
		clock.Now(),
	)
	if delay, resets := timer.state(); delay != terminalSampleWait || resets != 0 {
		t.Fatalf("timer slid after second chunk: delay=%s resets=%d", delay, resets)
	}
	if observer.Count() != 0 {
		t.Fatalf("observations before sample = %d, want 0", observer.Count())
	}

	clock.advance(50 * time.Millisecond)
	timer.fire(clock.Now())
	observer.WaitForCount(t, 1)

	snapshot, capturedAt, available := actor.snapshotWithCapturedAt()
	if !available {
		t.Fatal("sampled snapshot is unavailable")
	}
	if want := time.Unix(100, 0).UTC().Add(100 * time.Millisecond); !capturedAt.Equal(want) {
		t.Fatalf("snapshot captured at %v, want %v", capturedAt, want)
	}
	row, ok := snapshot.Row(30)
	if !ok || row != "Esc to cancel" {
		t.Fatalf("snapshot row 30 = %q, %t", row, ok)
	}
}

func TestTerminalActorEndOutputFlushesDirtyScreen(t *testing.T) {
	clock := newTerminalTestClock(time.Unix(200, 0).UTC())
	observer := &recordingTerminalObserver{}
	actor := newTerminalTestActor(t, "claude", clock, &terminalTestProcess{}, observer)
	t.Cleanup(func() {
		if err := actor.Close(); err != nil {
			t.Errorf("close actor: %v", err)
		}
	})

	data := []byte("\x1b[2J\x1b[30;1HDo you want to proceed?\r\nEsc to cancel")
	feedTerminalTestChunk(t, actor, data, uint64(len(data)), 1, clock.Now())
	if err := actor.EndOutput(context.Background(), uint64(len(data))); err != nil {
		t.Fatalf("end output: %v", err)
	}

	if got := observer.Count(); got != 1 {
		t.Fatalf("final-flush observations = %d, want 1", got)
	}
	if _, available := actor.Snapshot(); !available {
		t.Fatal("final-flush snapshot is unavailable")
	}
	if active := clock.onlyTimer(t).Active(); active {
		t.Fatal("sample timer remained active after output end")
	}
}

func TestTerminalActorProcessExitFencesTrailingScreens(t *testing.T) {
	clock := newTerminalTestClock(time.Unix(300, 0).UTC())
	observer := &recordingTerminalObserver{}
	actor := newTerminalTestActor(t, "claude", clock, &terminalTestProcess{}, observer)
	t.Cleanup(func() {
		if err := actor.Close(); err != nil {
			t.Errorf("close actor: %v", err)
		}
	})

	first := []byte("\x1b[2J")
	feedTerminalTestChunk(t, actor, first, uint64(len(first)), 1, clock.Now())
	actor.MarkProcessExited()

	trailing := []byte("\x1b[30;1HDo you want to proceed?\r\nEsc to cancel")
	feedTerminalTestChunk(
		t,
		actor,
		trailing,
		uint64(len(first)+len(trailing)),
		2,
		clock.Now(),
	)
	if err := actor.EndOutput(
		context.Background(),
		uint64(len(first)+len(trailing)),
	); err != nil {
		t.Fatalf("end trailing output: %v", err)
	}

	if got := observer.Count(); got != 0 {
		t.Fatalf("post-exit observations = %d, want 0", got)
	}
	if _, available := actor.Snapshot(); available {
		t.Fatal("post-exit snapshot remained available")
	}
}

func TestTerminalActorWritesQueryRepliesDirectly(t *testing.T) {
	clock := newTerminalTestClock(time.Unix(400, 0).UTC())
	process := &terminalTestProcess{}
	actor := newTerminalTestActor(
		t,
		"generic",
		clock,
		process,
		&recordingTerminalObserver{},
	)
	t.Cleanup(func() {
		if err := actor.Close(); err != nil {
			t.Errorf("close actor: %v", err)
		}
	})

	query := []byte("\x1b[6n")
	feedTerminalTestChunk(t, actor, query, uint64(len(query)), 1, clock.Now())
	if got := process.WaitForWrites(t, 1); string(got[0]) != "\x1b[1;1R" {
		t.Fatalf("query replies = %q, want DSR reply", got)
	}
}

func TestTerminalActorReportsShortQueryReplyAtOutputEnd(t *testing.T) {
	clock := newTerminalTestClock(time.Unix(500, 0).UTC())
	process := &terminalTestProcess{writeLimit: 1}
	actor := newTerminalTestActor(
		t,
		"generic",
		clock,
		process,
		&recordingTerminalObserver{},
	)
	t.Cleanup(func() {
		_ = actor.Close()
	})

	query := []byte("\x1b[6n")
	feedTerminalTestChunk(t, actor, query, uint64(len(query)), 1, clock.Now())
	err := actor.EndOutput(context.Background(), uint64(len(query)))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("end output error = %v, want short write", err)
	}
}

func TestTerminalActorResizesPTYBeforeEmulator(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		clock := newTerminalTestClock(time.Unix(600, 0).UTC())
		process := &terminalTestProcess{}
		actor := newTerminalTestActor(
			t,
			"generic",
			clock,
			process,
			&recordingTerminalObserver{},
		)
		defer actor.Close()

		size, err := term.NewSize(24, 80)
		if err != nil {
			t.Fatalf("new size: %v", err)
		}
		if err := actor.Resize(context.Background(), size); err != nil {
			t.Fatalf("resize: %v", err)
		}
		if err := actor.EndOutput(context.Background(), 0); err != nil {
			t.Fatalf("end output: %v", err)
		}
		snapshot, available := actor.Snapshot()
		if !available || snapshot.Size() != size {
			t.Fatalf("snapshot size = %+v, available=%t", snapshot.Size(), available)
		}
		if got := process.Resizes(); len(got) != 1 || got[0] != [2]uint16{24, 80} {
			t.Fatalf("PTY resizes = %v", got)
		}
	})

	t.Run("PTY failure leaves emulator unchanged", func(t *testing.T) {
		clock := newTerminalTestClock(time.Unix(700, 0).UTC())
		resizeErr := errors.New("resize failed")
		process := &terminalTestProcess{resizeErr: resizeErr}
		actor := newTerminalTestActor(
			t,
			"generic",
			clock,
			process,
			&recordingTerminalObserver{},
		)
		defer actor.Close()

		size, err := term.NewSize(24, 80)
		if err != nil {
			t.Fatalf("new size: %v", err)
		}
		if err := actor.Resize(context.Background(), size); !errors.Is(err, resizeErr) {
			t.Fatalf("resize error = %v, want %v", err, resizeErr)
		}

		data := []byte("x")
		feedTerminalTestChunk(t, actor, data, uint64(len(data)), 1, clock.Now())
		if err := actor.EndOutput(context.Background(), uint64(len(data))); err != nil {
			t.Fatalf("end output: %v", err)
		}
		snapshot, available := actor.Snapshot()
		initial, _ := initialTerminalSize()
		if !available || snapshot.Size() != initial {
			t.Fatalf("snapshot size = %+v, want %+v", snapshot.Size(), initial)
		}
	})
}

func TestTerminalActorInboxAppliesBackpressure(t *testing.T) {
	clock := newTerminalTestClock(time.Unix(800, 0).UTC())
	release := make(chan struct{})
	defer close(release)
	observer := &blockingTerminalObserver{
		started: make(chan struct{}),
		release: release,
	}
	actor := newTerminalTestActor(
		t,
		"claude",
		clock,
		&terminalTestProcess{},
		observer,
	)
	t.Cleanup(func() {
		_ = actor.Close()
	})

	data := []byte("\x1b[2J\x1b[30;1HDo you want to proceed?\r\nEsc to cancel")
	feedTerminalTestChunk(t, actor, data, uint64(len(data)), 1, clock.Now())
	timer := clock.onlyTimer(t)
	timer.fire(clock.Now().Add(terminalSampleWait))
	select {
	case <-observer.started:
	case <-time.After(time.Second):
		t.Fatal("screen observation did not block")
	}

	for range terminalInboxSize {
		actor.requests <- terminalRequest{
			operation: terminalSnapshot,
			result:    make(chan terminalResult, 1),
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := actor.submit(ctx, terminalRequest{operation: terminalSnapshot}); err == nil {
		t.Fatal("submit succeeded through a full terminal inbox")
	}
}

func TestTerminalActorCloseIsIdempotent(t *testing.T) {
	actor := newTerminalTestActor(
		t,
		"generic",
		newTerminalTestClock(time.Unix(900, 0).UTC()),
		&terminalTestProcess{},
		&recordingTerminalObserver{},
	)

	var wait sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errs <- actor.Close()
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("close actor: %v", err)
		}
	}
	if _, available := actor.Snapshot(); available {
		t.Fatal("closed actor returned a snapshot")
	}
}

func newTerminalTestActor(
	t *testing.T,
	vendor string,
	clock observationClock,
	process terminalProcess,
	observer terminalObserver,
) *terminalActor {
	t.Helper()
	size, err := initialTerminalSize()
	if err != nil {
		t.Fatalf("initial terminal size: %v", err)
	}
	classifier, err := adapter.NewRegistry().For(vendor).NewScreenClassifier()
	if err != nil {
		t.Fatalf("new screen classifier: %v", err)
	}
	actor, err := newTerminalActor(
		size,
		process,
		classifier,
		observer,
		vendor,
		clock,
		nil,
	)
	if err != nil {
		t.Fatalf("new terminal actor: %v", err)
	}
	return actor
}

func mustInitialTerminalSize(t *testing.T) term.Size {
	t.Helper()
	size, err := initialTerminalSize()
	if err != nil {
		t.Fatalf("initial terminal size: %v", err)
	}
	return size
}

func feedTerminalTestChunk(
	t *testing.T,
	actor *terminalActor,
	data []byte,
	offset uint64,
	seq uint64,
	at time.Time,
) {
	t.Helper()
	chunk, err := term.NewCommittedChunk(data, offset, seq, at)
	if err != nil {
		t.Fatalf("new committed chunk: %v", err)
	}
	if err := actor.FeedCommitted(context.Background(), chunk); err != nil {
		t.Fatalf("feed committed chunk: %v", err)
	}
}

type recordingTerminalObserver struct {
	mu           sync.Mutex
	observations int
	err          error
}

type blockingTerminalObserver struct {
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (o *blockingTerminalObserver) Deliver(
	context.Context,
	detect.Observation,
) error {
	o.once.Do(func() {
		close(o.started)
	})
	<-o.release
	return nil
}

func (o *recordingTerminalObserver) Deliver(
	context.Context,
	detect.Observation,
) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.observations++
	return o.err
}

func (o *recordingTerminalObserver) Count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.observations
}

func (o *recordingTerminalObserver) WaitForCount(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for o.Count() < want {
		if time.Now().After(deadline) {
			t.Fatalf("observations = %d, want %d", o.Count(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

type terminalTestProcess struct {
	mu           sync.Mutex
	writes       [][]byte
	resizes      [][2]uint16
	writeLimit   int
	writeErr     error
	resizeErr    error
	writeStarted chan struct{}
	writeRelease <-chan struct{}
	startOnce    sync.Once
}

func (p *terminalTestProcess) Write(frame []byte) (int, error) {
	if p.writeStarted != nil {
		p.startOnce.Do(func() {
			close(p.writeStarted)
		})
	}
	if p.writeRelease != nil {
		<-p.writeRelease
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.writes = append(p.writes, append([]byte(nil), frame...))
	if p.writeErr != nil {
		return 0, p.writeErr
	}
	if p.writeLimit > 0 && p.writeLimit < len(frame) {
		return p.writeLimit, nil
	}
	return len(frame), nil
}

func (p *terminalTestProcess) Resize(rows, columns uint16) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resizes = append(p.resizes, [2]uint16{rows, columns})
	return p.resizeErr
}

func (p *terminalTestProcess) Writes() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	writes := make([][]byte, len(p.writes))
	for index := range p.writes {
		writes[index] = append([]byte(nil), p.writes[index]...)
	}
	return writes
}

func (p *terminalTestProcess) WaitForWrites(t *testing.T, want int) [][]byte {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		writes := p.Writes()
		if len(writes) >= want {
			if len(writes) != want {
				t.Fatalf("query replies = %q, want %d", writes, want)
			}
			return writes
		}
		if time.Now().After(deadline) {
			t.Fatalf("query replies = %q, want %d", writes, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func (p *terminalTestProcess) Resizes() [][2]uint16 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][2]uint16(nil), p.resizes...)
}

type terminalTestClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*terminalTestTimer
}

func newTerminalTestClock(now time.Time) *terminalTestClock {
	return &terminalTestClock{now: now}
}

func (c *terminalTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *terminalTestClock) NewTimer(delay time.Duration) observationTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &terminalTestTimer{
		channel: make(chan time.Time, 1),
		active:  true,
		delay:   delay,
	}
	c.timers = append(c.timers, timer)
	return timer
}

func (c *terminalTestClock) advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}

func (c *terminalTestClock) onlyTimer(t *testing.T) *terminalTestTimer {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.timers) != 1 {
		t.Fatalf("timer count = %d, want 1", len(c.timers))
	}
	return c.timers[0]
}

func (c *terminalTestClock) timerAt(t *testing.T, index int) *terminalTestTimer {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if index < 0 || index >= len(c.timers) {
		t.Fatalf("timer index %d outside %d timers", index, len(c.timers))
	}
	return c.timers[index]
}

type terminalTestTimer struct {
	mu         sync.Mutex
	channel    chan time.Time
	active     bool
	delay      time.Duration
	resetCount int
}

func (t *terminalTestTimer) C() <-chan time.Time {
	return t.channel
}

func (t *terminalTestTimer) Reset(delay time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	wasActive := t.active
	t.active = true
	t.delay = delay
	t.resetCount++
	return wasActive
}

func (t *terminalTestTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	wasActive := t.active
	t.active = false
	return wasActive
}

func (t *terminalTestTimer) fire(at time.Time) {
	t.mu.Lock()
	if !t.active {
		t.mu.Unlock()
		return
	}
	t.active = false
	t.mu.Unlock()
	t.channel <- at
}

func (t *terminalTestTimer) state() (time.Duration, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.delay, t.resetCount
}

func (t *terminalTestTimer) Active() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.active
}
