package recording

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
)

func TestTimelineProjectsStateSpansBlockedEntriesAndRetention(t *testing.T) {
	st := openTailStore(t)
	base := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	rows := timelineFixtureRows(t, base)
	appendTailRows(t, st, 0, rows)
	archive := NewArchive(st, newTailClock(rows[len(rows)-1].Seq))

	timeline, err := archive.Timeline(context.Background(), "s1")
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if timeline.SessionID != "s1" ||
		timeline.AgentID != "s1" ||
		timeline.Captured != (Cursor{Seq: 14, NextOffset: 18}) ||
		!timeline.CapturedAt.Equal(base.Add(131*time.Second)) ||
		timeline.DurationMillis != 131000 {
		t.Fatalf("timeline metadata = %+v", timeline)
	}
	if len(timeline.Spans) != 8 {
		t.Fatalf("span count = %d, want 8", len(timeline.Spans))
	}
	if got := timeline.Spans[0]; got.State != agent.StateStarting ||
		got.Start != (Cursor{Seq: 2}) ||
		got.End == nil ||
		*got.End != (Cursor{Seq: 4, NextOffset: 4}) ||
		got.Source != "session" ||
		got.DurationMillis == nil ||
		*got.DurationMillis != 2000 {
		t.Fatalf("starting span = %+v", got)
	}
	if got := timeline.Spans[1]; got.State != agent.StateWorking ||
		got.Source != "notify" {
		t.Fatalf("known v2 span = %+v", got)
	}
	if got := timeline.Spans[3]; got.State != agent.StateWorking ||
		got.Source != "" ||
		got.Rule != "" {
		t.Fatalf("unknown evidence span = %+v", got)
	}
	if got := timeline.Spans[len(timeline.Spans)-1]; got.State != agent.StateDone ||
		got.End == nil ||
		*got.End != timeline.Captured ||
		got.EndAt == nil ||
		!got.EndAt.Equal(timeline.CapturedAt) {
		t.Fatalf("terminal span = %+v", got)
	}
	if len(timeline.Blocked) != 3 {
		t.Fatalf("blocked count = %d, want 3", len(timeline.Blocked))
	}
	for index, occurrence := range timeline.Blocked {
		if occurrence.Number != index+1 ||
			occurrence.Span.State != agent.StateBlocked ||
			!occurrence.FrameAvailable {
			t.Fatalf("blocked occurrence %d = %+v", index, occurrence)
		}
	}
	if got := timeline.Blocked[0]; got.Span.Source != "screen" ||
		got.Span.Rule != "claude.approval_prompt" ||
		got.Jump != (Cursor{Seq: 4, NextOffset: 4}) {
		t.Fatalf("first blocked occurrence = %+v", got)
	}
	if got := timeline.Blocked[2].Jump; got != (Cursor{Seq: 9, NextOffset: 13}) {
		t.Fatalf("third blocked jump = %+v", got)
	}
	if timeline.Output.Range != (OutputRange{Start: 0, End: 18}) ||
		len(timeline.Output.Missing) != 0 ||
		!equalOutputRanges(
			timeline.Output.Retained,
			[]OutputRange{{Start: 0, End: 18}},
		) {
		t.Fatalf("initial output coverage = %+v", timeline.Output)
	}

	deleted, err := st.PruneOutputAttachments(
		context.Background(),
		base.Add(30*time.Second),
	)
	if err != nil {
		t.Fatalf("prune output: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted attachments = %d, want 2", deleted)
	}
	pruned, err := archive.Timeline(context.Background(), "s1")
	if err != nil {
		t.Fatalf("timeline after pruning: %v", err)
	}
	if !equalOutputRanges(
		pruned.Output.Missing,
		[]OutputRange{{Start: 0, End: 9}},
	) || !equalOutputRanges(
		pruned.Output.Retained,
		[]OutputRange{{Start: 9, End: 18}},
	) {
		t.Fatalf("pruned output coverage = %+v", pruned.Output)
	}
	if pruned.Blocked[0].FrameAvailable {
		t.Fatalf("expired blocked jump marked available: %+v", pruned.Blocked[0])
	}
}

func TestBlockedLookupValidatesOrdinalAndCapturedRange(t *testing.T) {
	st := openTailStore(t)
	base := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	rows := timelineFixtureRows(t, base)
	appendTailRows(t, st, 0, rows)
	archive := NewArchive(st, newTailClock(rows[len(rows)-1].Seq))

	occurrence, err := archive.Blocked(context.Background(), "s1", 2)
	if err != nil {
		t.Fatalf("blocked occurrence: %v", err)
	}
	if occurrence.Number != 2 ||
		occurrence.Span.Start != (Cursor{Seq: 9, NextOffset: 13}) {
		t.Fatalf("blocked occurrence = %+v", occurrence)
	}
	if _, err := archive.Blocked(
		context.Background(),
		"s1",
		0,
	); !errors.Is(err, ErrInvalidBlockedOccurrence) {
		t.Fatalf("zero ordinal error = %v", err)
	}
	if _, err := archive.Blocked(
		context.Background(),
		"s1",
		4,
	); !errors.Is(err, ErrBlockedOccurrenceNotFound) {
		t.Fatalf("missing ordinal error = %v", err)
	}
}

