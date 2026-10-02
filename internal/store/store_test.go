package store

import (
	"path/filepath"
	"testing"
	"time"
)

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
