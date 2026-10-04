package recording

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
)

func TestRawTailReadsOffsetSuffixThenFollowsSparseCommits(t *testing.T) {
	st := openTailStore(t)
	base := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	appendTailRows(t, st, 0, []store.EventRow{
		tailEventRow(1, base, "s1", event.TypeSessionLifecycle, "created", ""),
		tailEventRow(2, base.Add(time.Second), "other", event.TypeSessionLifecycle, "created", ""),
		tailOutputRow(3, base.Add(2*time.Second), "s1", 0, []byte("abc")),
		tailEventRow(
			4,
			base.Add(3*time.Second),
			"s1",
			event.TypeAgentResized,
			"",
			`{"version":1,"rows":50,"columns":160,"output_offset":3}`,
		),
	})
	clock := newTailClock(4)
	offset := OutputOffset(1)
	selector, err := NewSelector(SelectorInput{Offset: &offset})
	if err != nil {
		t.Fatalf("new selector: %v", err)
	}
	tail, err := NewArchive(st, clock).TailRaw(context.Background(), "s1", &selector)
	if err != nil {
		t.Fatalf("tail raw: %v", err)
	}
	t.Cleanup(func() {
		_ = tail.Close()
	})

	output := nextRawOutput(t, tail)
	if output.Sequence != 3 ||
		output.Offset != 1 ||
		!bytes.Equal(output.Data, []byte("bc")) ||
		output.Cursor != (Cursor{Seq: 3, NextOffset: 3}) ||
		!output.Historical {
		t.Fatalf("historical output = %+v", output)
	}
	resize := nextResize(t, tail)
	if resize.Sequence != 4 ||
		resize.Rows != 50 ||
		resize.Columns != 160 ||
		resize.OutputOffset != 3 ||
		resize.Cursor != (Cursor{Seq: 4, NextOffset: 3}) ||
		!resize.Historical {
		t.Fatalf("historical resize = %+v", resize)
	}
	caughtUp := nextRawCaughtUp(t, tail)
	if caughtUp.Cursor != (Cursor{Seq: 4, NextOffset: 3}) {
		t.Fatalf("caught-up cursor = %+v", caughtUp.Cursor)
	}

	appendTailRows(t, st, 4, []store.EventRow{
		tailEventRow(5, base.Add(4*time.Second), "other", event.TypeError, "", "other"),
	})
	clock.Advance(5)
	result := make(chan RawItem, 1)
	resultErr := make(chan error, 1)
	go func() {
		item, nextErr := tail.Next()
		if nextErr != nil {
			resultErr <- nextErr
			return
		}
		result <- item
	}()
	select {
	case item := <-result:
		t.Fatalf("tail emitted item for another session: %+v", item)
	case nextErr := <-resultErr:
		t.Fatalf("tail failed while scanning sparse range: %v", nextErr)
	case <-time.After(25 * time.Millisecond):
	}

	appendTailRows(t, st, 5, []store.EventRow{
		tailOutputRow(6, base.Add(5*time.Second), "s1", 3, []byte("de")),
	})
	clock.Advance(6)
	select {
	case item := <-result:
		output, ok := item.(RawOutput)
		if !ok {
			t.Fatalf("live item = %T, want RawOutput", item)
		}
		if output.Sequence != 6 ||
			output.Offset != 3 ||
			!bytes.Equal(output.Data, []byte("de")) ||
			output.Cursor != (Cursor{Seq: 6, NextOffset: 5}) ||
			output.Historical {
			t.Fatalf("live output = %+v", output)
		}
	case nextErr := <-resultErr:
		t.Fatalf("tail live output: %v", nextErr)
	case <-time.After(time.Second):
		t.Fatal("tail did not wake for live output")
	}
}

func TestRawTailReportsExpiredOutputBeforeLaterBytes(t *testing.T) {
	st := openTailStore(t)
	base := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	appendTailRows(t, st, 0, []store.EventRow{
		tailEventRow(1, base, "s1", event.TypeSessionLifecycle, "created", ""),
		tailOutputRow(2, base.Add(time.Second), "s1", 0, []byte("old")),
		tailOutputRow(3, base.Add(3*time.Second), "s1", 3, []byte("new")),
	})
	deleted, err := st.PruneOutputAttachments(
		context.Background(),
		base.Add(2*time.Second),
	)
	if err != nil {
		t.Fatalf("prune output: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted attachments = %d, want 1", deleted)
	}

	tail, err := NewArchive(st, newTailClock(3)).TailRaw(
		context.Background(),
		"s1",
		nil,
	)
	if err != nil {
		t.Fatalf("tail raw: %v", err)
	}
	t.Cleanup(func() {
		_ = tail.Close()
	})
	_, err = tail.Next()
	var expired *OutputExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("next error = %v, want OutputExpiredError", err)
	}
	want := []OutputRange{{Start: 0, End: 3}}
	if expired.SessionID != "s1" || !equalOutputRanges(expired.Missing, want) {
		t.Fatalf("expired output = %+v, want session s1 and %+v", expired, want)
	}
	if _, err := tail.Next(); !errors.As(err, &expired) {
		t.Fatalf("second next error = %v, want same terminal expiry", err)
	}
}

