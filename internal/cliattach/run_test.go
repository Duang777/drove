package cliattach

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	xterm "github.com/charmbracelet/x/term"
	"github.com/muesli/cancelreader"

	"github.com/Duang777/drove/internal/client"
)

func TestRunReadOnlySendsNoInputOrViewport(t *testing.T) {
	stream := newFakeTerminalStream()
	reader := newFakeCancelReader()
	reader.Send([]byte{'x', localDetachByte})
	lifecycle := &fakeLifecycle{}
	deps := newFakeRunnerDependencies(stream, reader, io.Discard, lifecycle)

	if err := run(context.Background(), "agent-1", Options{ReadOnly: true}, deps); err != nil {
		t.Fatalf("run read-only attach: %v", err)
	}

	subscription := stream.Subscription()
	if subscription.Access != client.TerminalAccessReadOnly ||
		subscription.Rows != 0 ||
		subscription.Columns != 0 {
		t.Fatalf("subscription = %+v", subscription)
	}
	if inputs := stream.Inputs(); len(inputs) != 0 {
		t.Fatalf("read-only inputs = %q", inputs)
	}
	if resizes := stream.Resizes(); len(resizes) != 0 {
		t.Fatalf("read-only resizes = %+v", resizes)
	}
	if lifecycle.getSize.Load() != 0 ||
		lifecycle.notify.Load() != 0 ||
		lifecycle.stop.Load() != 0 {
		t.Fatalf("read-only lifecycle = %+v", lifecycle.snapshot())
	}
	if reader.maxRead.Load() != stdinChunkBytes {
		t.Fatalf("stdin read buffer = %d, want %d", reader.maxRead.Load(), stdinChunkBytes)
	}
	assertCleanupCounts(t, stream, reader, lifecycle)
}

func TestAttachLocalDetachIgnoresOutputCloseRace(t *testing.T) {
	stream := newFakeTerminalStream()
	stream.closeReadErr = errors.New("use of closed network connection")
	reader := newFakeCancelReader()
	reader.Send([]byte{localDetachByte})
	lifecycle := &fakeLifecycle{}
	deps := newFakeRunnerDependencies(stream, reader, io.Discard, lifecycle)

	if err := run(context.Background(), "agent-1", Options{}, deps); err != nil {
		t.Fatalf("run local detach: %v", err)
	}
	assertCleanupCounts(t, stream, reader, lifecycle)
}

func TestAttachTTYRejectsNonTerminalBeforeSetup(t *testing.T) {
	opened := false
	deps := runnerDependencies{
		stdin:  fakeInputTerminal{},
		stdout: io.Discard,
		open: func(context.Context) (terminalStream, error) {
			opened = true
			return newFakeTerminalStream(), nil
		},
		isTerminal: func(uintptr) bool {
			return false
		},
	}
	err := run(context.Background(), "agent-1", Options{}, deps)
	if !errors.Is(err, errStdinNotTerminal) {
		t.Fatalf("run error = %v, want non-terminal error", err)
	}
	if opened {
		t.Fatal("non-terminal stdin opened a terminal stream")
	}
}

func TestAttachCleanupAfterPumpFailure(t *testing.T) {
	stream := newFakeTerminalStream()
	stream.messages <- client.TerminalOutput{
		AgentID: "agent-1",
		Data:    []byte("remote output"),
	}
	reader := newFakeCancelReader()
	lifecycle := &fakeLifecycle{}
	writeErr := errors.New("stdout unavailable")
	deps := newFakeRunnerDependencies(
		stream,
		reader,
		errorWriter{err: writeErr},
		lifecycle,
	)

	err := run(context.Background(), "agent-1", Options{}, deps)
	if !errors.Is(err, writeErr) {
		t.Fatalf("run error = %v, want stdout failure", err)
	}
	if lifecycle.notify.Load() != 1 || lifecycle.stop.Load() != 1 {
		t.Fatalf("resize signal lifecycle = %+v", lifecycle.snapshot())
	}
	assertCleanupCounts(t, stream, reader, lifecycle)
}

func TestAttachCleanupOnContextCancellation(t *testing.T) {
	stream := newFakeTerminalStream()
	reader := newFakeCancelReader()
	lifecycle := &fakeLifecycle{}
	deps := newFakeRunnerDependencies(stream, reader, io.Discard, lifecycle)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- run(ctx, "agent-1", Options{}, deps)
	}()
	select {
	case <-stream.subscribed:
	case <-time.After(time.Second):
		t.Fatal("attach did not subscribe")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not unblock attach")
	}
	assertCleanupCounts(t, stream, reader, lifecycle)
}

func assertCleanupCounts(
	t *testing.T,
	stream *fakeTerminalStream,
	reader *fakeCancelReader,
	lifecycle *fakeLifecycle,
) {
	t.Helper()
	if stream.closeCount.Load() != 1 ||
		reader.cancelCount.Load() != 1 ||
		reader.closeCount.Load() != 1 ||
		lifecycle.raw.Load() != 1 ||
		lifecycle.restore.Load() != 1 {
		t.Fatalf(
			"cleanup stream=%d cancel=%d reader_close=%d raw=%d restore=%d",
			stream.closeCount.Load(),
			reader.cancelCount.Load(),
			reader.closeCount.Load(),
			lifecycle.raw.Load(),
			lifecycle.restore.Load(),
		)
	}
}

type fakeInputTerminal struct{}

func (fakeInputTerminal) Read([]byte) (int, error) {
	return 0, errors.New("unexpected direct terminal read")
}

