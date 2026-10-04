package term

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

const controllerTestDeadline = time.Second

func TestSizeValidation(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		rows    int
		columns int
		wantErr bool
	}{
		{name: "valid", rows: 40, columns: 120},
		{name: "zero rows", rows: 0, columns: 120, wantErr: true},
		{name: "zero columns", rows: 40, columns: 0, wantErr: true},
		{name: "negative rows", rows: -1, columns: 120, wantErr: true},
		{name: "rows overflow", rows: maxTerminalDimension + 1, columns: 120, wantErr: true},
		{name: "columns overflow", rows: 40, columns: maxTerminalDimension + 1, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			size, err := NewSize(test.rows, test.columns)
			if test.wantErr {
				if err == nil {
					t.Fatal("NewSize returned nil error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewSize: %v", err)
			}
			if size.Rows() != test.rows || size.Columns() != test.columns {
				t.Fatalf("size = %dx%d, want %dx%d", size.Rows(), size.Columns(), test.rows, test.columns)
			}
		})
	}
}

func TestCommittedChunkCopiesAndValidates(t *testing.T) {
	t.Parallel()

	committedAt := time.Now().UTC()
	input := []byte("redacted")
	chunk, err := NewCommittedChunk(input, 21, 9, committedAt)
	if err != nil {
		t.Fatalf("NewCommittedChunk: %v", err)
	}
	input[0] = 'X'
	first := chunk.Bytes()
	if string(first) != "redacted" {
		t.Fatalf("Bytes = %q, want redacted", first)
	}
	first[0] = 'Y'
	if got := string(chunk.Bytes()); got != "redacted" {
		t.Fatalf("second Bytes = %q, want redacted", got)
	}
	if chunk.OutputOffset() != 21 {
		t.Fatalf("OutputOffset = %d, want 21", chunk.OutputOffset())
	}
	if chunk.LastSeq() != 9 {
		t.Fatalf("LastSeq = %d, want 9", chunk.LastSeq())
	}
	if !chunk.CommittedAt().Equal(committedAt) {
		t.Fatalf("CommittedAt = %v, want %v", chunk.CommittedAt(), committedAt)
	}

	for _, test := range []struct {
		name         string
		data         []byte
		outputOffset uint64
		lastSeq      uint64
		committedAt  time.Time
	}{
		{name: "empty bytes", outputOffset: 1, lastSeq: 1, committedAt: committedAt},
		{name: "offset before chunk", data: []byte("abc"), outputOffset: 2, lastSeq: 1, committedAt: committedAt},
		{name: "zero sequence", data: []byte("a"), outputOffset: 1, committedAt: committedAt},
		{name: "zero time", data: []byte("a"), outputOffset: 1, lastSeq: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewCommittedChunk(
				test.data,
				test.outputOffset,
				test.lastSeq,
				test.committedAt,
			); err == nil {
				t.Fatal("NewCommittedChunk returned nil error")
			}
		})
	}
}

func TestViewOptionsValidation(t *testing.T) {
	t.Parallel()

	if _, err := NewViewOptions(MaxViewRows, MaxViewCellsPerRow, MaxViewBytes); err != nil {
		t.Fatalf("NewViewOptions maximums: %v", err)
	}
	for _, test := range []struct {
		rows  int
		cells int
		bytes int
	}{
		{rows: 0, cells: 1, bytes: 1},
		{rows: MaxViewRows + 1, cells: 1, bytes: 1},
		{rows: 1, cells: 0, bytes: 1},
		{rows: 1, cells: MaxViewCellsPerRow + 1, bytes: 1},
		{rows: 1, cells: 1, bytes: 0},
		{rows: 1, cells: 1, bytes: MaxViewBytes + 1},
	} {
		if _, err := NewViewOptions(test.rows, test.cells, test.bytes); err == nil {
			t.Fatalf("NewViewOptions(%d, %d, %d) returned nil error", test.rows, test.cells, test.bytes)
		}
	}
}

func TestControllerDSRReply(t *testing.T) {
	controller, replies := newReplyController(t, mustSize(t, 10, 20))
	fixture := loadQueryFixture(t, "dsr")
	writeController(t, controller, fixture.Input)
	assertReply(t, replies, fixture.Reply)
	closeController(t, controller)
}

func TestControllerOSCColorReplies(t *testing.T) {
	controller, replies := newReplyController(t, mustSize(t, 10, 20))

	for _, name := range []string{"osc_10", "osc_11"} {
		fixture := loadQueryFixture(t, name)
		writeController(t, controller, fixture.Input)
		assertReply(t, replies, fixture.Reply)
	}

	closeController(t, controller)
}

