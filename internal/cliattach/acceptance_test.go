package cliattach

import (
	"bytes"
	"context"
	"io"
	"os"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"

	xterm "github.com/charmbracelet/x/term"
	"github.com/creack/pty"
	"github.com/muesli/cancelreader"

	"github.com/Duang777/drove/internal/client"
)

func TestAttachTTYPseudoTerminalForwardsKeysResizesAndRestores(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("open pseudo-terminal: %v", err)
	}
	defer master.Close()
	defer slave.Close()
	if err := pty.Setsize(slave, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
		t.Fatalf("set initial pseudo-terminal size: %v", err)
	}
	initialState, err := xterm.GetState(slave.Fd())
	if err != nil {
		t.Fatalf("read initial terminal state: %v", err)
	}

	stream := newFakeTerminalStream()
	var output bytes.Buffer
	resizeReady := make(chan struct{})
	var (
		resizeMu      sync.Mutex
		resizeSignals chan<- os.Signal
	)
	deps := pseudoTerminalDependencies(
		slave,
		stream,
		&output,
		func(signals chan<- os.Signal) {
			resizeMu.Lock()
			resizeSignals = signals
			resizeMu.Unlock()
			close(resizeReady)
		},
	)
	result := make(chan error, 1)
	go func() {
		result <- run(context.Background(), "agent-1", Options{}, deps)
	}()
	waitForAttachSubscription(t, stream)

	stream.messages <- client.TerminalOutput{
		AgentID: "agent-1",
		Data:    []byte("\x1b[32mremote\x1b[0m"),
	}
	input := []byte("\x1b[A\x1b")
	input = append(input, []byte("粘贴")...)
	input = append(input, 0x03)
	if _, err := master.Write(input); err != nil {
		t.Fatalf("write pseudo-terminal input: %v", err)
	}
	waitForAttachInput(t, stream)

	if err := pty.Setsize(slave, &pty.Winsize{Rows: 42, Cols: 132}); err != nil {
		t.Fatalf("resize pseudo-terminal: %v", err)
	}
	select {
	case <-resizeReady:
	case <-time.After(time.Second):
		t.Fatal("resize listener was not installed")
	}
	resizeMu.Lock()
	resizeSignals <- syscall.SIGWINCH
	resizeMu.Unlock()
	waitForAttachResize(t, stream, terminalResize{rows: 42, columns: 132})

	if _, err := master.Write([]byte{localDetachByte}); err != nil {
		t.Fatalf("write Ctrl-Q: %v", err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("run pseudo-terminal attach: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ctrl-Q did not detach")
	}

	if got := joinInputs(stream.Inputs()); !bytes.Equal(got, input) {
		t.Fatalf("forwarded input = %q, want %q", got, input)
	}
	if bytes.Contains(joinInputs(stream.Inputs()), []byte{localDetachByte}) {
		t.Fatal("forwarded input retained Ctrl-Q")
	}
	if got := output.Bytes(); !bytes.Equal(got, []byte("\x1b[32mremote\x1b[0m")) {
		t.Fatalf("terminal output = %q", got)
	}
	subscription := stream.Subscription()
	if subscription.Access != client.TerminalAccessReadWrite ||
		subscription.Rows != 24 ||
		subscription.Columns != 80 {
		t.Fatalf("subscription = %+v", subscription)
	}
	restoredState, err := xterm.GetState(slave.Fd())
	if err != nil {
		t.Fatalf("read restored terminal state: %v", err)
	}
	if !reflect.DeepEqual(restoredState, initialState) {
		t.Fatal("terminal state was not restored")
	}
}

func TestAttachTTYPseudoTerminalRemoteEOFRestores(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("open pseudo-terminal: %v", err)
	}
	defer master.Close()
	defer slave.Close()
	initialState, err := xterm.GetState(slave.Fd())
	if err != nil {
		t.Fatalf("read initial terminal state: %v", err)
	}

	stream := newFakeTerminalStream()
	deps := pseudoTerminalDependencies(
		slave,
		stream,
		io.Discard,
		func(chan<- os.Signal) {},
	)
	result := make(chan error, 1)
	go func() {
		result <- run(context.Background(), "agent-1", Options{}, deps)
	}()
	waitForAttachSubscription(t, stream)
	stream.RemoteEOF()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("remote EOF error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remote EOF did not stop attach")
	}
	restoredState, err := xterm.GetState(slave.Fd())
	if err != nil {
		t.Fatalf("read restored terminal state: %v", err)
	}
	if !reflect.DeepEqual(restoredState, initialState) {
		t.Fatal("terminal state was not restored after remote EOF")
	}
}

func pseudoTerminalDependencies(
	stdin *os.File,
	stream terminalStream,
	output io.Writer,
	notify func(chan<- os.Signal),
) runnerDependencies {
	return runnerDependencies{
		stdin:      stdin,
		stdout:     output,
		open:       func(context.Context) (terminalStream, error) { return stream, nil },
		isTerminal: xterm.IsTerminal,
		makeRaw:    xterm.MakeRaw,
		restore:    xterm.Restore,
		getSize: func(fd uintptr) (int, int, error) {
			columns, rows, err := xterm.GetSize(fd)
			return rows, columns, err
		},
		newCancelReader: func(reader io.Reader) (cancelReader, error) {
			return cancelreader.NewReader(reader)
		},
		notifyResize: notify,
		stopResize:   func(chan<- os.Signal) {},
	}
}

func waitForAttachSubscription(t *testing.T, stream *fakeTerminalStream) {
	t.Helper()
	select {
	case <-stream.subscribed:
	case <-time.After(time.Second):
		t.Fatal("attach did not subscribe")
	}
}

func waitForAttachInput(t *testing.T, stream *fakeTerminalStream) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for len(stream.Inputs()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("terminal input was not forwarded")
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForAttachResize(
	t *testing.T,
	stream *fakeTerminalStream,
	want terminalResize,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		resizes := stream.Resizes()
		if len(resizes) != 0 {
			if got := resizes[len(resizes)-1]; got != want {
				t.Fatalf("terminal resize = %+v, want %+v", got, want)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal resize was not forwarded")
		}
		time.Sleep(time.Millisecond)
	}
}
