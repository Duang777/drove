package recording

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
)

func TestFrameResolvesSequenceTimeAndPartialOffsetFromOrigin(t *testing.T) {
	st := openTailStore(t)
	base := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	rows := []store.EventRow{
		tailEventRow(1, base, "s1", event.TypeSessionLifecycle, "created", ""),
		tailOutputRow(2, base.Add(time.Second), "s1", 0, []byte("hello")),
		tailEventRow(
			3,
			base.Add(2*time.Second),
			"s1",
			event.TypeAgentResized,
			"",
			`{"version":1,"rows":3,"columns":10,"output_offset":5}`,
		),
		tailOutputRow(4, base.Add(3*time.Second), "s1", 5, []byte("\rworld")),
		tailOutputRow(5, base.Add(4*time.Second), "s1", 11, []byte("\nline2")),
	}
	appendTailRows(t, st, 0, rows)
	archive := NewArchive(st, newTailClock(5))

	sequence := Seq(2)
	sequenceSelector, err := NewSelector(SelectorInput{Seq: &sequence})
	if err != nil {
		t.Fatalf("sequence selector: %v", err)
	}
	beforeResize, err := archive.Frame(context.Background(), "s1", &sequenceSelector)
	if err != nil {
		t.Fatalf("sequence frame: %v", err)
	}
	if beforeResize.Cursor != (Cursor{Seq: 2, NextOffset: 5}) ||
		beforeResize.Rows != 40 ||
		beforeResize.Columns != 120 ||
		len(beforeResize.Lines) != 1 ||
		beforeResize.Lines[0] != "hello" ||
		beforeResize.Fidelity != FrameFidelityExactOriginReplay ||
		beforeResize.Restorable {
		t.Fatalf("sequence frame = %+v", beforeResize)
	}

	at := base.Add(3 * time.Second)
	timeSelector, err := NewSelector(SelectorInput{At: &at})
	if err != nil {
		t.Fatalf("time selector: %v", err)
	}
	atOutput, err := archive.Frame(context.Background(), "s1", &timeSelector)
	if err != nil {
		t.Fatalf("time frame: %v", err)
	}
	if atOutput.Cursor != (Cursor{Seq: 4, NextOffset: 11}) ||
		atOutput.Rows != 3 ||
		atOutput.Columns != 10 ||
		len(atOutput.Lines) == 0 ||
		atOutput.Lines[len(atOutput.Lines)-1] != "world" {
		t.Fatalf("time frame = %+v", atOutput)
	}

	offset := OutputOffset(8)
	offsetSelector, err := NewSelector(SelectorInput{Offset: &offset})
	if err != nil {
		t.Fatalf("offset selector: %v", err)
	}
	partial, err := archive.Frame(context.Background(), "s1", &offsetSelector)
	if err != nil {
		t.Fatalf("partial offset frame: %v", err)
	}
	if partial.Cursor != (Cursor{Seq: 4, NextOffset: 8}) ||
		partial.Rows != 3 ||
		partial.Columns != 10 ||
		len(partial.Lines) == 0 ||
		partial.Lines[len(partial.Lines)-1] != "wollo" {
		t.Fatalf("partial frame = %+v", partial)
	}
}