func TestControllerDeviceAttributesReply(t *testing.T) {
	controller, replies := newReplyController(t, mustSize(t, 10, 20))
	fixture := loadQueryFixture(t, "device_attributes")
	writeController(t, controller, fixture.Input)
	assertReply(t, replies, fixture.Reply)
	closeController(t, controller)
}

func TestControllerKittyKeyboardReply(t *testing.T) {
	controller, replies := newReplyController(t, mustSize(t, 10, 20))
	fixture := loadQueryFixture(t, "kitty_keyboard")
	writeController(t, controller, fixture.Input)
	assertReply(t, replies, fixture.Reply)
	closeController(t, controller)
}

func TestControllerReplyBytesDoNotEnterSnapshot(t *testing.T) {
	controller, replies := newReplyController(t, mustSize(t, 2, 20))
	writeController(t, controller, "ready\x1b[6n")
	assertReply(t, replies, "\x1b[1;6R")

	snapshot, err := controller.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	row, ok := snapshot.Row(0)
	if !ok {
		t.Fatal("snapshot row 0 is missing")
	}
	if row != "ready" {
		t.Fatalf("snapshot row = %q, want ready", row)
	}
	closeController(t, controller)
}

func TestControllerDrainContinuesAfterSinkError(t *testing.T) {
	var calls atomic.Int32
	sinkErr := errors.New("PTY closed")
	controller, err := NewController(mustSize(t, 10, 20), func([]byte) error {
		calls.Add(1)
		return sinkErr
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	writeController(t, controller, "\x1b[6n")
	writeController(t, controller, "\x1b[c")
	waitFor(t, func() bool { return calls.Load() == 2 }, "two reply sink calls")

	select {
	case got := <-controller.Errors():
		if !errors.Is(got, sinkErr) {
			t.Fatalf("reply error = %v, want %v", got, sinkErr)
		}
	case <-time.After(controllerTestDeadline):
		t.Fatal("timed out waiting for reply error")
	}
	closeController(t, controller)
}

func TestControllerCloseIsIdempotentAndJoinsPump(t *testing.T) {
	controller, _ := newReplyController(t, mustSize(t, 10, 20))

	const closers = 16
	var wait sync.WaitGroup
	errorsSeen := make(chan error, closers)
	wait.Add(closers)
	for range closers {
		go func() {
			defer wait.Done()
			errorsSeen <- controller.Close()
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	if _, open := <-controller.Errors(); open {
		t.Fatal("reply error channel remains open after Close")
	}
	if err := controller.Write([]byte("late")); !errors.Is(err, ErrClosed) {
		t.Fatalf("Write after close = %v, want ErrClosed", err)
	}
	if _, err := controller.Snapshot(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Snapshot after close = %v, want ErrClosed", err)
	}
	if err := controller.Resize(mustSize(t, 5, 5)); !errors.Is(err, ErrClosed) {
		t.Fatalf("Resize after close = %v, want ErrClosed", err)
	}
}

func TestControllerResize(t *testing.T) {
	controller, _ := newReplyController(t, mustSize(t, 4, 8))
	writeController(t, controller, "content")

	want := mustSize(t, 2, 5)
	if err := controller.Resize(want); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	snapshot, err := controller.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Size() != want {
		t.Fatalf(
			"snapshot size = %dx%d, want %dx%d",
			snapshot.Size().Rows(),
			snapshot.Size().Columns(),
			want.Rows(),
			want.Columns(),
		)
	}
	if err := controller.Resize(Size{}); err == nil {
		t.Fatal("Resize accepted a zero size")
	}
	closeController(t, controller)
}

func TestSnapshotIsImmutableAndOmitsMetadata(t *testing.T) {
	controller, _ := newReplyController(t, mustSize(t, 2, 20))
	writeController(t, controller, "\x1b]0;private title\x07\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\")

	snapshot, err := controller.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	writeController(t, controller, "\rnew")

	row, ok := snapshot.Row(0)
	if !ok {
		t.Fatal("snapshot row 0 is missing")
	}
	if row != "link" {
		t.Fatalf("snapshot row = %q, want link", row)
	}
	closeController(t, controller)
}

func TestSnapshotViewBoundsRowsAndCells(t *testing.T) {
	controller, _ := newReplyController(t, mustSize(t, 14, 200))
	var output strings.Builder
	for row := 1; row <= 14; row++ {
		fmt.Fprintf(&output, "\x1b[%d;1Hrow-%02d", row, row)
	}
	output.WriteString("\x1b[14;1H")
	output.WriteString(strings.Repeat("x", 170))
	writeController(t, controller, output.String())

	snapshot, err := controller.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	view, err := snapshot.View(DefaultViewOptions())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	rows := view.Rows()
	if len(rows) != MaxViewRows {
		t.Fatalf("view rows = %d, want %d", len(rows), MaxViewRows)
	}
	if rows[0] != "row-03" {
		t.Fatalf("first row = %q, want row-03", rows[0])
	}
	if len(rows[len(rows)-1]) != MaxViewCellsPerRow {
		t.Fatalf("last row bytes = %d, want %d", len(rows[len(rows)-1]), MaxViewCellsPerRow)
	}
	if !view.Truncated() {
		t.Fatal("View did not report row and cell truncation")
	}
	rows[0] = "changed"
	if next := view.Rows()[0]; next != "row-03" {
		t.Fatalf("Rows exposed mutable storage: %q", next)
	}
	closeController(t, controller)
}

func TestSnapshotViewBoundsUTF8Bytes(t *testing.T) {
	controller, _ := newReplyController(t, mustSize(t, 1, MaxViewCellsPerRow))
	grapheme := "a" + strings.Repeat("\u0301", 20)
	writeController(t, controller, strings.Repeat(grapheme, MaxViewCellsPerRow))

	snapshot, err := controller.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	view, err := snapshot.View(DefaultViewOptions())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	rows := view.Rows()
	if len(rows) != 1 {
		t.Fatalf("view rows = %d, want 1", len(rows))
	}
	if len(rows[0]) > MaxViewBytes {
		t.Fatalf("view bytes = %d, limit %d", len(rows[0]), MaxViewBytes)
	}
	if !utf8.ValidString(rows[0]) {
		t.Fatal("view split UTF-8 content")
	}
	if !view.Truncated() {
		t.Fatal("View did not report byte truncation")
	}
	closeController(t, controller)
}

func TestSnapshotViewDoesNotSplitWideCell(t *testing.T) {
	controller, _ := newReplyController(t, mustSize(t, 1, 4))
	writeController(t, controller, "界x")

	snapshot, err := controller.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	row, ok := snapshot.Row(0)
	if !ok || row != "界x" {
		t.Fatalf("snapshot row = %q, %v, want 界x, true", row, ok)
	}
	options, err := NewViewOptions(1, 1, MaxViewBytes)
	if err != nil {
		t.Fatalf("NewViewOptions: %v", err)
	}
	view, err := snapshot.View(options)
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if rows := view.Rows(); len(rows) != 1 || rows[0] != "" {
		t.Fatalf("view rows = %#v, want one empty row", rows)
	}
	if !view.Truncated() {
		t.Fatal("View did not report a split wide cell as truncated")
	}
	closeController(t, controller)
}

func TestSnapshotViewRejectsZeroOptions(t *testing.T) {
	var snapshot Snapshot
	if _, err := snapshot.View(ViewOptions{}); err == nil {
		t.Fatal("View accepted zero options")
	}
}

func newReplyController(t *testing.T, size Size) (*Controller, <-chan []byte) {
	t.Helper()
	replies := make(chan []byte, 16)
	controller, err := NewController(size, func(frame []byte) error {
		replies <- append([]byte(nil), frame...)
		return nil
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	return controller, replies
}

func mustSize(t *testing.T, rows, columns int) Size {
	t.Helper()
	size, err := NewSize(rows, columns)
	if err != nil {
		t.Fatalf("NewSize: %v", err)
	}
	return size
}

func writeController(t *testing.T, controller *Controller, data string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- controller.Write([]byte(data))
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
	case <-time.After(controllerTestDeadline):
		t.Fatalf("Write timed out for %q", []byte(data))
	}
}

func assertReply(t *testing.T, replies <-chan []byte, want string) {
	t.Helper()
	select {
	case got := <-replies:
		if !bytes.Equal(got, []byte(want)) {
			t.Fatalf("reply = %q, want %q", got, want)
		}
	case <-time.After(controllerTestDeadline):
		t.Fatalf("timed out waiting for reply %q", want)
	}
}

func closeController(t *testing.T, controller *Controller) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- controller.Close()
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(controllerTestDeadline):
		t.Fatal("Close timed out")
	}
}

func waitFor(t *testing.T, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(controllerTestDeadline)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(time.Millisecond)
	}
}

type queryFixture struct {
	Input string `json:"input"`
	Reply string `json:"reply"`
}

func loadQueryFixture(t *testing.T, name string) queryFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/query_replies.json")
	if err != nil {
		t.Fatalf("read query fixtures: %v", err)
	}
	var fixtures map[string]queryFixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("decode query fixtures: %v", err)
	}
	fixture, ok := fixtures[name]
	if !ok {
		t.Fatalf("query fixture %q is missing", name)
	}
	return fixture
}
