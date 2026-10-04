package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/term"
)

const (
	terminalInboxSize  = 64
	terminalSampleWait = 100 * time.Millisecond
)

var (
	errTerminalActorClosed = errors.New("session: terminal actor closed")
	errTerminalStreamEnded = errors.New("session: terminal output stream ended")
)

type terminalResizeError struct {
	afterPTY bool
	err      error
}

func (e *terminalResizeError) Error() string {
	return e.err.Error()
}

func (e *terminalResizeError) Unwrap() error {
	return e.err
}

func terminalResizeWasApplied(err error) bool {
	var resizeErr *terminalResizeError
	return errors.As(err, &resizeErr) && resizeErr.afterPTY
}

type terminalProcess interface {
	Write([]byte) (int, error)
	Resize(rows, columns uint16) error
}

type terminalObserver interface {
	Deliver(context.Context, detect.Observation) error
}

type terminalOperation uint8

const (
	terminalFeed terminalOperation = iota + 1
	terminalSnapshot
	terminalProcessExited
	terminalEndOutput
	terminalResize
)

type terminalRequest struct {
	operation   terminalOperation
	chunk       term.CommittedChunk
	finalOffset uint64
	size        term.Size
	result      chan terminalResult
}

type terminalResult struct {
	snapshot     term.Snapshot
	capturedAt   time.Time
	outputOffset uint64
	available    bool
	err          error
}

type terminalActor struct {
	controller *term.Controller
	classifier *adapter.ScreenClassifier
	process    terminalProcess
	observer   terminalObserver
	vendor     string
	clock      observationClock
	fail       func(error)

	requests chan terminalRequest
	stop     chan struct{}
	done     chan struct{}

	admissionMu sync.RWMutex
	closing     bool
	closeOnce   sync.Once
	closeErr    error
}

type terminalActorState struct {
	nextOutputOffset     uint64
	lastOutputSeq        uint64
	dirty                bool
	ended                bool
	processExited        bool
	controllerClosed     bool
	snapshot             term.Snapshot
	snapshotCapturedAt   time.Time
	snapshotOutputOffset uint64
	snapshotAvailable    bool
	replyErr             error
	failure              error
}