func TestFrameCacheDoesNotSurviveOutputRetention(t *testing.T) {
	st := openTailStore(t)
	base := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	rows := []store.EventRow{
		tailEventRow(1, base, "s1", event.TypeSessionLifecycle, "created", ""),
		tailOutputRow(2, base.Add(time.Second), "s1", 0, []byte("old")),
		tailOutputRow(3, base.Add(3*time.Second), "s1", 3, []byte("new")),
	}
	appendTailRows(t, st, 0, rows)
	archive := NewArchive(st, newTailClock(3))
	sequence := Seq(3)
	selector, err := NewSelector(SelectorInput{Seq: &sequence})
	if err != nil {
		t.Fatalf("selector: %v", err)
	}

	first, err := archive.Frame(context.Background(), "s1", &selector)
	if err != nil {
		t.Fatalf("initial frame: %v", err)
	}
	if len(first.Lines) != 1 || first.Lines[0] != "oldnew" {
		t.Fatalf("initial frame = %+v", first)
	}
	first.Lines[0] = "mutated"
	cached, err := archive.Frame(context.Background(), "s1", &selector)
	if err != nil {
		t.Fatalf("cached frame: %v", err)
	}
	if cached.Lines[0] != "oldnew" {
		t.Fatalf("cached frame was aliased: %+v", cached)
	}

	beforeGeneration := st.OutputRetentionGeneration()
	deleted, err := st.PruneOutputAttachments(
		context.Background(),
		base.Add(2*time.Second),
	)
	if err != nil {
		t.Fatalf("prune output: %v", err)
	}
	if deleted != 1 ||
		st.OutputRetentionGeneration() != beforeGeneration+1 {
		t.Fatalf(
			"prune result = deleted %d generation %d, want 1 and %d",
			deleted,
			st.OutputRetentionGeneration(),
			beforeGeneration+1,
		)
	}

	_, err = archive.Frame(context.Background(), "s1", &selector)
	var expired *OutputExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("frame after prune error = %v, want OutputExpiredError", err)
	}
	if !equalOutputRanges(
		expired.Missing,
		[]OutputRange{{Start: 0, End: 3}},
	) {
		t.Fatalf("expired ranges = %+v", expired.Missing)
	}
}

func TestFrameReportsOnlyRequiredPartialMissingRange(t *testing.T) {
	st := openTailStore(t)
	base := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	rows := []store.EventRow{
		tailEventRow(1, base, "s1", event.TypeSessionLifecycle, "created", ""),
		tailOutputRow(2, base.Add(time.Second), "s1", 0, []byte("abcdef")),
	}
	appendTailRows(t, st, 0, rows)
	if _, err := st.PruneOutputAttachments(
		context.Background(),
		base.Add(2*time.Second),
	); err != nil {
		t.Fatalf("prune output: %v", err)
	}
	archive := NewArchive(st, newTailClock(2))
	offset := OutputOffset(3)
	selector, err := NewSelector(SelectorInput{Offset: &offset})
	if err != nil {
		t.Fatalf("selector: %v", err)
	}
	_, err = archive.Frame(context.Background(), "s1", &selector)
	var expired *OutputExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("partial frame error = %v, want OutputExpiredError", err)
	}
	if !equalOutputRanges(
		expired.Missing,
		[]OutputRange{{Start: 0, End: 3}},
	) {
		t.Fatalf("partial missing ranges = %+v", expired.Missing)
	}
}

func TestFrameRequiresOneSupportedSelector(t *testing.T) {
	st := openTailStore(t)
	base := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	appendTailRows(t, st, 0, []store.EventRow{
		tailEventRow(1, base, "s1", event.TypeSessionLifecycle, "created", ""),
	})
	archive := NewArchive(st, newTailClock(1))
	if _, err := archive.Frame(
		context.Background(),
		"s1",
		nil,
	); !errors.Is(err, ErrInvalidFrameSelector) {
		t.Fatalf("nil selector error = %v", err)
	}
	cursor := Origin
	selector, err := NewSelector(SelectorInput{Cursor: &cursor})
	if err != nil {
		t.Fatalf("cursor selector: %v", err)
	}
	if _, err := archive.Frame(
		context.Background(),
		"s1",
		&selector,
	); !errors.Is(err, ErrInvalidFrameSelector) {
		t.Fatalf("cursor selector error = %v", err)
	}
	if _, err := archive.Frame(
		context.Background(),
		"s1",
		&Selector{},
	); !errors.Is(err, ErrInvalidFrameSelector) {
		t.Fatalf("zero selector error = %v", err)
	}
	sequence := Seq(0)
	valid, err := NewSelector(SelectorInput{Seq: &sequence})
	if err != nil {
		t.Fatalf("sequence selector: %v", err)
	}
	if _, err := archive.Frame(
		context.Background(),
		"missing",
		&valid,
	); !errors.Is(err, ErrUnknownSession) {
		t.Fatalf("unknown session error = %v", err)
	}
}
