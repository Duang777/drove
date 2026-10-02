package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScanEventsReturnsZeroForEmptyStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	visits := 0
	lastSeq, err := s.ScanEvents(context.Background(), func(EventRow) error {
		visits++
		return nil
	})
	if err != nil {
		t.Fatalf("scan events: %v", err)
	}
	if lastSeq != 0 || visits != 0 {
		t.Fatalf("empty scan = (last seq %d, visits %d), want (0, 0)", lastSeq, visits)
	}
}

func TestScanEventsWrapsVisitorErrorWithSequence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	for _, seq := range []uint64{4, 9} {
		if err := s.AppendEvent(EventRow{Seq: seq, Timestamp: time.Now().UTC(), Type: "output", SessionID: "s1"}); err != nil {
			t.Fatalf("append seq %d: %v", seq, err)
		}
	}

	visitorErr := errors.New("projection failed")
	visits := 0
	_, err = s.ScanEvents(context.Background(), func(EventRow) error {
		visits++
		return visitorErr
	})
	if !errors.Is(err, visitorErr) {
		t.Fatalf("scan error = %v, want wrapped visitor error", err)
	}
	if !strings.Contains(err.Error(), "seq 4") {
		t.Fatalf("error = %q, want sequence context", err)
	}
	if visits != 1 {
		t.Fatalf("visitor called %d times, want 1", visits)
	}
}

func TestScanEventsRejectsMalformedTimestamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	_, err = s.db.Exec(
		`INSERT INTO events (seq, ts, type, session_id) VALUES (?, ?, ?, ?)`,
		3, "not-a-timestamp", "output", "s1",
	)
	if err != nil {
		t.Fatalf("insert malformed row: %v", err)
	}

	_, err = s.ScanEvents(context.Background(), func(EventRow) error { return nil })
	if err == nil {
		t.Fatal("scan events succeeded with a malformed timestamp")
	}
	if !strings.Contains(err.Error(), "seq 3") {
		t.Fatalf("error = %q, want sequence context", err)
	}
}

func TestScanEventsRejectsSequenceZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if err := s.AppendEvent(EventRow{Seq: 0, Timestamp: time.Now().UTC(), Type: "output", SessionID: "s1"}); err != nil {
		t.Fatalf("append sequence zero: %v", err)
	}

	_, err = s.ScanEvents(context.Background(), func(EventRow) error { return nil })
	if err == nil {
		t.Fatal("scan events succeeded with sequence zero")
	}
	if !strings.Contains(err.Error(), "seq 0") {
		t.Fatalf("error = %q, want sequence context", err)
	}
}

func TestScanEventsRejectsNegativeSequence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	_, err = s.db.Exec(
		`INSERT INTO events (seq, ts, type, session_id) VALUES (?, ?, ?, ?)`,
		-1, time.Now().UTC().Format(time.RFC3339Nano), "output", "s1",
	)
	if err != nil {
		t.Fatalf("insert negative sequence: %v", err)
	}

	_, err = s.ScanEvents(context.Background(), func(EventRow) error { return nil })
	if err == nil {
		t.Fatal("scan events succeeded with a negative sequence")
	}
	if !strings.Contains(err.Error(), "seq -1") {
		t.Fatalf("error = %q, want negative sequence context", err)
	}
}

func TestScanEventsVisitsRowsInSequenceOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	base := time.Date(2026, time.October, 3, 1, 2, 3, 4, time.UTC)
	for _, row := range []EventRow{
		{Seq: 9, Timestamp: base.Add(time.Second), Type: "output", SessionID: "s2", Payload: "second"},
		{Seq: 4, Timestamp: base, Type: "state_changed", SessionID: "s1", AgentID: "s1", From: "working", To: "done"},
	} {
		if err := s.AppendEvent(row); err != nil {
			t.Fatalf("append seq %d: %v", row.Seq, err)
		}
	}

	var got []EventRow
	lastSeq, err := s.ScanEvents(context.Background(), func(row EventRow) error {
		got = append(got, row)
		return nil
	})
	if err != nil {
		t.Fatalf("scan events: %v", err)
	}
	if lastSeq != 9 {
		t.Fatalf("last seq = %d, want 9", lastSeq)
	}
	if len(got) != 2 {
		t.Fatalf("visited %d rows, want 2", len(got))
	}
	if got[0].Seq != 4 || got[1].Seq != 9 {
		t.Fatalf("visit order = [%d, %d], want [4, 9]", got[0].Seq, got[1].Seq)
	}
	if !got[0].Timestamp.Equal(base) || !got[1].Timestamp.Equal(base.Add(time.Second)) {
		t.Fatalf("timestamps = [%v, %v], want [%v, %v]", got[0].Timestamp, got[1].Timestamp, base, base.Add(time.Second))
	}
}

func TestAppendEventsAppendsConsecutiveBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	base := time.Date(2026, time.October, 3, 2, 3, 4, 5, time.UTC)
	newLastSeq, err := s.AppendEvents(context.Background(), 0, []EventRow{
		{Seq: 1, Timestamp: base, Type: "error", SessionID: "s1", AgentID: "s1", Payload: "daemon restarted"},
		{Seq: 2, Timestamp: base.Add(time.Nanosecond), Type: "state_changed", SessionID: "s1", AgentID: "s1", From: "working", To: "stopped", Reason: "daemon_restart"},
	})
	if err != nil {
		t.Fatalf("append events: %v", err)
	}
	if newLastSeq != 2 {
		t.Fatalf("new last seq = %d, want 2", newLastSeq)
	}

	var got []uint64
	lastSeq, err := s.ScanEvents(context.Background(), func(row EventRow) error {
		got = append(got, row.Seq)
		return nil
	})
	if err != nil {
		t.Fatalf("scan appended events: %v", err)
	}
	if lastSeq != 2 || len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("appended events = (last seq %d, rows %v), want (2, [1 2])", lastSeq, got)
	}
}

func TestAppendEventsRejectsStaleBoundaryWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if err := s.AppendEvent(EventRow{Seq: 1, Timestamp: time.Now().UTC(), Type: "output", SessionID: "existing"}); err != nil {
		t.Fatalf("append existing event: %v", err)
	}

	newLastSeq, err := s.AppendEvents(context.Background(), 0, []EventRow{
		{Seq: 1, Timestamp: time.Now().UTC(), Type: "output", SessionID: "new"},
	})
	if err == nil {
		t.Fatal("append events succeeded with a stale boundary")
	}
	if newLastSeq != 0 || !strings.Contains(err.Error(), "current seq 1, expected 0") {
		t.Fatalf("append result = (last seq %d, error %q), want stale boundary at seq 1", newLastSeq, err)
	}

	rows, err := s.Replay("new")
	if err != nil {
		t.Fatalf("replay new session: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("stale append wrote %d rows, want 0", len(rows))
	}
}

func TestAppendEventsRejectsNonConsecutiveBatchWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	newLastSeq, err := s.AppendEvents(context.Background(), 0, []EventRow{
		{Seq: 1, Timestamp: time.Now().UTC(), Type: "output", SessionID: "s1"},
		{Seq: 3, Timestamp: time.Now().UTC(), Type: "output", SessionID: "s1"},
	})
	if err == nil {
		t.Fatal("append events succeeded with a sequence gap")
	}
	if newLastSeq != 0 || !strings.Contains(err.Error(), "want 2") {
		t.Fatalf("append result = (last seq %d, error %q), want sequence validation failure", newLastSeq, err)
	}

	lastSeq, err := s.LastSeq()
	if err != nil {
		t.Fatalf("last seq: %v", err)
	}
	if lastSeq != 0 {
		t.Fatalf("last seq after rejected batch = %d, want 0", lastSeq)
	}
}

func TestAppendEventsRollsBackAfterInsertFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if _, err := s.db.Exec(`
		CREATE TRIGGER fail_second_event
		BEFORE INSERT ON events
		WHEN NEW.seq = 2
		BEGIN
			SELECT RAISE(ABORT, 'forced insert failure');
		END;
	`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	newLastSeq, err := s.AppendEvents(context.Background(), 0, []EventRow{
		{Seq: 1, Timestamp: time.Now().UTC(), Type: "output", SessionID: "s1"},
		{Seq: 2, Timestamp: time.Now().UTC(), Type: "output", SessionID: "s1"},
	})
	if err == nil {
		t.Fatal("append events succeeded despite forced insert failure")
	}
	if newLastSeq != 0 || !strings.Contains(err.Error(), "seq 2") {
		t.Fatalf("append result = (last seq %d, error %q), want insert failure at seq 2", newLastSeq, err)
	}

	lastSeq, err := s.LastSeq()
	if err != nil {
		t.Fatalf("last seq: %v", err)
	}
	if lastSeq != 0 {
		t.Fatalf("last seq after rolled-back batch = %d, want 0", lastSeq)
	}
}

func TestAppendEventsEmptyBatchChecksBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if err := s.AppendEvent(EventRow{Seq: 5, Timestamp: time.Now().UTC(), Type: "output", SessionID: "s1"}); err != nil {
		t.Fatalf("append existing event: %v", err)
	}

	newLastSeq, err := s.AppendEvents(context.Background(), 5, nil)
	if err != nil {
		t.Fatalf("append empty batch: %v", err)
	}
	if newLastSeq != 5 {
		t.Fatalf("empty batch last seq = %d, want 5", newLastSeq)
	}

	_, err = s.AppendEvents(context.Background(), 4, nil)
	if err == nil {
		t.Fatal("empty append succeeded with a stale boundary")
	}
}

func TestAppendAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	base := time.Now().UTC()
	rows := []EventRow{
		{Seq: 1, Timestamp: base, Type: "state_changed", SessionID: "s1", AgentID: "a1", From: "working", To: "blocked", Reason: "waiting"},
		{Seq: 2, Timestamp: base.Add(time.Second), Type: "output", SessionID: "s1", AgentID: "a1", Payload: "line one\n"},
		{Seq: 3, Timestamp: base.Add(2 * time.Second), Type: "state_changed", SessionID: "s1", AgentID: "a1", From: "blocked", To: "working"},
	}
	for _, r := range rows {
		if err := s.AppendEvent(r); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	replayed, err := s.Replay("s1")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(replayed) != 3 {
		t.Fatalf("replayed %d rows, want 3", len(replayed))
	}
	for i, r := range replayed {
		if r.Seq != rows[i].Seq {
			t.Errorf("row %d seq = %d, want %d", i, r.Seq, rows[i].Seq)
		}
		if r.Payload != rows[i].Payload {
			t.Errorf("row %d payload = %q, want %q", i, r.Payload, rows[i].Payload)
		}
		if !r.Timestamp.Equal(rows[i].Timestamp) {
			t.Errorf("row %d ts mismatch: %v vs %v", i, r.Timestamp, rows[i].Timestamp)
		}
	}
}

func TestReplayScopedBySession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	_ = s.AppendEvent(EventRow{Seq: 1, SessionID: "s1", Type: "output", Payload: "a"})
	_ = s.AppendEvent(EventRow{Seq: 2, SessionID: "s2", Type: "output", Payload: "b"})

	rows, err := s.Replay("s2")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(rows) != 1 || rows[0].Payload != "b" {
		t.Fatalf("session scoping broken: %+v", rows)
	}
}

func TestLastSeq(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	seq, err := s.LastSeq()
	if err != nil || seq != 0 {
		t.Fatalf("empty store LastSeq = %d, %v; want 0", seq, err)
	}
	_ = s.AppendEvent(EventRow{Seq: 7, SessionID: "s1"})
	seq, _ = s.LastSeq()
	if seq != 7 {
		t.Fatalf("LastSeq = %d, want 7", seq)
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("open first: %v", err)
	}
	if err := s1.AppendEvent(EventRow{Seq: 1, SessionID: "s1", Type: "output", Payload: "persisted"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	_ = s1.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("open second: %v", err)
	}
	defer s2.Close()
	rows, err := s2.Replay("s1")
	if err != nil {
		t.Fatalf("replay after reopen: %v", err)
	}
	if len(rows) != 1 || rows[0].Payload != "persisted" {
		t.Fatalf("persistence broken: %+v", rows)
	}
}