func newTerminalActor(
	size term.Size,
	process terminalProcess,
	classifier *adapter.ScreenClassifier,
	observer terminalObserver,
	vendor string,
	clock observationClock,
	fail func(error),
) (*terminalActor, error) {
	if process == nil || classifier == nil {
		return nil, errors.New(
			"session: terminal actor requires process and classifier",
		)
	}
	if vendor == "" {
		return nil, errors.New("session: terminal actor vendor is required")
	}
	if clock == nil {
		clock = systemObservationClock{}
	}
	controller, err := term.NewController(size, func(frame []byte) error {
		written, writeErr := process.Write(frame)
		if writeErr != nil {
			return writeErr
		}
		if written != len(frame) {
			return io.ErrShortWrite
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("session: create terminal controller: %w", err)
	}
	actor := &terminalActor{
		controller: controller,
		classifier: classifier,
		process:    process,
		observer:   observer,
		vendor:     vendor,
		clock:      clock,
		fail:       fail,
		requests:   make(chan terminalRequest, terminalInboxSize),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	go actor.run()
	return actor, nil
}

func (a *terminalActor) FeedCommitted(
	ctx context.Context,
	chunk term.CommittedChunk,
) error {
	result, err := a.submit(ctx, terminalRequest{
		operation: terminalFeed,
		chunk:     chunk,
	})
	if err != nil {
		return err
	}
	return result.err
}

func (a *terminalActor) Snapshot() (term.Snapshot, bool) {
	snapshot, _, available := a.snapshotWithCapturedAt()
	return snapshot, available
}

func (a *terminalActor) snapshotWithCapturedAt() (
	term.Snapshot,
	time.Time,
	bool,
) {
	snapshot, capturedAt, _, available := a.snapshotState()
	return snapshot, capturedAt, available
}

func (a *terminalActor) snapshotState() (
	term.Snapshot,
	time.Time,
	uint64,
	bool,
) {
	result, err := a.submit(context.Background(), terminalRequest{
		operation: terminalSnapshot,
	})
	if err != nil || result.err != nil {
		return term.Snapshot{}, time.Time{}, 0, false
	}
	return result.snapshot, result.capturedAt, result.outputOffset, result.available
}

func (a *terminalActor) MarkProcessExited() {
	_, _ = a.submit(context.Background(), terminalRequest{
		operation: terminalProcessExited,
	})
}

func (a *terminalActor) EndOutput(
	ctx context.Context,
	finalOffset uint64,
) error {
	result, err := a.submit(ctx, terminalRequest{
		operation:   terminalEndOutput,
		finalOffset: finalOffset,
	})
	if err != nil {
		return err
	}
	return result.err
}

func (a *terminalActor) Resize(ctx context.Context, size term.Size) error {
	result, err := a.submit(ctx, terminalRequest{
		operation: terminalResize,
		size:      size,
	})
	if err != nil {
		return err
	}
	return result.err
}

func (a *terminalActor) Close() error {
	a.closeOnce.Do(func() {
		a.admissionMu.Lock()
		a.closing = true
		close(a.stop)
		a.admissionMu.Unlock()
		<-a.done
	})
	return a.closeErr
}

func (a *terminalActor) submit(
	ctx context.Context,
	request terminalRequest,
) (terminalResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	request.result = make(chan terminalResult, 1)

	a.admissionMu.RLock()
	if a.closing {
		a.admissionMu.RUnlock()
		return terminalResult{}, errTerminalActorClosed
	}
	select {
	case a.requests <- request:
		a.admissionMu.RUnlock()
	case <-ctx.Done():
		a.admissionMu.RUnlock()
		return terminalResult{}, fmt.Errorf(
			"session: admit terminal operation: %w",
			ctx.Err(),
		)
	}
	return <-request.result, nil
}

func (a *terminalActor) run() {
	defer close(a.done)
	state := terminalActorState{}
	var timer observationTimer
	var timerC <-chan time.Time
	replyErrors := a.controller.Errors()

	closeController := func() {
		if state.controllerClosed {
			return
		}
		state.controllerClosed = true
		if err := a.controller.Close(); err != nil {
			a.closeErr = errors.Join(a.closeErr, err)
		}
		for err := range replyErrors {
			if state.replyErr == nil {
				state.replyErr = err
			}
		}
		replyErrors = nil
	}
	defer closeController()

	handle := func(request terminalRequest) {
		result := a.handle(&state, &timer, &timerC, closeController, request)
		request.result <- result
	}

	for {
		select {
		case request := <-a.requests:
			handle(request)
		case firedAt := <-timerC:
			timerC = nil
			state.dirty = false
			if err := a.sample(&state, firedAt); err != nil {
				a.failActor(&state, err)
			}
		case err, open := <-replyErrors:
			if !open {
				replyErrors = nil
				continue
			}
			if state.replyErr == nil {
				state.replyErr = err
			}
		case <-a.stop:
			for {
				select {
				case request := <-a.requests:
					handle(request)
				default:
					if timer != nil {
						stopAndDrainTimer(timer)
					}
					return
				}
			}
		}
	}
}

func (a *terminalActor) handle(
	state *terminalActorState,
	timer *observationTimer,
	timerC *<-chan time.Time,
	closeController func(),
	request terminalRequest,
) terminalResult {
	if state.failure != nil && request.operation != terminalSnapshot &&
		request.operation != terminalProcessExited {
		return terminalResult{err: state.failure}
	}

	switch request.operation {
	case terminalFeed:
		if state.ended {
			return terminalResult{err: errTerminalStreamEnded}
		}
		data := request.chunk.Bytes()
		wantOffset := state.nextOutputOffset + uint64(len(data))
		if request.chunk.OutputOffset() != wantOffset {
			return terminalResult{err: fmt.Errorf(
				"session: committed terminal offset %d, want %d",
				request.chunk.OutputOffset(),
				wantOffset,
			)}
		}
		if request.chunk.LastSeq() <= state.lastOutputSeq {
			return terminalResult{err: fmt.Errorf(
				"session: committed terminal sequence %d does not follow %d",
				request.chunk.LastSeq(),
				state.lastOutputSeq,
			)}
		}
		if state.controllerClosed {
			return terminalResult{err: term.ErrClosed}
		}
		if err := a.controller.Write(data); err != nil {
			return terminalResult{err: err}
		}
		state.nextOutputOffset = request.chunk.OutputOffset()
		state.lastOutputSeq = request.chunk.LastSeq()
		if !state.dirty {
			state.dirty = true
			a.armSampleTimer(timer, timerC)
		}
		return terminalResult{}

	case terminalSnapshot:
		return terminalResult{
			snapshot:     state.snapshot,
			capturedAt:   state.snapshotCapturedAt,
			outputOffset: state.snapshotOutputOffset,
			available:    state.snapshotAvailable && !state.processExited,
		}

	case terminalProcessExited:
		state.processExited = true
		state.snapshotAvailable = false
		return terminalResult{}

	case terminalEndOutput:
		if state.ended {
			return terminalResult{err: errTerminalStreamEnded}
		}
		if request.finalOffset != state.nextOutputOffset {
			return terminalResult{err: fmt.Errorf(
				"session: terminal final offset %d, want %d",
				request.finalOffset,
				state.nextOutputOffset,
			)}
		}
		state.ended = true
		if *timer != nil {
			stopAndDrainTimer(*timer)
			*timerC = nil
		}
		if state.dirty {
			state.dirty = false
			if err := a.sample(state, a.clock.Now()); err != nil {
				a.failActor(state, err)
				return terminalResult{err: err}
			}
		}
		closeController()
		return terminalResult{err: state.replyErr}

	case terminalResize:
		if state.ended {
			return terminalResult{err: errTerminalStreamEnded}
		}
		rows := request.size.Rows()
		columns := request.size.Columns()
		if rows <= 0 || columns <= 0 || rows > 1<<16-1 || columns > 1<<16-1 {
			return terminalResult{err: errors.New(
				"session: terminal resize requires a validated size",
			)}
		}
		if err := a.process.Resize(uint16(rows), uint16(columns)); err != nil {
			return terminalResult{err: &terminalResizeError{
				err: fmt.Errorf("session: resize PTY: %w", err),
			}}
		}
		if err := a.controller.Resize(request.size); err != nil {
			return terminalResult{err: &terminalResizeError{
				afterPTY: true,
				err: fmt.Errorf(
					"session: resize emulator after PTY: %w",
					err,
				),
			}}
		}
		state.snapshotAvailable = false
		if !state.dirty {
			state.dirty = true
			a.armSampleTimer(timer, timerC)
		}
		return terminalResult{}

	default:
		return terminalResult{err: fmt.Errorf(
			"session: unknown terminal operation %d",
			request.operation,
		)}
	}
}

func (a *terminalActor) armSampleTimer(
	timer *observationTimer,
	timerC *<-chan time.Time,
) {
	if *timer == nil {
		*timer = a.clock.NewTimer(terminalSampleWait)
	} else {
		stopAndDrainTimer(*timer)
		(*timer).Reset(terminalSampleWait)
	}
	*timerC = (*timer).C()
}

func (a *terminalActor) sample(
	state *terminalActorState,
	at time.Time,
) error {
	snapshot, err := a.controller.Snapshot()
	if err != nil {
		return fmt.Errorf("session: snapshot terminal: %w", err)
	}
	state.snapshot = snapshot
	state.snapshotCapturedAt = at.UTC()
	state.snapshotOutputOffset = state.nextOutputOffset
	state.snapshotAvailable = !state.processExited
	if state.processExited {
		return nil
	}

	hints := a.classifier.Observe(snapshot)
	if len(hints) > 0 && a.observer == nil {
		return errors.New("session: screen edge has no observation actor")
	}
	for _, hint := range hints {
		attribution, err := agent.NewScreenAttribution(
			hint.Rule,
			hint.Edge,
			hint.Region,
			state.nextOutputOffset,
			state.lastOutputSeq,
			hint.Evidence,
		)
		if err != nil {
			return fmt.Errorf("session: create screen attribution: %w", err)
		}
		signal, err := detect.NewScreenSignal(detect.Signal{
			Kind:       hint.Kind,
			Vendor:     a.vendor,
			Confidence: hint.Confidence,
			ReceivedAt: at,
			Screen:     &attribution,
		})
		if err != nil {
			return fmt.Errorf("session: create screen signal: %w", err)
		}
		observation, err := detect.ObserveSignal(signal)
		if err != nil {
			return fmt.Errorf("session: observe screen signal: %w", err)
		}
		if err := a.observer.Deliver(context.Background(), observation); err != nil {
			return fmt.Errorf("session: deliver screen observation: %w", err)
		}
	}
	return nil
}

func (a *terminalActor) failActor(state *terminalActorState, err error) {
	if err == nil || state.failure != nil {
		return
	}
	state.failure = err
	if a.fail != nil {
		a.fail(err)
	}
}
