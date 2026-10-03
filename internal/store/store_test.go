package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/event"
)

func TestOpenMigratesV1WithoutRewritingEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE events (
			seq        INTEGER PRIMARY KEY,
			ts         TEXT    NOT NULL,
			type       TEXT    NOT NULL,
			session_id TEXT    NOT NULL,
			agent_id   TEXT    NOT NULL DEFAULT '',
			from_state TEXT    NOT NULL DEFAULT '',
			to_state   TEXT    NOT NULL DEFAULT '',
			reason     TEXT    NOT NULL DEFAULT '',
			payload    TEXT    NOT NULL DEFAULT ''
		);
		INSERT INTO events (
			seq, ts, type, session_id, agent_id, from_state, to_state, reason, payload
		) VALUES (
			41, '2026-10-04T01:02:03Z', 'output', 'legacy', 'legacy', '', '', '', 'kept'
		);
		PRAGMA user_version = 1;
	`); err != nil {
		t.Fatalf("seed version 1 database: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	defer s.Close()

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read migrated version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}
	rows, err := s.Replay("legacy")
	if err != nil {
		t.Fatalf("replay legacy row: %v", err)
	}
	if len(rows) != 1 ||
		rows[0].Seq != 41 ||
		rows[0].Payload != "kept" ||
		rows[0].Type != string(event.TypeOutput) {
		t.Fatalf("legacy row changed during migration: %+v", rows)
	}
}

func TestOpenEnablesForeignKeys(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	var enabled int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		t.Fatalf("read foreign key setting: %v", err)
	}
	if enabled != 1 {
		t.Fatalf("foreign_keys = %d, want 1", enabled)
	}
	if _, err := s.db.Exec(
		`INSERT INTO output_chunks (event_seq, data) VALUES (?, ?)`,
		99,
		[]byte("orphan"),
	); err == nil {
		t.Fatal("foreign key accepted an orphaned output attachment")
	}
}

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
		{
			Seq:              1,
			Timestamp:        time.Now().UTC(),
			Type:             string(event.TypeOutputChunk),
			SessionID:        "s1",
			AgentID:          "s1",
			Payload:          `{"version":1,"offset":0,"len":3}`,
			OutputAttachment: []byte("one"),
		},
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
	var attachments int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM output_chunks`).Scan(&attachments); err != nil {
		t.Fatalf("count output attachments: %v", err)
	}
	if attachments != 0 {
		t.Fatalf("output attachment count = %d, want 0", attachments)
	}
}

func TestAppendEventsRollsBackAfterAttachmentFailure(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if _, err := s.db.Exec(`
		CREATE TRIGGER fail_output_attachment
		BEFORE INSERT ON output_chunks
		BEGIN
			SELECT RAISE(ABORT, 'forced attachment failure');
		END;
	`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	row := EventRow{
		Seq:              1,
		Timestamp:        time.Now().UTC(),
		Type:             string(event.TypeOutputChunk),
		SessionID:        "s1",
		AgentID:          "s1",
		Payload:          `{"version":1,"offset":0,"len":3}`,
		OutputAttachment: []byte("one"),
	}
	lastSeq, err := s.AppendEvents(context.Background(), 0, []EventRow{row})
	if err == nil || !strings.Contains(err.Error(), "attachment") {
		t.Fatalf("append result = (%d, %v), want attachment failure", lastSeq, err)
	}
	if lastSeq != 0 {
		t.Fatalf("last seq = %d, want 0", lastSeq)
	}
	var envelopes int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&envelopes); err != nil {
		t.Fatalf("count event envelopes: %v", err)
	}
	if envelopes != 0 {
		t.Fatalf("event envelope count = %d, want 0", envelopes)
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

func TestOutputChunkAttachmentIsAtomicAndReplayOnly(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	data := []byte{0, 1, 2, '\n', 0xff}
	row := EventRow{
		Seq:              1,
		Timestamp:        time.Now().UTC(),
		Type:             string(event.TypeOutputChunk),
		SessionID:        "s1",
		AgentID:          "s1",
		Payload:          `{"version":1,"offset":7,"len":5}`,
		OutputAttachment: data,
	}
	if _, err := s.AppendEvents(context.Background(), 0, []EventRow{row}); err != nil {
		t.Fatalf("append output chunk: %v", err)
	}

	var scanned EventRow
	if _, err := s.ScanEvents(context.Background(), func(row EventRow) error {
		scanned = row
		return nil
	}); err != nil {
		t.Fatalf("scan events: %v", err)
	}
	if scanned.OutputAttachment != nil {
		t.Fatalf("projection scan loaded an attachment: %v", scanned.OutputAttachment)
	}

	replayed, err := s.Replay("s1")
	if err != nil {
		t.Fatalf("replay output chunk: %v", err)
	}
	if len(replayed) != 1 || !bytes.Equal(replayed[0].OutputAttachment, data) {
		t.Fatalf("replayed attachment = %+v, want %v", replayed, data)
	}
	if replayed[0].Payload != row.Payload {
		t.Fatalf("replayed payload = %q, want metadata %q", replayed[0].Payload, row.Payload)
	}
	encoded, err := json.Marshal(replayed[0])
	if err != nil {
		t.Fatalf("marshal replay row: %v", err)
	}
	if bytes.Contains(encoded, data) || bytes.Contains(encoded, []byte("OutputAttachment")) {
		t.Fatalf("JSON exposed output attachment: %s", encoded)
	}
}

func TestAppendRejectsInvalidOutputAttachments(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if err := s.AppendEvent(EventRow{
		Seq:       1,
		Type:      string(event.TypeOutputChunk),
		SessionID: "s1",
	}); err == nil {
		t.Fatal("append accepted an output chunk without an attachment")
	}
	if err := s.AppendEvent(EventRow{
		Seq:              1,
		Type:             string(event.TypeOutput),
		SessionID:        "s1",
		OutputAttachment: []byte("hidden"),
	}); err == nil {
		t.Fatal("append accepted an attachment on a legacy output event")
	}
	lastSeq, err := s.LastSeq()
	if err != nil {
		t.Fatalf("last seq: %v", err)
	}
	if lastSeq != 0 {
		t.Fatalf("last seq after rejected rows = %d, want 0", lastSeq)
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
