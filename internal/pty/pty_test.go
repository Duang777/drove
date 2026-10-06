package pty

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

type chunkWriter struct {
	bytes.Buffer
	max int
	err error
}

type outputRecord struct {
	data   []byte
	offset uint64
}

func (w *chunkWriter) Write(data []byte) (int, error) {
	if w.max == 0 {
		return 0, w.err
	}
	if len(data) > w.max {
		data = data[:w.max]
	}
	n, _ := w.Buffer.Write(data)
	return n, w.err
}

func TestWriteFullCompletesShortWrites(t *testing.T) {
	writer := &chunkWriter{max: 2}
	data := []byte("abcdef")

	n, err := writeFull(writer, data)
	if err != nil {
		t.Fatalf("write full: %v", err)
	}
	if n != len(data) || !bytes.Equal(writer.Bytes(), data) {
		t.Fatalf("write result = (%d, %q), want (%d, %q)", n, writer.Bytes(), len(data), data)
	}
}

func TestWriteFullRejectsZeroProgress(t *testing.T) {
	n, err := writeFull(&chunkWriter{}, []byte("input"))
	if n != 0 || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("write result = (%d, %v), want (0, io.ErrShortWrite)", n, err)
	}
}

func TestWriteFullPreservesPartialFailure(t *testing.T) {
	writeErr := errors.New("write failed")
	writer := &chunkWriter{max: 2, err: writeErr}

	n, err := writeFull(writer, []byte("input"))
	if n != 2 || !errors.Is(err, writeErr) {
		t.Fatalf("write result = (%d, %v), want (2, write failed)", n, err)
	}
	if got := writer.String(); got != "in" {
		t.Fatalf("written data = %q, want %q", got, "in")
	}
}

func TestNewSizeValidatesDimensions(t *testing.T) {
	tests := []struct {
		name    string
		rows    int
		columns int
		wantErr bool
	}{
		{name: "valid", rows: 40, columns: 120},
		{name: "zero rows", rows: 0, columns: 120, wantErr: true},
		{name: "zero columns", rows: 40, columns: 0, wantErr: true},
		{name: "rows overflow", rows: maxTerminalDimension + 1, columns: 120, wantErr: true},
		{name: "columns overflow", rows: 40, columns: maxTerminalDimension + 1, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			size, err := NewSize(test.rows, test.columns)
			if test.wantErr {
				if err == nil {
					t.Fatalf("NewSize(%d, %d) succeeded", test.rows, test.columns)
				}
				return
			}
			if err != nil {
				t.Fatalf("new size: %v", err)
			}
			if size.Rows() != test.rows || size.Columns() != test.columns {
				t.Fatalf(
					"size = %dx%d, want %dx%d",
					size.Rows(),
					size.Columns(),
					test.rows,
					test.columns,
				)
			}
		})
	}
}

func TestStartRejectsInvalidSizeBeforeProcessStart(t *testing.T) {
	touch, err := exec.LookPath("touch")
	if err != nil {
		t.Fatalf("find touch: %v", err)
	}
	tests := []struct {
		name string
		size Size
	}{
		{name: "zero rows", size: Size{columns: 120}},
		{name: "zero columns", size: Size{rows: 40}},
		{name: "rows overflow", size: Size{rows: maxTerminalDimension + 1, columns: 120}},
		{name: "columns overflow", size: Size{rows: 40, columns: maxTerminalDimension + 1}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "started")
			sess, err := Start(Config{
				Command: touch,
				Args:    []string{marker},
				Size:    test.size,
			})
			if err == nil {
				t.Fatal("start succeeded with an invalid size")
			}
			if sess != nil {
				t.Fatalf("session = %+v, want nil", sess)
			}
			if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("process marker error = %v, want not exist", statErr)
			}
		})
	}
}