func (fakeInputTerminal) Fd() uintptr {
	return 42
}

type fakeCancelReader struct {
	data        chan []byte
	canceled    chan struct{}
	cancelOnce  sync.Once
	cancelCount atomic.Int32
	closeCount  atomic.Int32
	maxRead     atomic.Int32
}

func newFakeCancelReader() *fakeCancelReader {
	return &fakeCancelReader{
		data:     make(chan []byte, 8),
		canceled: make(chan struct{}),
	}
}

func (r *fakeCancelReader) Send(data []byte) {
	r.data <- append([]byte(nil), data...)
}

func (r *fakeCancelReader) Read(buffer []byte) (int, error) {
	r.maxRead.Store(int32(len(buffer)))
	select {
	case data := <-r.data:
		return copy(buffer, data), nil
	case <-r.canceled:
		return 0, cancelreader.ErrCanceled
	}
}

func (r *fakeCancelReader) Cancel() bool {
	r.cancelCount.Add(1)
	r.cancelOnce.Do(func() {
		close(r.canceled)
	})
	return true
}

func (r *fakeCancelReader) Close() error {
	r.closeCount.Add(1)
	return nil
}

type terminalResize struct {
	rows    int
	columns int
}

type fakeTerminalStream struct {
	mu           sync.Mutex
	subscription client.TerminalSubscription
	inputs       []string
	resizes      []terminalResize

	messages     chan client.TerminalMessage
	closed       chan struct{}
	subscribed   chan struct{}
	closeReadErr error
	closeOnce    sync.Once
	closeCount   atomic.Int32
}

func newFakeTerminalStream() *fakeTerminalStream {
	return &fakeTerminalStream{
		messages:   make(chan client.TerminalMessage, 8),
		closed:     make(chan struct{}),
		subscribed: make(chan struct{}),
	}
}

func (s *fakeTerminalStream) Subscribe(
	_ context.Context,
	subscription client.TerminalSubscription,
) error {
	s.mu.Lock()
	s.subscription = subscription
	s.mu.Unlock()
	close(s.subscribed)
	return nil
}

func (s *fakeTerminalStream) Next(
	ctx context.Context,
	apply func(client.TerminalMessage) error,
) error {
	if s.closeReadErr != nil {
		<-s.closed
		return s.closeReadErr
	}
	select {
	case message := <-s.messages:
		return apply(message)
	case <-s.closed:
		return io.EOF
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *fakeTerminalStream) SendInput(
	_ context.Context,
	_ string,
	data string,
) (int, error) {
	s.mu.Lock()
	s.inputs = append(s.inputs, data)
	s.mu.Unlock()
	return len(data), nil
}

func (s *fakeTerminalStream) Resize(
	_ context.Context,
	_ string,
	rows int,
	columns int,
) error {
	s.mu.Lock()
	s.resizes = append(s.resizes, terminalResize{rows: rows, columns: columns})
	s.mu.Unlock()
	return nil
}

func (s *fakeTerminalStream) Close() error {
	s.closeCount.Add(1)
	s.closeOnce.Do(func() {
		close(s.closed)
	})
	return nil
}

func (s *fakeTerminalStream) RemoteEOF() {
	s.closeOnce.Do(func() {
		close(s.closed)
	})
}

func (s *fakeTerminalStream) Subscription() client.TerminalSubscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subscription
}

func (s *fakeTerminalStream) Inputs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.inputs...)
}

func (s *fakeTerminalStream) Resizes() []terminalResize {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]terminalResize(nil), s.resizes...)
}

type fakeLifecycle struct {
	raw      atomic.Int32
	restore  atomic.Int32
	getSize  atomic.Int32
	notify   atomic.Int32
	stop     atomic.Int32
	signalMu sync.Mutex
	signals  chan<- os.Signal
}

func (l *fakeLifecycle) snapshot() [5]int32 {
	return [5]int32{
		l.raw.Load(),
		l.restore.Load(),
		l.getSize.Load(),
		l.notify.Load(),
		l.stop.Load(),
	}
}

func newFakeRunnerDependencies(
	stream terminalStream,
	reader cancelReader,
	output io.Writer,
	lifecycle *fakeLifecycle,
) runnerDependencies {
	return runnerDependencies{
		stdin:  fakeInputTerminal{},
		stdout: output,
		open: func(context.Context) (terminalStream, error) {
			return stream, nil
		},
		isTerminal: func(uintptr) bool {
			return true
		},
		makeRaw: func(uintptr) (*xterm.State, error) {
			lifecycle.raw.Add(1)
			return &xterm.State{}, nil
		},
		restore: func(uintptr, *xterm.State) error {
			lifecycle.restore.Add(1)
			return nil
		},
		getSize: func(uintptr) (int, int, error) {
			lifecycle.getSize.Add(1)
			return 24, 80, nil
		},
		newCancelReader: func(io.Reader) (cancelReader, error) {
			return reader, nil
		},
		notifyResize: func(signals chan<- os.Signal) {
			lifecycle.notify.Add(1)
			lifecycle.signalMu.Lock()
			lifecycle.signals = signals
			lifecycle.signalMu.Unlock()
		},
		stopResize: func(chan<- os.Signal) {
			lifecycle.stop.Add(1)
		},
	}
}

type errorWriter struct {
	err error
}

func (w errorWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func joinInputs(inputs []string) []byte {
	var output bytes.Buffer
	for _, input := range inputs {
		output.WriteString(input)
	}
	return output.Bytes()
}