func TestTimelineRejectsMalformedKnownEvidenceButPreservesUnknownVersion(t *testing.T) {
	st := openTailStore(t)
	base := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	appendTailRows(t, st, 0, []store.EventRow{
		tailEventRow(1, base, "unknown", event.TypeSessionLifecycle, "created", ""),
		{
			Seq:       2,
			Timestamp: base.Add(time.Second),
			Type:      string(event.TypeStateChanged),
			SessionID: "unknown",
			AgentID:   "unknown",
			From:      string(agent.StatePending),
			To:        string(agent.StateStarting),
			Payload:   `{"version":99,"future":"accepted"}`,
		},
		tailEventRow(3, base, "malformed", event.TypeSessionLifecycle, "created", ""),
		{
			Seq:       4,
			Timestamp: base.Add(time.Second),
			Type:      string(event.TypeStateChanged),
			SessionID: "malformed",
			AgentID:   "malformed",
			From:      string(agent.StatePending),
			To:        string(agent.StateStarting),
			Payload:   `{"version":1,"source":"session"}`,
		},
	})
	archive := NewArchive(st, newTailClock(4))
	timeline, err := archive.Timeline(context.Background(), "unknown")
	if err != nil {
		t.Fatalf("unknown version timeline: %v", err)
	}
	if len(timeline.Spans) != 1 ||
		timeline.Spans[0].Source != "" ||
		timeline.Spans[0].Rule != "" {
		t.Fatalf("unknown evidence timeline = %+v", timeline)
	}
	if _, err := archive.Timeline(context.Background(), "malformed"); err == nil {
		t.Fatal("timeline accepted malformed known evidence")
	}
}

func timelineFixtureRows(t *testing.T, base time.Time) []store.EventRow {
	t.Helper()
	screen := &event.ScreenAttributionPayload{
		Rule:          "claude.approval_prompt",
		Edge:          "present",
		Region:        "viewport.bottom",
		OutputOffset:  9,
		LastOutputSeq: 5,
		Evidence:      "approval prompt",
	}
	return []store.EventRow{
		tailEventRow(1, base, "s1", event.TypeSessionLifecycle, "created", ""),
		timelineStateRow(
			t,
			2,
			base.Add(time.Second),
			agent.StatePending,
			agent.StateStarting,
			event.StateEvidencePayloadV1{
				Version: 1, Source: "session", Event: "session_start", Confidence: 1,
			},
		),
		tailOutputRow(3, base.Add(2*time.Second), "s1", 0, []byte("boot")),
		timelineStateRow(
			t,
			4,
			base.Add(3*time.Second),
			agent.StateStarting,
			agent.StateWorking,
			event.StateEvidencePayloadV2{
				Version: 2, Source: "notify", Event: "turn_started", Confidence: 1,
				DeliveryID: "550e8400-e29b-41d4-a716-446655440000",
			},
		),
		tailOutputRow(5, base.Add(20*time.Second), "s1", 4, []byte("ready")),
		timelineStateRow(
			t,
			6,
			base.Add(40*time.Second),
			agent.StateWorking,
			agent.StateBlocked,
			event.StateEvidencePayloadV3{
				StateEvidencePayloadV1: event.StateEvidencePayloadV1{
					Version:    3,
					Source:     "screen",
					Event:      "claude.approval_prompt",
					Confidence: 1,
				},
				Screen: screen,
			},
		),
		tailOutputRow(7, base.Add(45*time.Second), "s1", 9, []byte("wait")),
		{
			Seq:       8,
			Timestamp: base.Add(50 * time.Second),
			Type:      string(event.TypeStateChanged),
			SessionID: "s1",
			AgentID:   "s1",
			From:      string(agent.StateBlocked),
			To:        string(agent.StateWorking),
			Reason:    "future evidence",
			Payload:   `{"version":4,"source":"future","rule":"private"}`,
		},
		timelineStateRow(
			t,
			9,
			base.Add(90*time.Second),
			agent.StateWorking,
			agent.StateBlocked,
			event.StateEvidencePayloadV1{
				Version: 1, Source: "heuristic", Event: "idle_timeout", Confidence: 0.8,
			},
		),
		tailOutputRow(10, base.Add(95*time.Second), "s1", 13, []byte("again")),
		timelineStateRow(
			t,
			11,
			base.Add(100*time.Second),
			agent.StateBlocked,
			agent.StateWorking,
			event.StateEvidencePayloadV1{
				Version: 1, Source: "heuristic", Event: "output_activity", Confidence: 0.8,
			},
		),
		timelineStateRow(
			t,
			12,
			base.Add(120*time.Second),
			agent.StateWorking,
			agent.StateBlocked,
			event.StateEvidencePayloadV1{
				Version: 1, Source: "timer", Event: "blocked_timeout", Confidence: 1,
			},
		),
		timelineStateRow(
			t,
			13,
			base.Add(130*time.Second),
			agent.StateBlocked,
			agent.StateDone,
			event.StateEvidencePayloadV1{
				Version: 1, Source: "process", Event: "process_exited", Confidence: 1,
			},
		),
		tailEventRow(14, base.Add(131*time.Second), "s1", event.TypeError, "", "late"),
	}
}

func timelineStateRow(
	t *testing.T,
	seq uint64,
	at time.Time,
	from agent.State,
	to agent.State,
	payload any,
) store.EventRow {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal state evidence: %v", err)
	}
	return store.EventRow{
		Seq:       seq,
		Timestamp: at,
		Type:      string(event.TypeStateChanged),
		SessionID: "s1",
		AgentID:   "s1",
		From:      string(from),
		To:        string(to),
		Reason:    string(to),
		Payload:   string(encoded),
	}
}