func TestNormalizeTerminationGrace(t *testing.T) {
	for _, test := range []struct {
		name    string
		input   time.Duration
		want    time.Duration
		wantErr bool
	}{
		{name: "default", want: 5 * time.Second},
		{name: "configured", input: 250 * time.Millisecond, want: 250 * time.Millisecond},
		{name: "negative", input: -time.Second, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeTerminationGrace(test.input)
			if test.wantErr {
				if err == nil {
					t.Fatalf("normalize %v succeeded", test.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalize %v: %v", test.input, err)
			}
			if got != test.want {
				t.Fatalf("normalized grace = %v, want %v", got, test.want)
			}
		})
	}
}

func TestProcessGroupAliveTreatsPermissionDeniedAsAlive(t *testing.T) {
	alive, err := processGroupAliveWith(
		func(pid int, signal syscall.Signal) error {
			if pid != 42 || signal != 0 {
				t.Fatalf("signal process group = (%d, %d), want (42, 0)", pid, signal)
			}
			return syscall.EPERM
		},
		42,
	)
	if err != nil {
		t.Fatalf("inspect process group: %v", err)
	}
	if !alive {
		t.Fatal("permission-denied process group reported as exited")
	}
}

func TestWaitForProcessGroupExitChecksAgainAtDeadline(t *testing.T) {
	probes := 0
	exited, err := waitForProcessGroupExit(
		func(pid int, signal syscall.Signal) error {
			if pid != 42 || signal != 0 {
				t.Fatalf("signal process group = (%d, %d), want (42, 0)", pid, signal)
			}
			probes++
			if probes == 1 {
				return nil
			}
			return syscall.ESRCH
		},
		42,
		time.Millisecond,
	)
	if err != nil {
		t.Fatalf("wait for process group: %v", err)
	}
	if !exited {
		t.Fatal("process group exit at deadline was not observed")
	}
	if probes != 2 {
		t.Fatalf("process group probes = %d, want 2", probes)
	}
}

func TestSessionChildObservesInitialSize(t *testing.T) {
	stty, err := exec.LookPath("stty")
	if err != nil {
		t.Fatalf("find stty: %v", err)
	}
	size := testSize(t)
	outputs := make(chan outputRecord)
	outputEnds := make(chan uint64, 1)

	sess, err := Start(Config{
		Command: stty,
		Args:    []string{"size"},
		Size:    size,
		OnOutput: func(chunk []byte, offset uint64) {
			outputs <- outputRecord{data: chunk, offset: offset}
		},
		OnOutputEnd: func(offset uint64) {
			outputEnds <- offset
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

	var got []byte
	var nextOffset uint64
	for {
		select {
		case output := <-outputs:
			if output.offset != nextOffset {
				t.Fatalf("output offset = %d, want %d", output.offset, nextOffset)
			}
			got = append(got, output.data...)
			nextOffset += uint64(len(output.data))
		case endOffset := <-outputEnds:
			if endOffset != nextOffset {
				t.Fatalf("end offset = %d, want %d", endOffset, nextOffset)
			}
			if observed := strings.TrimSpace(string(got)); observed != "40 120" {
				t.Fatalf("initial size = %q, want %q", observed, "40 120")
			}
			return
		case <-time.After(2 * time.Second):
			t.Fatal("initial size query timed out")
		}
	}
}

func TestStartDeliversImmediateOutputAndExitOnce(t *testing.T) {
	outputs := make(chan outputRecord)
	outputEnds := make(chan uint64, 2)
	exits := make(chan ExitInfo, 2)

	sess, err := Start(Config{
		Command: "/bin/sh",
		Args:    []string{"-c", "printf 'first\\nlast'"},
		Size:    testSize(t),
		OnOutput: func(chunk []byte, offset uint64) {
			outputs <- outputRecord{data: chunk, offset: offset}
		},
		OnOutputEnd: func(offset uint64) {
			outputEnds <- offset
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

	var got []byte
	var nextOffset uint64
	for {
		select {
		case output := <-outputs:
			if output.offset != nextOffset {
				t.Fatalf("output offset = %d, want %d", output.offset, nextOffset)
			}
			got = append(got, output.data...)
			nextOffset += uint64(len(output.data))
		case endOffset := <-outputEnds:
			if endOffset != nextOffset {
				t.Fatalf("end offset = %d, want %d", endOffset, nextOffset)
			}
			goto outputComplete
		case <-time.After(2 * time.Second):
			t.Fatal("output end timed out")
		}
	}
outputComplete:
	if string(got) != "first\r\nlast" {
		t.Fatalf("output = %q, want %q", got, "first\r\nlast")
	}

	info := waitExit(t, exits)
	if info.Code != 0 || info.Err != nil {
		t.Fatalf("exit info = %+v, want successful exit", info)
	}

	select {
	case output := <-outputs:
		t.Fatalf("unexpected duplicate output %q", output.data)
	case offset := <-outputEnds:
		t.Fatalf("unexpected duplicate output end at %d", offset)
	case exit := <-exits:
		t.Fatalf("unexpected duplicate exit %+v", exit)
	case <-time.After(25 * time.Millisecond):
	}

	select {
	case <-sess.done:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not finish after natural exit")
	}
	if _, err := sess.Write([]byte("input")); !errors.Is(err, ErrClosed) {
		t.Fatalf("write after natural exit = %v, want ErrClosed", err)
	}
	if err := sess.Resize(24, 80); !errors.Is(err, ErrClosed) {
		t.Fatalf("resize after natural exit = %v, want ErrClosed", err)
	}
}

func TestStartDeliversPromptWithoutNewline(t *testing.T) {
	outputs := make(chan outputRecord, 1)
	sess, err := Start(Config{
		Command: "/bin/sh",
		Args:    []string{"-c", "printf 'Allow? [y/n] '; sleep 30"},
		Size:    testSize(t),
		OnOutput: func(chunk []byte, offset uint64) {
			outputs <- outputRecord{data: chunk, offset: offset}
		},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		if err := sess.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()

	select {
	case output := <-outputs:
		if output.offset != 0 || string(output.data) != "Allow? [y/n] " {
			t.Fatalf("output = (%d, %q)", output.offset, output.data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("prompt without newline was not delivered")
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
		Size:    testSize(t),
		OnOutput: func([]byte, uint64) {
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

func TestCloseAllowsProcessGroupToExitOnTerm(t *testing.T) {
	outputs := make(chan []byte, 128)
	outputEnds := make(chan struct{}, 1)
	exits := make(chan ExitInfo, 1)
	sess, err := Start(Config{
		Command:          os.Args[0],
		Args:             ptyHelperArgs("cooperative"),
		Env:              []string{"DROVE_PTY_HELPER=1"},
		Size:             testSize(t),
		TerminationGrace: 2 * time.Second,
		OnOutput: func(chunk []byte, _ uint64) {
			outputs <- append([]byte(nil), chunk...)
		},
		OnOutputEnd: func(uint64) {
			outputEnds <- struct{}{}
		},
		OnExit: func(info ExitInfo) {
			exits <- info
		},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	waitForOutput(t, outputs, "ready")
	if err := sess.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	info := waitExit(t, exits)
	if info.Code != 0 || info.Err != nil {
		t.Fatalf("exit info = %+v, want cooperative exit", info)
	}
	select {
	case <-outputEnds:
	case <-time.After(2 * time.Second):
		t.Fatal("output end callback did not finish")
	}
	waitForOutput(t, outputs, "terminated")
}

func TestCloseKillsProcessGroupAfterGrace(t *testing.T) {
	const grace = 75 * time.Millisecond
	outputs := make(chan []byte, 128)
	exits := make(chan ExitInfo, 1)
	sess, err := Start(Config{
		Command:          os.Args[0],
		Args:             ptyHelperArgs("ignore"),
		Env:              []string{"DROVE_PTY_HELPER=1"},
		Size:             testSize(t),
		TerminationGrace: grace,
		OnOutput: func(chunk []byte, _ uint64) {
			outputs <- append([]byte(nil), chunk...)
		},
		OnExit: func(info ExitInfo) {
			exits <- info
		},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	waitForOutput(t, outputs, "ready")
	started := time.Now()
	if err := sess.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if elapsed := time.Since(started); elapsed < grace {
		t.Fatalf("close elapsed = %v, want at least %v", elapsed, grace)
	}
	info := waitExit(t, exits)
	if info.Err == nil || info.Code != -1 {
		t.Fatalf("exit info = %+v, want forced signal exit", info)
	}
}

func TestExitInfoOnlyReportsProcessGroupCleanupErrors(t *testing.T) {
	outputs := make(chan []byte, 128)
	exits := make(chan ExitInfo, 1)
	sess, err := Start(Config{
		Command:          os.Args[0],
		Args:             ptyHelperArgs("cooperative"),
		Env:              []string{"DROVE_PTY_HELPER=1"},
		Size:             testSize(t),
		TerminationGrace: 2 * time.Second,
		OnOutput: func(chunk []byte, _ uint64) {
			outputs <- append([]byte(nil), chunk...)
		},
		OnExit: func(info ExitInfo) {
			exits <- info
		},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForOutput(t, outputs, "ready")

	closeErr := errors.New("unrelated close failure")
	sess.mu.Lock()
	sess.closeErr = closeErr
	sess.mu.Unlock()

	if err := sess.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("close error = %v, want unrelated close failure", err)
	}
	if info := waitExit(t, exits); info.CleanupErr != nil {
		t.Fatalf(
			"exit cleanup error = %v, want only process group failures",
			info.CleanupErr,
		)
	}
}

func TestCloseReportsInaccessibleProcessGroup(t *testing.T) {
	const grace = time.Millisecond
	type signalCall struct {
		pid    int
		signal syscall.Signal
	}
	var signalsMu sync.Mutex
	var calls []signalCall
	exits := make(chan ExitInfo, 1)

	sess, err := startWithProcessGroupSignal(
		Config{
			Command:          "/bin/sh",
			Args:             []string{"-c", "exit 0"},
			Size:             testSize(t),
			TerminationGrace: grace,
			OnExit: func(info ExitInfo) {
				exits <- info
			},
		},
		func(pid int, signal syscall.Signal) error {
			signalsMu.Lock()
			calls = append(calls, signalCall{pid: pid, signal: signal})
			signalsMu.Unlock()
			return syscall.EPERM
		},
	)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	select {
	case <-sess.done:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not finish after process exit")
	}
	closeErr := sess.Close()
	if !errors.Is(closeErr, syscall.EPERM) {
		t.Fatalf("close error = %v, want EPERM", closeErr)
	}
	exit := waitExit(t, exits)
	if !errors.Is(exit.CleanupErr, syscall.EPERM) {
		t.Fatalf("exit cleanup error = %v, want EPERM", exit.CleanupErr)
	}
	for _, message := range []string{
		"pty: terminate process group",
		"pty: kill process group",
	} {
		if !strings.Contains(closeErr.Error(), message) {
			t.Fatalf("close error = %q, want %q", closeErr, message)
		}
	}

	signalsMu.Lock()
	defer signalsMu.Unlock()
	var inspected, terminated, killed bool
	for _, call := range calls {
		if call.pid <= 0 {
			t.Fatalf("process group pid = %d, want positive", call.pid)
		}
		switch call.signal {
		case 0:
			inspected = true
		case syscall.SIGTERM:
			terminated = true
		case syscall.SIGKILL:
			killed = true
		}
	}
	if !inspected || !terminated || !killed {
		t.Fatalf(
			"signals = %v, want probe, SIGTERM, and SIGKILL",
			calls,
		)
	}
}

func TestCleanupFailureClosesMasterHeldBySurvivingDescendant(t *testing.T) {
	const grace = time.Millisecond
	childPIDPath := filepath.Join(t.TempDir(), "child.pid")
	childReadyPath := filepath.Join(t.TempDir(), "child.ready")
	exits := make(chan ExitInfo, 1)
	exitStarted := make(chan struct{})
	releaseExit := make(chan struct{})
	outputEnded := make(chan struct{})

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create inherited output pipe: %v", err)
	}
	command := exec.Command(
		os.Args[0],
		ptyHelperArgs(
			"natural-group-leader",
			childPIDPath,
			childReadyPath,
		)...,
	)
	command.Env = append(os.Environ(), "DROVE_PTY_HELPER=1")
	command.Stdout = writer
	command.Stderr = writer
	if err := command.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		t.Fatalf("start process helper: %v", err)
	}
	if err := writer.Close(); err != nil {
		_ = command.Process.Kill()
		_ = reader.Close()
		t.Fatalf("close parent output writer: %v", err)
	}

	sess := &Session{
		cmd:         command,
		ptmx:        reader,
		grace:       grace,
		groupSignal: func(_ int, _ syscall.Signal) error { return syscall.EPERM },
		onOutputEnd: func(uint64) {
			close(outputEnded)
		},
		onExit: func(info ExitInfo) {
			close(exitStarted)
			<-releaseExit
			exits <- info
		},
		readDone:      make(chan struct{}),
		processExited: make(chan struct{}),
		done:          make(chan struct{}),
		WaitCh:        make(chan ExitInfo, 1),
	}
	go sess.readLoop()
	go sess.waitLoop()

	select {
	case <-sess.processExited:
	case <-time.After(2 * time.Second):
		t.Fatal("direct child did not exit")
	}
	rawPID, err := os.ReadFile(childPIDPath)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	childPID, err := strconv.Atoi(string(rawPID))
	if err != nil {
		t.Fatalf("parse child pid %q: %v", rawPID, err)
	}
	select {
	case <-exitStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("exit callback did not start")
	}
	outputPassedExitFence := false
	select {
	case <-outputEnded:
		outputPassedExitFence = true
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseExit)
	if outputPassedExitFence {
		t.Fatal("output end ran before the cleanup-failure exit callback")
	}
	childNeedsCleanup := true
	t.Cleanup(func() {
		if childNeedsCleanup {
			_ = syscall.Kill(childPID, syscall.SIGKILL)
		}
	})

	select {
	case <-sess.done:
	case <-time.After(2 * time.Second):
		_ = syscall.Kill(childPID, syscall.SIGKILL)
		childNeedsCleanup = false
		<-sess.done
		t.Fatal("session waited for an inaccessible descendant to close the PTY")
	}
	if err := syscall.Kill(childPID, 0); err != nil {
		t.Fatalf("descendant exited before PTY cleanup completed: %v", err)
	}
	if closeErr := sess.Close(); !errors.Is(closeErr, syscall.EPERM) {
		t.Fatalf("close error = %v, want EPERM", closeErr)
	}
	if exit := waitExit(t, exits); !errors.Is(exit.CleanupErr, syscall.EPERM) {
		t.Fatalf("exit cleanup error = %v, want EPERM", exit.CleanupErr)
	}

	if err := syscall.Kill(childPID, syscall.SIGKILL); err != nil &&
		!errors.Is(err, syscall.ESRCH) {
		t.Fatalf("kill descendant: %v", err)
	}
	childNeedsCleanup = false
	waitForProcessNotRunning(t, childPID)
}

func TestCloseSuppressesTermPermissionErrorAfterProcessGroupExits(t *testing.T) {
	const grace = time.Second
	var signalsMu sync.Mutex
	var signals []syscall.Signal
	probes := 0

	sess, err := startWithProcessGroupSignal(
		Config{
			Command:          "/bin/sh",
			Args:             []string{"-c", "exit 0"},
			Size:             testSize(t),
			TerminationGrace: grace,
		},
		func(_ int, signal syscall.Signal) error {
			signalsMu.Lock()
			defer signalsMu.Unlock()
			signals = append(signals, signal)
			if signal == 0 {
				probes++
				if probes > 1 {
					return syscall.ESRCH
				}
			}
			return syscall.EPERM
		},
	)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	select {
	case <-sess.done:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not finish after process group exit")
	}
	if closeErr := sess.Close(); closeErr != nil {
		t.Fatalf("close after process group exit: %v", closeErr)
	}

	signalsMu.Lock()
	defer signalsMu.Unlock()
	for _, signal := range signals {
		if signal == syscall.SIGKILL {
			t.Fatalf("signals = %v, want no SIGKILL after ESRCH", signals)
		}
	}
}

func TestCloseSuppressesTermPermissionErrorWhenKillFindsExitedGroup(t *testing.T) {
	const grace = time.Millisecond
	var signalsMu sync.Mutex
	var signals []syscall.Signal

	sess, err := startWithProcessGroupSignal(
		Config{
			Command:          "/bin/sh",
			Args:             []string{"-c", "exit 0"},
			Size:             testSize(t),
			TerminationGrace: grace,
		},
		func(_ int, signal syscall.Signal) error {
			signalsMu.Lock()
			signals = append(signals, signal)
			signalsMu.Unlock()
			if signal == syscall.SIGKILL {
				return syscall.ESRCH
			}
			return syscall.EPERM
		},
	)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	select {
	case <-sess.done:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not finish after process group exit")
	}
	if closeErr := sess.Close(); closeErr != nil {
		t.Fatalf("close after kill found exited group: %v", closeErr)
	}

	signalsMu.Lock()
	defer signalsMu.Unlock()
	if len(signals) == 0 || signals[len(signals)-1] != syscall.SIGKILL {
		t.Fatalf("signals = %v, want final SIGKILL probe", signals)
	}
}

func TestCloseSuppressesTermPermissionErrorAfterKillConfirmsExit(t *testing.T) {
	const grace = time.Millisecond
	killed := false
	postKillProbes := 0

	sess, err := startWithProcessGroupSignal(
		Config{
			Command:          "/bin/sh",
			Args:             []string{"-c", "exit 0"},
			Size:             testSize(t),
			TerminationGrace: grace,
		},
		func(_ int, signal syscall.Signal) error {
			switch {
			case signal == syscall.SIGKILL:
				killed = true
				return nil
			case signal == 0 && killed:
				postKillProbes++
				if postKillProbes >= 3 {
					return syscall.ESRCH
				}
				return nil
			default:
				return syscall.EPERM
			}
		},
	)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	select {
	case <-sess.done:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not finish after confirmed process group exit")
	}
	if closeErr := sess.Close(); closeErr != nil {
		t.Fatalf("close after confirmed process group exit: %v", closeErr)
	}
}

func TestCloseReportsProcessGroupRemainingAfterAcceptedKill(t *testing.T) {
	const grace = time.Millisecond
	exits := make(chan ExitInfo, 1)

	sess, err := startWithProcessGroupSignal(
		Config{
			Command:          "/bin/sh",
			Args:             []string{"-c", "exit 0"},
			Size:             testSize(t),
			TerminationGrace: grace,
			OnExit: func(info ExitInfo) {
				exits <- info
			},
		},
		func(_ int, _ syscall.Signal) error {
			return nil
		},
	)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	select {
	case <-sess.done:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not finish after process group cleanup failure")
	}
	closeErr := sess.Close()
	if closeErr == nil ||
		!strings.Contains(closeErr.Error(), "remains after SIGKILL") {
		t.Fatalf(
			"close error = %v, want process group remains after SIGKILL",
			closeErr,
		)
	}
	exit := waitExit(t, exits)
	if exit.CleanupErr == nil ||
		!strings.Contains(exit.CleanupErr.Error(), "remains after SIGKILL") {
		t.Fatalf(
			"exit cleanup error = %v, want process group remains after SIGKILL",
			exit.CleanupErr,
		)
	}
}

func TestCloseKillsRemainingProcessGroupChildren(t *testing.T) {
	const grace = 75 * time.Millisecond
	childPIDPath := filepath.Join(t.TempDir(), "child.pid")
	childReadyPath := filepath.Join(t.TempDir(), "child.ready")
	outputs := make(chan []byte, 128)
	sess, err := Start(Config{
		Command:          os.Args[0],
		Args:             ptyHelperArgs("group-leader", childPIDPath, childReadyPath),
		Env:              []string{"DROVE_PTY_HELPER=1"},
		Size:             testSize(t),
		TerminationGrace: grace,
		OnOutput: func(chunk []byte, _ uint64) {
			outputs <- append([]byte(nil), chunk...)
		},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	waitForOutput(t, outputs, "ready")
	rawPID, err := os.ReadFile(childPIDPath)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	childPID, err := strconv.Atoi(string(rawPID))
	if err != nil {
		t.Fatalf("parse child pid %q: %v", rawPID, err)
	}
	if err := syscall.Kill(childPID, 0); err != nil {
		t.Fatalf("surviving child is not alive before Close: %v", err)
	}
	childNeedsCleanup := true
	t.Cleanup(func() {
		if childNeedsCleanup {
			_ = syscall.Kill(childPID, syscall.SIGKILL)
		}
	})

	started := time.Now()
	if err := sess.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if elapsed := time.Since(started); elapsed < grace {
		t.Fatalf("close elapsed = %v, want at least %v", elapsed, grace)
	}
	waitForProcessExit(t, childPID)
	childNeedsCleanup = false
}

func TestCloseCleansProcessGroupAfterLeaderNaturalExit(t *testing.T) {
	const grace = 75 * time.Millisecond
	childPIDPath := filepath.Join(t.TempDir(), "child.pid")
	childReadyPath := filepath.Join(t.TempDir(), "child.ready")
	outputs := make(chan []byte, 128)
	sess, err := Start(Config{
		Command:          os.Args[0],
		Args:             ptyHelperArgs("natural-group-leader", childPIDPath, childReadyPath),
		Env:              []string{"DROVE_PTY_HELPER=1"},
		Size:             testSize(t),
		TerminationGrace: grace,
		OnOutput: func(chunk []byte, _ uint64) {
			outputs <- append([]byte(nil), chunk...)
		},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	waitForOutput(t, outputs, "ready")
	select {
	case <-sess.processExited:
	case <-time.After(2 * time.Second):
		t.Fatal("direct child did not exit")
	}
	rawPID, err := os.ReadFile(childPIDPath)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	childPID, err := strconv.Atoi(string(rawPID))
	if err != nil {
		t.Fatalf("parse child pid %q: %v", rawPID, err)
	}
	childNeedsCleanup := true
	t.Cleanup(func() {
		if childNeedsCleanup {
			_ = syscall.Kill(childPID, syscall.SIGKILL)
			_ = syscall.Kill(-sess.PID(), syscall.SIGKILL)
		}
	})

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- sess.Close()
	}()
	select {
	case closeErr := <-closeDone:
		if closeErr != nil {
			t.Fatalf("close: %v", closeErr)
		}
	case <-time.After(grace + time.Second):
		_ = syscall.Kill(-sess.PID(), syscall.SIGKILL)
		<-closeDone
		t.Fatal("close did not clean the surviving process group")
	}
	waitForProcessNotRunning(t, childPID)
	childNeedsCleanup = false
}

func TestCloseInterruptsWriteBlockedByStoppedRawProcess(t *testing.T) {
	const grace = 75 * time.Millisecond
	outputs := make(chan []byte, 16)
	sess, err := Start(Config{
		Command:          os.Args[0],
		Args:             ptyHelperArgs("stopped-raw"),
		Env:              []string{"DROVE_PTY_HELPER=1"},
		Size:             testSize(t),
		TerminationGrace: grace,
		OnOutput: func(chunk []byte, _ uint64) {
			outputs <- append([]byte(nil), chunk...)
		},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	waitForOutput(t, outputs, "ready")
	writeDone := make(chan error, 1)
	go func() {
		_, writeErr := sess.Write(bytes.Repeat([]byte{'x'}, 256*1024))
		writeDone <- writeErr
	}()
	select {
	case writeErr := <-writeDone:
		t.Fatalf("write returned before close: %v", writeErr)
	case <-time.After(50 * time.Millisecond):
	}
	if written, err := sess.Write([]byte("second")); written != 0 ||
		!errors.Is(err, ErrWriteBackpressure) {
		t.Fatalf(
			"concurrent write = (%d, %v), want (0, ErrWriteBackpressure)",
			written,
			err,
		)
	}

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- sess.Close()
	}()
	select {
	case closeErr := <-closeDone:
		if closeErr != nil {
			t.Fatalf("close: %v", closeErr)
		}
	case <-time.After(grace + time.Second):
		_ = syscall.Kill(-sess.PID(), syscall.SIGKILL)
		<-closeDone
		<-writeDone
		t.Fatal("close remained blocked behind the PTY write")
	}
	if writeErr := <-writeDone; writeErr == nil {
		t.Fatal("blocked write succeeded after close")
	}
}

func TestWriteTimesOutWithPartialCount(t *testing.T) {
	const grace = 25 * time.Millisecond
	outputs := make(chan []byte, 16)
	sess, err := Start(Config{
		Command:          os.Args[0],
		Args:             ptyHelperArgs("stopped-raw"),
		Env:              []string{"DROVE_PTY_HELPER=1"},
		Size:             testSize(t),
		TerminationGrace: grace,
		OnOutput: func(chunk []byte, _ uint64) {
			outputs <- append([]byte(nil), chunk...)
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

	waitForOutput(t, outputs, "ready")
	data := bytes.Repeat([]byte{'x'}, 256*1024)
	started := time.Now()
	written, err := sess.Write(data)
	if !errors.Is(err, ErrWriteBackpressure) {
		t.Fatalf("write error = %v, want ErrWriteBackpressure", err)
	}
	if written <= 0 || written >= len(data) {
		t.Fatalf("written bytes = %d, want a partial count below %d", written, len(data))
	}
	if elapsed := time.Since(started); elapsed < writeTimeout/2 ||
		elapsed > writeTimeout+time.Second {
		t.Fatalf("write elapsed = %v, want a bounded timeout near %v", elapsed, writeTimeout)
	}
}

func TestNaturalExitInterruptsBlockedWrite(t *testing.T) {
	outputs := make(chan []byte, 16)
	sess, err := Start(Config{
		Command: os.Args[0],
		Args:    ptyHelperArgs("raw-exit"),
		Env:     []string{"DROVE_PTY_HELPER=1"},
		Size:    testSize(t),
		OnOutput: func(chunk []byte, _ uint64) {
			outputs <- append([]byte(nil), chunk...)
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

	waitForOutput(t, outputs, "ready")
	data := bytes.Repeat([]byte{'x'}, 256*1024)
	written, err := sess.Write(data)
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("write error = %v, want ErrClosed", err)
	}
	if written <= 0 || written >= len(data) {
		t.Fatalf("written bytes = %d, want a partial count below %d", written, len(data))
	}
	select {
	case <-sess.done:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not finish after natural exit")
	}
}

func TestPTYProcessHelper(t *testing.T) {
	if os.Getenv("DROVE_PTY_HELPER") != "1" {
		return
	}
	args := helperArguments()
	if len(args) == 0 {
		os.Exit(2)
	}
	switch args[0] {
	case "cooperative":
		terms := make(chan os.Signal, 1)
		signal.Notify(terms, syscall.SIGTERM)
		_, _ = os.Stdout.WriteString("ready")
		<-terms
		_, _ = os.Stdout.WriteString("terminated")
		os.Exit(0)
	case "ignore":
		signal.Ignore(syscall.SIGTERM)
		_, _ = os.Stdout.WriteString("ready")
		for {
			time.Sleep(time.Hour)
		}
	case "stopped-raw":
		stty := exec.Command("stty", "raw", "-echo")
		stty.Stdin = os.Stdin
		stty.Stdout = os.Stdout
		stty.Stderr = os.Stderr
		if err := stty.Run(); err != nil {
			os.Exit(6)
		}
		_, _ = os.Stdout.WriteString("ready")
		if err := syscall.Kill(os.Getpid(), syscall.SIGSTOP); err != nil {
			os.Exit(7)
		}
		os.Exit(0)
	case "raw-exit":
		stty := exec.Command("stty", "raw", "-echo")
		stty.Stdin = os.Stdin
		stty.Stdout = os.Stdout
		stty.Stderr = os.Stderr
		if err := stty.Run(); err != nil {
			os.Exit(6)
		}
		exitTimer := exec.Command(
			"/bin/sh",
			"-c",
			`sleep 0.1; kill -KILL "$1"`,
			"pty-exit-timer",
			strconv.Itoa(os.Getpid()),
		)
		exitTimer.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := exitTimer.Start(); err != nil {
			os.Exit(8)
		}
		_, _ = os.Stdout.WriteString("ready")
		for {
			time.Sleep(time.Hour)
		}
	case "group-leader":
		if len(args) != 3 {
			os.Exit(2)
		}
		child := exec.Command(
			os.Args[0],
			ptyHelperArgs("group-child", args[2])...,
		)
		child.Env = os.Environ()
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		if err := os.WriteFile(
			args[1],
			[]byte(strconv.Itoa(child.Process.Pid)),
			0o600,
		); err != nil {
			os.Exit(4)
		}
		for {
			if _, err := os.Stat(args[2]); err == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
		terms := make(chan os.Signal, 1)
		signal.Notify(terms, syscall.SIGTERM)
		_, _ = os.Stdout.WriteString("ready")
		<-terms
		os.Exit(0)
	case "natural-group-leader":
		if len(args) != 3 {
			os.Exit(2)
		}
		child := exec.Command(
			os.Args[0],
			ptyHelperArgs("group-child", args[2])...,
		)
		child.Env = os.Environ()
		child.Stdin = os.Stdin
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		if err := os.WriteFile(
			args[1],
			[]byte(strconv.Itoa(child.Process.Pid)),
			0o600,
		); err != nil {
			os.Exit(4)
		}
		for {
			if _, err := os.Stat(args[2]); err == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
		_, _ = os.Stdout.WriteString("ready")
		os.Exit(0)
	case "group-child":
		if len(args) != 2 {
			os.Exit(2)
		}
		signal.Ignore(syscall.SIGTERM, syscall.SIGHUP)
		if err := os.WriteFile(args[1], nil, 0o600); err != nil {
			os.Exit(5)
		}
		for {
			time.Sleep(time.Hour)
		}
	default:
		os.Exit(2)
	}
}

func TestStartFailureInvokesNoCallback(t *testing.T) {
	var outputCalls atomic.Int32
	var exitCalls atomic.Int32

	sess, err := Start(Config{
		Command: filepath.Join(t.TempDir(), "missing-command"),
		Size:    testSize(t),
		OnOutput: func([]byte, uint64) {
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

func TestReadOutputPreservesUTF8AcrossShortReads(t *testing.T) {
	reader := &scriptedReader{chunks: [][]byte{
		[]byte("A\xe4"),
		[]byte("\xb8"),
		[]byte("\x96B"),
	}}
	var records []outputRecord
	var endOffsets []uint64
	readOutput(
		reader,
		func(chunk []byte, offset uint64) {
			records = append(records, outputRecord{data: chunk, offset: offset})
		},
		func(offset uint64) {
			endOffsets = append(endOffsets, offset)
		},
	)

	var got []byte
	var offset uint64
	for _, record := range records {
		if record.offset != offset {
			t.Fatalf("record offset = %d, want %d", record.offset, offset)
		}
		if !utf8.Valid(record.data) {
			t.Fatalf("normal output split UTF-8: %x", record.data)
		}
		got = append(got, record.data...)
		offset += uint64(len(record.data))
	}
	if string(got) != "A世B" {
		t.Fatalf("output = %q, want A世B", got)
	}
	if len(endOffsets) != 1 || endOffsets[0] != uint64(len(got)) {
		t.Fatalf("end offsets = %v, want [%d]", endOffsets, len(got))
	}
}

func TestReadOutputPreservesInvalidAndIncompleteFinalBytes(t *testing.T) {
	input := []byte{'a', 0xff, 0xe4, 0xb8}
	reader := &scriptedReader{chunks: [][]byte{
		input[:2],
		input[2:],
	}}
	var got []byte
	var endOffset uint64
	readOutput(
		reader,
		func(chunk []byte, _ uint64) {
			got = append(got, chunk...)
		},
		func(offset uint64) {
			endOffset = offset
		},
	)
	if !bytes.Equal(got, input) {
		t.Fatalf("output = %x, want %x", got, input)
	}
	if endOffset != uint64(len(input)) {
		t.Fatalf("end offset = %d, want %d", endOffset, len(input))
	}
}

func TestReadOutputCapsChunksAndCopiesCallbackBytes(t *testing.T) {
	first := bytes.Repeat([]byte{'a'}, outputReadBufferSize-1)
	first = append(first, 0xe4)
	reader := &scriptedReader{chunks: [][]byte{
		first,
		{0xb8, 0x96, 'z'},
	}}
	var records []outputRecord
	readOutput(reader, func(chunk []byte, offset uint64) {
		records = append(records, outputRecord{data: chunk, offset: offset})
	}, nil)

	var got []byte
	for _, record := range records {
		if len(record.data) > outputReadBufferSize {
			t.Fatalf("chunk length = %d, max %d", len(record.data), outputReadBufferSize)
		}
		if !utf8.Valid(record.data) {
			t.Fatalf("chunk split UTF-8: suffix %x", record.data[len(record.data)-4:])
		}
		got = append(got, record.data...)
	}
	want := append(append([]byte(nil), first...), 0xb8, 0x96, 'z')
	if !bytes.Equal(got, want) {
		t.Fatalf("output length = %d, want %d", len(got), len(want))
	}
	reader.chunks[0][0] = 'x'
	if records[0].data[0] != 'a' {
		t.Fatal("callback bytes alias reader input")
	}
}

type scriptedReader struct {
	chunks [][]byte
	index  int
}

func (r *scriptedReader) Read(buffer []byte) (int, error) {
	if r.index >= len(r.chunks) {
		return 0, io.EOF
	}
	chunk := r.chunks[r.index]
	r.index++
	return copy(buffer, chunk), nil
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

func waitForOutput(t *testing.T, outputs <-chan []byte, want string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	var observed []byte
	for {
		select {
		case chunk := <-outputs:
			observed = append(observed, chunk...)
			if strings.Contains(string(observed), want) {
				return
			}
		case <-deadline:
			t.Fatalf("output = %q, want substring %q", observed, want)
		}
	}
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			t.Fatalf("inspect child process %d: %v", pid, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("child process %d is still alive", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForProcessNotRunning(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			t.Fatalf("inspect child process %d: %v", pid, err)
		}
		status, statusErr := exec.Command(
			"ps",
			"-o",
			"stat=",
			"-p",
			strconv.Itoa(pid),
		).Output()
		if statusErr != nil || strings.HasPrefix(
			strings.TrimSpace(string(status)),
			"Z",
		) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("child process %d is still running", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func ptyHelperArgs(mode string, args ...string) []string {
	result := []string{"-test.run=^TestPTYProcessHelper$", "--", mode}
	return append(result, args...)
}

func helperArguments() []string {
	for index, arg := range os.Args {
		if arg == "--" {
			return os.Args[index+1:]
		}
	}
	return nil
}

func testSize(t *testing.T) Size {
	t.Helper()
	size, err := NewSize(40, 120)
	if err != nil {
		t.Fatalf("new test size: %v", err)
	}
	return size
}
