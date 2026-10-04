package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

func TestOpenCreatesPrivateDatabaseAndPreservesExistingMode(t *testing.T) {
	dir := t.TempDir()
	newPath := filepath.Join(dir, "new.db")
	s, err := Open(newPath)
	if err != nil {
		t.Fatalf("open new database: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close new database: %v", err)
	}
	info, err := os.Lstat(newPath)
	if err != nil {
		t.Fatalf("inspect new database: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("new database mode = %04o, want 0600", info.Mode().Perm())
	}

	existingPath := filepath.Join(dir, "existing.db")
	if err := os.WriteFile(existingPath, nil, 0o666); err != nil {
		t.Fatalf("write existing database: %v", err)
	}
	if err := os.Chmod(existingPath, 0o666); err != nil {
		t.Fatalf("set existing database mode: %v", err)
	}
	s, err = Open(existingPath)
	if err != nil {
		t.Fatalf("open existing database: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close existing database: %v", err)
	}
	info, err = os.Lstat(existingPath)
	if err != nil {
		t.Fatalf("inspect existing database: %v", err)
	}
	if info.Mode().Perm() != 0o666 {
		t.Fatalf("existing database mode = %04o, want unchanged 0666", info.Mode().Perm())
	}
}

func TestOpenRejectsNonRegularDatabasePaths(t *testing.T) {
	dir := t.TempDir()
	directoryPath := filepath.Join(dir, "database-directory")
	if err := os.Mkdir(directoryPath, 0o700); err != nil {
		t.Fatalf("create database directory: %v", err)
	}
	if _, err := Open(directoryPath); err == nil {
		t.Fatal("open accepted a database directory")
	}

	target := filepath.Join(dir, "target.db")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	symlinkPath := filepath.Join(dir, "database-link")
	if err := os.Symlink(target, symlinkPath); err != nil {
		t.Fatalf("create database symlink: %v", err)
	}
	if _, err := Open(symlinkPath); err == nil {
		t.Fatal("open accepted a database symlink")
	}
}

func TestOpenEnablesSecureDelete(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	var enabled int
	if err := s.db.QueryRow(`PRAGMA secure_delete`).Scan(&enabled); err != nil {
		t.Fatalf("read secure_delete setting: %v", err)
	}
	if enabled != 1 {
		t.Fatalf("secure_delete = %d, want 1", enabled)
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

func TestRecentEventsFiltersBeforeLimitAndReturnsChronologicalEnvelopes(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	base := time.Date(2026, time.October, 4, 10, 0, 0, 0, time.UTC)
	relevant := []EventRow{
		{
			Seq:       1,
			Timestamp: base,
			Type:      string(event.TypeStateChanged),
			SessionID: "s1",
			AgentID:   "s1",
			From:      "working",
			To:        "blocked",
			Reason:    "signal",
			Payload:   `{"version":1}`,
		},
		{
			Seq:       2,
			Timestamp: base.Add(time.Second),
			Type:      string(event.TypeAgentSignal),
			SessionID: "s1",
			AgentID:   "s1",
			Payload:   `{"version":1}`,
		},
	}
	for _, row := range relevant {
		if err := s.AppendEvent(row); err != nil {
			t.Fatalf("append relevant seq %d: %v", row.Seq, err)
		}
	}
	for seq := uint64(3); seq <= 52; seq++ {
		if err := s.AppendEvent(EventRow{
			Seq:              seq,
			Timestamp:        base.Add(time.Duration(seq) * time.Second),
			Type:             string(event.TypeOutputChunk),
			SessionID:        "s1",
			AgentID:          "s1",
			Payload:          `{"version":1,"offset":1,"len":1}`,
			OutputAttachment: []byte("x"),
		}); err != nil {
			t.Fatalf("append output seq %d: %v", seq, err)
		}
	}
	if err := s.AppendEvent(EventRow{
		Seq:       53,
		Timestamp: base.Add(53 * time.Second),
		Type:      string(event.TypeAgentSignal),
		SessionID: "s2",
		AgentID:   "s2",
		Payload:   `{"version":1}`,
	}); err != nil {
		t.Fatalf("append other session signal: %v", err)
	}

	rows, err := s.RecentEvents(
		context.Background(),
		"s1",
		[]event.Type{event.TypeAgentSignal, event.TypeStateChanged},
		2,
	)
	if err != nil {
		t.Fatalf("query recent events: %v", err)
	}
	if len(rows) != 2 || rows[0].Seq != 1 || rows[1].Seq != 2 {
		t.Fatalf("recent rows = %+v, want relevant seqs [1 2]", rows)
	}
	for index, row := range rows {
		if row.OutputAttachment != nil {
			t.Fatalf("row %d loaded output attachment: %q", index, row.OutputAttachment)
		}
		if !row.Timestamp.Equal(relevant[index].Timestamp) {
			t.Fatalf(
				"row %d timestamp = %v, want %v",
				index,
				row.Timestamp,
				relevant[index].Timestamp,
			)
		}
	}
}

func TestRecentEventsAcceptsAttachmentAuditType(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	row := EventRow{
		Seq:       1,
		Timestamp: time.Date(2026, time.October, 4, 13, 0, 0, 0, time.UTC),
		Type:      string(event.TypeAgentAttachment),
		SessionID: "s1",
		AgentID:   "s1",
		Payload:   `{"version":1,"action":"attached","access":"read_only"}`,
	}
	if err := s.AppendEvent(row); err != nil {
		t.Fatalf("append attachment event: %v", err)
	}

	recent, err := s.RecentEvents(
		context.Background(),
		"s1",
		[]event.Type{event.TypeAgentAttachment},
		1,
	)
	if err != nil {
		t.Fatalf("query attachment event: %v", err)
	}
	if len(recent) != 1 || recent[0].Seq != row.Seq || recent[0].Payload != row.Payload {
		t.Fatalf("recent attachment rows = %+v, want %+v", recent, row)
	}
	if _, err := event.DecodeAttachmentAuditPayload(recent[0].Payload); err != nil {
		t.Fatalf("decode recent attachment payload: %v", err)
	}

	replayed, err := s.Replay("s1")
	if err != nil {
		t.Fatalf("replay attachment event: %v", err)
	}
	if len(replayed) != 1 ||
		replayed[0].Type != string(event.TypeAgentAttachment) ||
		replayed[0].OutputAttachment != nil {
		t.Fatalf("replayed attachment rows = %+v", replayed)
	}
}

func TestRecentEventsNeverHydratesOutputAttachments(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if err := s.AppendEvent(EventRow{
		Seq:              1,
		Timestamp:        time.Now().UTC(),
		Type:             string(event.TypeOutputChunk),
		SessionID:        "s1",
		AgentID:          "s1",
		Payload:          `{"version":1,"offset":6,"len":6}`,
		OutputAttachment: []byte("secret"),
	}); err != nil {
		t.Fatalf("append output chunk: %v", err)
	}
	rows, err := s.RecentEvents(
		context.Background(),
		"s1",
		[]event.Type{event.TypeOutputChunk},
		1,
	)
	if err != nil {
		t.Fatalf("query recent output: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("recent row count = %d, want 1", len(rows))
	}
	if rows[0].OutputAttachment != nil {
		t.Fatalf("recent query hydrated output attachment: %q", rows[0].OutputAttachment)
	}
}

func TestRecentEventsValidatesArguments(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	tests := []struct {
		name      string
		sessionID string
		types     []event.Type
		limit     int
	}{
		{name: "missing session", types: []event.Type{event.TypeAgentSignal}, limit: 1},
		{name: "missing types", sessionID: "s1", limit: 1},
		{name: "invalid type", sessionID: "s1", types: []event.Type{"unknown"}, limit: 1},
		{
			name:      "duplicate type",
			sessionID: "s1",
			types:     []event.Type{event.TypeAgentSignal, event.TypeAgentSignal},
			limit:     1,
		},
		{
			name:      "zero limit",
			sessionID: "s1",
			types:     []event.Type{event.TypeAgentSignal},
		},
		{
			name:      "negative limit",
			sessionID: "s1",
			types:     []event.Type{event.TypeAgentSignal},
			limit:     -1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := s.RecentEvents(
				context.Background(),
				test.sessionID,
				test.types,
				test.limit,
			); err == nil {
				t.Fatal("RecentEvents accepted invalid arguments")
			}
		})
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

func TestPruneOutputAttachmentsKeepsEventHistoryAndOtherAttachments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	cutoff := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	rows := []EventRow{
		{
			Seq:              1,
			Timestamp:        cutoff.Add(-48 * time.Hour),
			Type:             string(event.TypeOutputChunk),
			SessionID:        "s1",
			AgentID:          "s1",
			Payload:          `{"version":1,"offset":0,"len":3}`,
			OutputAttachment: []byte("old"),
		},
		{
			Seq:              2,
			Timestamp:        cutoff,
			Type:             string(event.TypeOutputChunk),
			SessionID:        "s1",
			AgentID:          "s1",
			Payload:          `{"version":1,"offset":3,"len":6}`,
			OutputAttachment: []byte("cutoff"),
		},
		{
			Seq:              3,
			Timestamp:        cutoff.Add(time.Hour),
			Type:             string(event.TypeOutputChunk),
			SessionID:        "s1",
			AgentID:          "s1",
			Payload:          `{"version":1,"offset":9,"len":3}`,
			OutputAttachment: []byte("new"),
		},
		{
			Seq:       4,
			Timestamp: cutoff.Add(-72 * time.Hour),
			Type:      string(event.TypeOutput),
			SessionID: "s1",
			AgentID:   "s1",
			Payload:   "legacy",
		},
		{
			Seq:       5,
			Timestamp: cutoff.Add(-72 * time.Hour),
			Type:      string(event.TypeStateChanged),
			SessionID: "s1",
			AgentID:   "s1",
			From:      "working",
			To:        "blocked",
		},
		{
			Seq:       6,
			Timestamp: cutoff.Add(-72 * time.Hour),
			Type:      string(event.TypeError),
			SessionID: "s1",
			AgentID:   "s1",
			Payload:   "kept error",
		},
		{
			Seq:       7,
			Timestamp: cutoff.Add(-72 * time.Hour),
			Type:      string(event.TypeAgentInput),
			SessionID: "s1",
			AgentID:   "s1",
			Payload:   `{"version":1,"bytes":4}`,
		},
		{
			Seq:       8,
			Timestamp: cutoff.Add(-72 * time.Hour),
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "s1",
			AgentID:   "s1",
			Reason:    "created",
			Payload:   `{"version":1,"name":"kept","vendor":"generic"}`,
		},
		{
			Seq:       9,
			Timestamp: cutoff.Add(-72 * time.Hour),
			Type:      string(event.TypeAgentSignal),
			SessionID: "s1",
			AgentID:   "s1",
			Payload:   `{"version":1,"source":"hook","kind":"observed"}`,
		},
	}
	if _, err := s.AppendEvents(context.Background(), 0, rows); err != nil {
		t.Fatalf("append fixture: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO output_chunks (event_seq, data) VALUES (?, ?)`,
		4,
		[]byte("non-output"),
	); err != nil {
		t.Fatalf("insert non-output attachment fixture: %v", err)
	}

	beforeLastSeq, err := s.LastSeq()
	if err != nil {
		t.Fatalf("last seq before prune: %v", err)
	}
	beforeGeneration := s.OutputRetentionGeneration()
	deleted, err := s.PruneOutputAttachments(context.Background(), cutoff)
	if err != nil {
		t.Fatalf("prune output attachments: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted attachments = %d, want 1", deleted)
	}
	if got := s.OutputRetentionGeneration(); got != beforeGeneration+1 {
		t.Fatalf(
			"retention generation = %d, want %d",
			got,
			beforeGeneration+1,
		)
	}
	afterLastSeq, err := s.LastSeq()
	if err != nil {
		t.Fatalf("last seq after prune: %v", err)
	}
	if afterLastSeq != beforeLastSeq || afterLastSeq != 9 {
		t.Fatalf("last seq changed from %d to %d", beforeLastSeq, afterLastSeq)
	}

	replayed, err := s.Replay("s1")
	if err != nil {
		t.Fatalf("replay after prune: %v", err)
	}
	if len(replayed) != len(rows) {
		t.Fatalf("event row count = %d, want %d", len(replayed), len(rows))
	}
	if replayed[0].OutputAttachment != nil {
		t.Fatalf("expired attachment remains: %q", replayed[0].OutputAttachment)
	}
	if !bytes.Equal(replayed[1].OutputAttachment, []byte("cutoff")) ||
		!bytes.Equal(replayed[2].OutputAttachment, []byte("new")) {
		t.Fatalf("retained output changed: %+v", replayed[:3])
	}
	if !bytes.Equal(replayed[3].OutputAttachment, []byte("non-output")) {
		t.Fatalf("non-output attachment was deleted: %q", replayed[3].OutputAttachment)
	}
	if replayed[3].Payload != "legacy" ||
		replayed[4].Type != string(event.TypeStateChanged) ||
		replayed[5].Payload != "kept error" ||
		replayed[6].Type != string(event.TypeAgentInput) ||
		replayed[7].Type != string(event.TypeSessionLifecycle) ||
		replayed[8].Type != string(event.TypeAgentSignal) {
		t.Fatalf("non-output history changed: %+v", replayed[3:])
	}

	scanned := 0
	lastSeq, err := s.ScanEvents(context.Background(), func(EventRow) error {
		scanned++
		return nil
	})
	if err != nil {
		t.Fatalf("scan after prune: %v", err)
	}
	if scanned != len(rows) || lastSeq != 9 {
		t.Fatalf("scan after prune = (%d rows, seq %d)", scanned, lastSeq)
	}
}

func TestPruneOutputAttachmentsCheckpointsWALWhenNothingExpires(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	row := EventRow{
		Seq:              1,
		Timestamp:        time.Now().UTC(),
		Type:             string(event.TypeOutputChunk),
		SessionID:        "s1",
		AgentID:          "s1",
		Payload:          `{"version":1,"offset":0,"len":4}`,
		OutputAttachment: []byte("kept"),
	}
	if _, err := s.AppendEvents(context.Background(), 0, []EventRow{row}); err != nil {
		t.Fatalf("append output: %v", err)
	}
	deleted, err := s.PruneOutputAttachments(
		context.Background(),
		row.Timestamp.Add(-time.Hour),
	)
	if err != nil {
		t.Fatalf("zero-row prune: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("deleted attachments = %d, want 0", deleted)
	}
	if got := s.OutputRetentionGeneration(); got != 0 {
		t.Fatalf("retention generation = %d, want 0", got)
	}
	if info, err := os.Stat(path + "-wal"); err == nil && info.Size() != 0 {
		t.Fatalf("WAL size after truncate checkpoint = %d, want 0", info.Size())
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspect WAL: %v", err)
	}
}

func TestSessionBoundaryCapturesDurablePrefix(t *testing.T) {
	s, base := newRecordingStoreFixture(t)

	boundary, found, err := s.SessionBoundary(context.Background(), "s1", nil)
	if err != nil {
		t.Fatalf("read current boundary: %v", err)
	}
	if !found {
		t.Fatal("current boundary did not find session")
	}
	if boundary.GlobalSeq != 6 ||
		boundary.ThroughSeq != 6 ||
		boundary.FirstSeq != 1 ||
		boundary.LastSeq != 6 ||
		boundary.NextOutputOffset != 5 {
		t.Fatalf("current boundary = %+v", boundary)
	}

	through := uint64(5)
	boundary, found, err = s.SessionBoundary(context.Background(), "s1", &through)
	if err != nil {
		t.Fatalf("read bounded boundary: %v", err)
	}
	if !found || boundary.GlobalSeq != 6 || boundary.ThroughSeq != 5 ||
		boundary.FirstSeq != 1 || boundary.LastSeq != 5 ||
		boundary.NextOutputOffset != 3 {
		t.Fatalf("bounded boundary = %+v, found %v", boundary, found)
	}

	through = 2
	boundary, found, err = s.SessionBoundary(context.Background(), "s1", &through)
	if err != nil {
		t.Fatalf("read pre-output boundary: %v", err)
	}
	if !found || boundary.LastSeq != 1 || boundary.NextOutputOffset != 0 {
		t.Fatalf("pre-output boundary = %+v, found %v", boundary, found)
	}

	boundary, found, err = s.SessionBoundary(context.Background(), "missing", nil)
	if err != nil {
		t.Fatalf("read missing boundary: %v", err)
	}
	if found || boundary.GlobalSeq != 6 {
		t.Fatalf("missing boundary = %+v, found %v", boundary, found)
	}

	future := uint64(7)
	if _, _, err := s.SessionBoundary(
		context.Background(),
		"s1",
		&future,
	); err == nil {
		t.Fatal("boundary accepted a sequence beyond the durable head")
	}

	if !base.Equal(time.Date(2026, time.October, 4, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("fixture base changed: %v", base)
	}
}

func TestReadSessionRangeBoundsRowsBytesAndRetention(t *testing.T) {
	s, _ := newRecordingStoreFixture(t)

	first, err := s.ReadSessionRange(
		context.Background(),
		"s1",
		0,
		6,
		ReadLimit{Rows: 2, AttachmentBytes: event.MaxOutputChunkBytes},
		true,
	)
	if err != nil {
		t.Fatalf("read first page: %v", err)
	}
	if len(first.Rows) != 2 || first.Rows[0].Seq != 1 || first.Rows[1].Seq != 3 ||
		!first.More || first.ScannedThrough != 3 || first.AttachmentBytes != 3 {
		t.Fatalf("first page = %+v", first)
	}
	if !first.Rows[1].OutputAttachmentPresent ||
		!bytes.Equal(first.Rows[1].OutputAttachment, []byte("abc")) {
		t.Fatalf("first output row = %+v", first.Rows[1])
	}

	second, err := s.ReadSessionRange(
		context.Background(),
		"s1",
		first.ScannedThrough,
		6,
		ReadLimit{Rows: 2, AttachmentBytes: event.MaxOutputChunkBytes},
		true,
	)
	if err != nil {
		t.Fatalf("read second page: %v", err)
	}
	if len(second.Rows) != 2 || second.Rows[0].Seq != 5 || second.Rows[1].Seq != 6 ||
		second.More || second.ScannedThrough != 6 || second.AttachmentBytes != 2 {
		t.Fatalf("second page = %+v", second)
	}

	if _, err := s.db.Exec(`DELETE FROM output_chunks WHERE event_seq = 3`); err != nil {
		t.Fatalf("expire first output: %v", err)
	}
	retained, err := s.ReadSessionRange(
		context.Background(),
		"s1",
		0,
		6,
		DefaultReadLimit(),
		true,
	)
	if err != nil {
		t.Fatalf("read retention page: %v", err)
	}
	if retained.Rows[1].OutputAttachmentPresent ||
		retained.Rows[1].OutputAttachment != nil {
		t.Fatalf("expired output row = %+v", retained.Rows[1])
	}
	if !retained.Rows[3].OutputAttachmentPresent ||
		!bytes.Equal(retained.Rows[3].OutputAttachment, []byte("de")) {
		t.Fatalf("retained output row = %+v", retained.Rows[3])
	}

	metadataOnly, err := s.ReadSessionRange(
		context.Background(),
		"s1",
		0,
		6,
		DefaultReadLimit(),
		false,
	)
	if err != nil {
		t.Fatalf("read metadata page: %v", err)
	}
	if !metadataOnly.Rows[3].OutputAttachmentPresent ||
		metadataOnly.Rows[3].OutputAttachment != nil ||
		metadataOnly.AttachmentBytes != 0 {
		t.Fatalf("metadata-only retained output = %+v", metadataOnly.Rows[3])
	}
}

func TestReadSessionRangeStopsAtAttachmentByteBudget(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})

	base := time.Date(2026, time.October, 4, 11, 0, 0, 0, time.UTC)
	firstData := bytes.Repeat([]byte("a"), 20*1024)
	secondData := bytes.Repeat([]byte("b"), 20*1024)
	rows := []EventRow{
		outputFixtureRow(1, base, "bytes", 0, firstData),
		outputFixtureRow(2, base.Add(time.Second), "bytes", uint64(len(firstData)), secondData),
	}
	if _, err := s.AppendEvents(context.Background(), 0, rows); err != nil {
		t.Fatalf("append byte fixture: %v", err)
	}

	page, err := s.ReadSessionRange(
		context.Background(),
		"bytes",
		0,
		2,
		ReadLimit{Rows: 10, AttachmentBytes: event.MaxOutputChunkBytes},
		true,
	)
	if err != nil {
		t.Fatalf("read byte-bounded page: %v", err)
	}
	if len(page.Rows) != 1 || !page.More || page.ScannedThrough != 1 ||
		page.AttachmentBytes != len(firstData) {
		t.Fatalf("byte-bounded page = %+v", page)
	}
}

func TestResolveSessionSelectorsUseCapturedPrefix(t *testing.T) {
	s, base := newRecordingStoreFixture(t)

	position, found, err := s.ResolveSessionSequence(context.Background(), "s1", 4, 6)
	if err != nil {
		t.Fatalf("resolve sequence: %v", err)
	}
	if !found || position.Seq != 3 || position.NextOutputOffset != 3 {
		t.Fatalf("sequence position = %+v, found %v", position, found)
	}

	position, found, err = s.ResolveSessionTime(
		context.Background(),
		"s1",
		base.Add(2500*time.Millisecond),
		6,
	)
	if err != nil {
		t.Fatalf("resolve timestamp: %v", err)
	}
	if !found || position.Seq != 5 || position.NextOutputOffset != 3 ||
		!position.Timestamp.Equal(base.Add(2*time.Second)) {
		t.Fatalf("timestamp position = %+v, found %v", position, found)
	}

	tests := []struct {
		offset  uint64
		found   bool
		seq     uint64
		next    uint64
		partial uint64
	}{
		{offset: 0, found: true, seq: 1, next: 0},
		{offset: 2, found: true, seq: 1, next: 2, partial: 3},
		{offset: 3, found: true, seq: 5, next: 3},
		{offset: 5, found: true, seq: 6, next: 5},
		{offset: 6},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("offset-%d", test.offset), func(t *testing.T) {
			resolved, found, err := s.ResolveSessionOutputOffset(
				context.Background(),
				"s1",
				test.offset,
				6,
			)
			if err != nil {
				t.Fatalf("resolve offset: %v", err)
			}
			if found != test.found {
				t.Fatalf("found = %v, want %v", found, test.found)
			}
			if !found {
				return
			}
			if resolved.Position.Seq != test.seq ||
				resolved.Position.NextOutputOffset != test.next ||
				resolved.PartialOutputSeq != test.partial {
				t.Fatalf("offset position = %+v", resolved)
			}
		})
	}
}

func TestReadSessionRangeValidatesBounds(t *testing.T) {
	s, _ := newRecordingStoreFixture(t)
	tests := []struct {
		name      string
		sessionID string
		after     uint64
		through   uint64
		limit     ReadLimit
	}{
		{name: "missing session", through: 1, limit: DefaultReadLimit()},
		{name: "reversed range", sessionID: "s1", after: 2, through: 1, limit: DefaultReadLimit()},
		{name: "zero rows", sessionID: "s1", through: 1, limit: ReadLimit{AttachmentBytes: event.MaxOutputChunkBytes}},
		{name: "too many rows", sessionID: "s1", through: 1, limit: ReadLimit{Rows: MaxRangeRows + 1, AttachmentBytes: event.MaxOutputChunkBytes}},
		{name: "tiny byte limit", sessionID: "s1", through: 1, limit: ReadLimit{Rows: 1, AttachmentBytes: event.MaxOutputChunkBytes - 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := s.ReadSessionRange(
				context.Background(),
				test.sessionID,
				test.after,
				test.through,
				test.limit,
				true,
			); err == nil {
				t.Fatal("range accepted invalid bounds")
			}
		})
	}
}

func newRecordingStoreFixture(t *testing.T) (*Store, time.Time) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "recording.db"))
	if err != nil {
		t.Fatalf("open recording fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close recording fixture: %v", err)
		}
	})

	base := time.Date(2026, time.October, 4, 10, 0, 0, 0, time.UTC)
	rows := []EventRow{
		{
			Seq:       1,
			Timestamp: base,
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "s1",
			AgentID:   "s1",
			Reason:    "created",
			Payload:   `{"version":1,"name":"one","vendor":"generic"}`,
		},
		{
			Seq:       2,
			Timestamp: base.Add(time.Second),
			Type:      string(event.TypeOutput),
			SessionID: "s2",
			AgentID:   "s2",
			Payload:   "other",
		},
		outputFixtureRow(3, base.Add(3*time.Second), "s1", 0, []byte("abc")),
		{
			Seq:       4,
			Timestamp: base.Add(1500 * time.Millisecond),
			Type:      string(event.TypeStateChanged),
			SessionID: "s2",
			AgentID:   "s2",
			From:      "working",
			To:        "blocked",
		},
		{
			Seq:       5,
			Timestamp: base.Add(2 * time.Second),
			Type:      string(event.TypeAgentResized),
			SessionID: "s1",
			AgentID:   "s1",
			Payload:   `{"version":1,"rows":50,"columns":160,"output_offset":3}`,
		},
		outputFixtureRow(6, base.Add(4*time.Second), "s1", 3, []byte("de")),
	}
	if _, err := s.AppendEvents(context.Background(), 0, rows); err != nil {
		t.Fatalf("append recording fixture: %v", err)
	}
	return s, base
}

func outputFixtureRow(
	seq uint64,
	at time.Time,
	sessionID string,
	offset uint64,
	data []byte,
) EventRow {
	return EventRow{
		Seq:              seq,
		Timestamp:        at,
		Type:             string(event.TypeOutputChunk),
		SessionID:        sessionID,
		AgentID:          sessionID,
		Payload:          fmt.Sprintf(`{"version":1,"offset":%d,"len":%d}`, offset, len(data)),
		OutputAttachment: data,
	}
}