func TestEventTailPreservesEnvelopesAndCancellationIsIndependent(t *testing.T) {
	st := openTailStore(t)
	base := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	appendTailRows(t, st, 0, []store.EventRow{
		tailEventRow(1, base, "s1", event.TypeSessionLifecycle, "created", `{"version":1}`),
	})
	clock := newTailClock(1)
	archive := NewArchive(st, clock)
	first, err := archive.TailEvents(context.Background(), "s1", nil)
	if err != nil {
		t.Fatalf("first event tail: %v", err)
	}
	second, err := archive.TailEvents(context.Background(), "s1", nil)
	if err != nil {
		t.Fatalf("second event tail: %v", err)
	}
	t.Cleanup(func() {
		_ = first.Close()
		_ = second.Close()
	})

	record := nextEventRecord(t, first)
	if record.Event.Seq != 1 ||
		record.Event.Timestamp != base ||
		record.Event.Type != event.TypeSessionLifecycle ||
		record.Event.Reason != "created" ||
		record.Event.Payload != `{"version":1}` ||
		record.Cursor != (Cursor{Seq: 1}) ||
		!record.Historical {
		t.Fatalf("event record = %+v", record)
	}
	_ = nextEventRecord(t, second)
	_ = nextEventCaughtUp(t, first)
	_ = nextEventCaughtUp(t, second)

	if err := first.Close(); err != nil {
		t.Fatalf("close first tail: %v", err)
	}
	if _, err := first.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("next closed first tail = %v, want EOF", err)
	}

	appendTailRows(t, st, 1, []store.EventRow{
		tailEventRow(2, base.Add(time.Second), "s1", event.TypeError, "", "failed"),
	})
	clock.Advance(2)
	live := nextEventRecord(t, second)
	if live.Event.Seq != 2 || live.Event.Payload != "failed" || live.Historical {
		t.Fatalf("live event = %+v", live)
	}
}

func TestEventTailRedactsPrivateSessionMetadata(t *testing.T) {
	st := openTailStore(t)
	base := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	appendTailRows(t, st, 0, []store.EventRow{
		tailEventRow(
			1,
			base,
			"s1",
			event.TypeSessionLifecycle,
			"created",
			`{"version":2,"name":"agent","vendor":"claude","working_dir":"/private/project"}`,
		),
		tailEventRow(
			2,
			base.Add(time.Second),
			"s1",
			event.TypeAgentSignal,
			"observed",
			`{"version":1,"source":"hook","kind":"session_started","vendor":"claude",`+
				`"vendor_event":"SessionStart","scope":"root","vendor_session_ref":"vendor-ref",`+
				`"confidence":1,"received_at":"2026-10-04T12:00:01Z",`+
				`"delivery_id":"550e8400-e29b-41d4-a716-446655440000","outcome":"observed"}`,
		),
		tailEventRow(
			3,
			base.Add(2*time.Second),
			"s1",
			event.TypeAgentResumed,
			"requested",
			`{"version":1,"vendor_session_ref":"vendor-ref"}`,
		),
	})
	tail, err := NewArchive(st, newTailClock(3)).TailEvents(
		context.Background(),
		"s1",
		nil,
	)
	if err != nil {
		t.Fatalf("tail events: %v", err)
	}
	t.Cleanup(func() {
		_ = tail.Close()
	})

	records := []EventRecord{
		nextEventRecord(t, tail),
		nextEventRecord(t, tail),
		nextEventRecord(t, tail),
	}
	for _, private := range []string{
		"/private/project",
		"vendor-ref",
		"working_dir",
		"vendor_session_ref",
		"vendor_session_id",
	} {
		for _, record := range records {
			if strings.Contains(record.Event.Payload, private) {
				t.Fatalf(
					"event %d exposed private value %q in %s",
					record.Event.Seq,
					private,
					record.Event.Payload,
				)
			}
		}
	}
}

