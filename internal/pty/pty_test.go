package pty

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

func testSize(t *testing.T) Size {
	t.Helper()
	size, err := NewSize(40, 120)
	if err != nil {
		t.Fatalf("new test size: %v", err)
	}
	return size
}
