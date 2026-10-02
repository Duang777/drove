package pty

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStartDeliversImmediateOutputAndExitOnce(t *testing.T) {
	outputs := make(chan string, 4)
	exits := make(chan ExitInfo, 2)

	sess, err := Start(Config{
		Command: "/bin/sh",
		Args:    []string{"-c", "printf 'first\\nlast'"},
		OnOutput: func(line string) {
			outputs <- line
		},
		OnExit: func(info ExitInfo) {
			exits <- info
		},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		if err := sess.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})

	gotOutputs := []string{
		waitString(t, outputs, "first output"),
		waitString(t, outputs, "final partial output"),
	}
	if gotOutputs[0] != "first\r\n" {
		t.Fatalf("first output = %q, want %q", gotOutputs[0], "first\r\n")
	}
	if gotOutputs[1] != "last" {
		t.Fatalf("final output = %q, want %q", gotOutputs[1], "last")
	}

	info := waitExit(t, exits)
	if info.Code != 0 || info.Err != nil {
		t.Fatalf("exit info = %+v, want successful exit", info)
	}

	select {
	case output := <-outputs:
		t.Fatalf("unexpected duplicate output %q", output)
	case exit := <-exits:
		t.Fatalf("unexpected duplicate exit %+v", exit)
	case <-time.After(25 * time.Millisecond):
	}
}

func TestCloseWaitsForOutputCallbackAndIsIdempotent(t *testing.T) {
	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	var callbackOnce sync.Once
	var exitCalls atomic.Int32

	sess, err := Start(Config{
		Command: "/bin/sh",
		Args:    []string{"-c", "printf 'ready\\n'; sleep 30"},
		OnOutput: func(string) {
			callbackOnce.Do(func() { close(callbackStarted) })
			<-releaseCallback
		},
		OnExit: func(ExitInfo) {
			exitCalls.Add(1)
		},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	select {
	case <-callbackStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("output callback did not start")
	}

	firstClose := make(chan error, 1)
	secondClose := make(chan error, 1)
	go func() { firstClose <- sess.Close() }()
	go func() { secondClose <- sess.Close() }()

	select {
	case err := <-firstClose:
		t.Fatalf("first close returned before callback completed: %v", err)
	case err := <-secondClose:
		t.Fatalf("second close returned before callback completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseCallback)
	for i, result := range []<-chan error{firstClose, secondClose} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("close %d: %v", i+1, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("close %d did not finish", i+1)
		}
	}
	if got := exitCalls.Load(); got != 1 {
		t.Fatalf("exit callback calls = %d, want 1", got)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("third close: %v", err)
	}
	if _, err := sess.Write([]byte("input")); !errors.Is(err, ErrClosed) {
		t.Fatalf("write error = %v, want ErrClosed", err)
	}
	if err := sess.Resize(24, 80); !errors.Is(err, ErrClosed) {
		t.Fatalf("resize error = %v, want ErrClosed", err)
	}
}

func TestStartFailureInvokesNoCallback(t *testing.T) {
	var outputCalls atomic.Int32
	var exitCalls atomic.Int32

	sess, err := Start(Config{
		Command: filepath.Join(t.TempDir(), "missing-command"),
		OnOutput: func(string) {
			outputCalls.Add(1)
		},
		OnExit: func(ExitInfo) {
			exitCalls.Add(1)
		},
	})
	if err == nil {
		t.Fatal("start succeeded with a missing command")
	}
	if sess != nil {
		t.Fatalf("session = %+v, want nil", sess)
	}
	if got := outputCalls.Load(); got != 0 {
		t.Fatalf("output callback calls = %d, want 0", got)
	}
	if got := exitCalls.Load(); got != 0 {
		t.Fatalf("exit callback calls = %d, want 0", got)
	}
}

func waitString(t *testing.T, ch <-chan string, label string) string {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(2 * time.Second):
		t.Fatalf("%s timed out", label)
		return ""
	}
}

func waitExit(t *testing.T, ch <-chan ExitInfo) ExitInfo {
	t.Helper()
	select {
	case info := <-ch:
		return info
	case <-time.After(2 * time.Second):
		t.Fatal("exit callback timed out")
		return ExitInfo{}
	}
}