func TestTailRejectsUnknownSessionAndMixedCursor(t *testing.T) {
	st := openTailStore(t)
	base := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	appendTailRows(t, st, 0, []store.EventRow{
		tailEventRow(1, base, "s1", event.TypeSessionLifecycle, "created", ""),
		tailOutputRow(2, base.Add(time.Second), "s1", 0, []byte("abc")),
	})
	archive := NewArchive(st, newTailClock(2))
	if _, err := archive.TailRaw(
		context.Background(),
		"missing",
		nil,
	); !errors.Is(err, ErrUnknownSession) {
		t.Fatalf("unknown session error = %v", err)
	}

	cursor := Cursor{Seq: 2, NextOffset: 2}
	selector, err := NewSelector(SelectorInput{Cursor: &cursor})
	if err != nil {
		t.Fatalf("new cursor selector: %v", err)
	}
	if _, err := archive.TailRaw(
		context.Background(),
		"s1",
		&selector,
	); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("mixed cursor error = %v", err)
	}
}

type tailClock struct {
	mu      sync.Mutex
	high    uint64
	changed chan struct{}
}

func newTailClock(high uint64) *tailClock {
	return &tailClock{high: high, changed: make(chan struct{})}
}

func (c *tailClock) HighWatermark() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.high
}

func (c *tailClock) WaitForCommit(ctx context.Context, after uint64) (uint64, error) {
	for {
		c.mu.Lock()
		if c.high > after {
			high := c.high
			c.mu.Unlock()
			return high, nil
		}
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-changed:
		}
	}
}

func (c *tailClock) Advance(high uint64) {
	c.mu.Lock()
	if high > c.high {
		c.high = high
		close(c.changed)
		c.changed = make(chan struct{})
	}
	c.mu.Unlock()
}

func openTailStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "tail.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return st
}

func appendTailRows(
	t *testing.T,
	st *store.Store,
	after uint64,
	rows []store.EventRow,
) {
	t.Helper()
	if _, err := st.AppendEvents(context.Background(), after, rows); err != nil {
		t.Fatalf("append tail rows after %d: %v", after, err)
	}
}

func tailEventRow(
	sequence uint64,
	at time.Time,
	sessionID string,
	eventType event.Type,
	reason string,
	payload string,
) store.EventRow {
	return store.EventRow{
		Seq:       sequence,
		Timestamp: at,
		Type:      string(eventType),
		SessionID: sessionID,
		AgentID:   sessionID,
		Reason:    reason,
		Payload:   payload,
	}
}

func tailOutputRow(
	sequence uint64,
	at time.Time,
	sessionID string,
	offset uint64,
	data []byte,
) store.EventRow {
	return store.EventRow{
		Seq:              sequence,
		Timestamp:        at,
		Type:             string(event.TypeOutputChunk),
		SessionID:        sessionID,
		AgentID:          sessionID,
		Payload:          outputMetadata(offset, len(data)),
		OutputAttachment: append([]byte(nil), data...),
	}
}

func outputMetadata(offset uint64, length int) string {
	return `{"version":1,"offset":` +
		OutputOffset(offset).String() +
		`,"len":` +
		Seq(length).String() +
		`}`
}

func nextRawOutput(t *testing.T, tail *RawTail) RawOutput {
	t.Helper()
	item, err := tail.Next()
	if err != nil {
		t.Fatalf("next raw output: %v", err)
	}
	output, ok := item.(RawOutput)
	if !ok {
		t.Fatalf("raw item = %T, want RawOutput", item)
	}
	return output
}

func nextResize(t *testing.T, tail *RawTail) Resize {
	t.Helper()
	item, err := tail.Next()
	if err != nil {
		t.Fatalf("next resize: %v", err)
	}
	resize, ok := item.(Resize)
	if !ok {
		t.Fatalf("raw item = %T, want Resize", item)
	}
	return resize
}

func nextRawCaughtUp(t *testing.T, tail *RawTail) CaughtUp {
	t.Helper()
	item, err := tail.Next()
	if err != nil {
		t.Fatalf("next raw caught-up: %v", err)
	}
	caughtUp, ok := item.(CaughtUp)
	if !ok {
		t.Fatalf("raw item = %T, want CaughtUp", item)
	}
	return caughtUp
}

func nextEventRecord(t *testing.T, tail *EventTail) EventRecord {
	t.Helper()
	item, err := tail.Next()
	if err != nil {
		t.Fatalf("next event record: %v", err)
	}
	record, ok := item.(EventRecord)
	if !ok {
		t.Fatalf("event item = %T, want EventRecord", item)
	}
	return record
}

func nextEventCaughtUp(t *testing.T, tail *EventTail) CaughtUp {
	t.Helper()
	item, err := tail.Next()
	if err != nil {
		t.Fatalf("next event caught-up: %v", err)
	}
	caughtUp, ok := item.(CaughtUp)
	if !ok {
		t.Fatalf("event item = %T, want CaughtUp", item)
	}
	return caughtUp
}

func equalOutputRanges(left, right []OutputRange) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
