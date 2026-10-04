package recording

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
	"github.com/Duang777/drove/internal/term"
)

func TestReplayAcceptanceComplexTerminalAndExpiredOutput(t *testing.T) {
	fixture := newReplayAcceptanceFixture(t)
	st := openTailStore(t)
	appendTailRows(t, st, 0, fixture.rows)
	archive := NewArchive(st, newTailClock(fixture.rows[len(fixture.rows)-1].Seq))

	sequence := Seq(10)
	at := fixture.base.Add(48 * time.Second)
	offset := fixture.partialOffset
	selectors := []struct {
		name  string
		input SelectorInput
	}{
		{name: "sequence", input: SelectorInput{Seq: &sequence}},
		{name: "time", input: SelectorInput{At: &at}},
		{name: "partial offset", input: SelectorInput{Offset: &offset}},
	}
	for _, test := range selectors {
		t.Run(test.name, func(t *testing.T) {
			selector, err := NewSelector(test.input)
			if err != nil {
				t.Fatalf("create selector: %v", err)
			}
			frame, err := archive.Frame(context.Background(), "s1", &selector)
			if err != nil {
				t.Fatalf("render archive frame: %v", err)
			}
			direct := replayFixtureDirectly(t, fixture.rows, frame.Cursor)
			if !reflect.DeepEqual(frame, direct) {
				t.Fatalf("archive frame = %+v, direct replay = %+v", frame, direct)
			}
		})
	}

	timeline, err := archive.Timeline(context.Background(), "s1")
	if err != nil {
		t.Fatalf("project timeline: %v", err)
	}
	if len(timeline.Blocked) != 3 {
		t.Fatalf("blocked intervals = %d, want 3", len(timeline.Blocked))
	}
	for index, blocked := range timeline.Blocked {
		if blocked.Number != index+1 || blocked.Span.State != agent.StateBlocked {
			t.Fatalf("blocked interval %d = %+v", index, blocked)
		}
	}

	deleted, err := st.PruneOutputAttachments(
		context.Background(),
		fixture.base.Add(47*time.Second),
	)
	if err != nil {
		t.Fatalf("prune fixture output: %v", err)
	}
	if deleted != 5 {
		t.Fatalf("pruned attachments = %d, want 5", deleted)
	}
	prunedTimeline, err := archive.Timeline(context.Background(), "s1")
	if err != nil {
		t.Fatalf("project timeline after pruning: %v", err)
	}
	if len(prunedTimeline.Blocked) != 3 ||
		len(prunedTimeline.Output.Missing) != 1 ||
		prunedTimeline.Output.Missing[0].Start != 0 {
		t.Fatalf("timeline after pruning = %+v", prunedTimeline)
	}

	finalSequence := Seq(fixture.rows[len(fixture.rows)-1].Seq)
	finalSelector, err := NewSelector(SelectorInput{Seq: &finalSequence})
	if err != nil {
		t.Fatalf("create final selector: %v", err)
	}
	_, err = archive.Frame(context.Background(), "s1", &finalSelector)
	var expired *OutputExpiredError
	if !errors.As(err, &expired) || len(expired.Missing) == 0 {
		t.Fatalf("frame after pruning error = %v, want output expiry", err)
	}
}

type replayAcceptanceFixture struct {
	base          time.Time
	rows          []store.EventRow
	partialOffset OutputOffset
}

func newReplayAcceptanceFixture(t *testing.T) replayAcceptanceFixture {
	t.Helper()
	base := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	rows := []store.EventRow{
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
	}
	nextSequence := uint64(3)
	nextOffset := uint64(0)
	appendOutput := func(at time.Duration, data []byte) uint64 {
		start := nextOffset
		rows = append(rows, tailOutputRow(
			nextSequence,
			base.Add(at),
			"s1",
			nextOffset,
			data,
		))
		nextSequence++
		nextOffset += uint64(len(data))
		return start
	}
	appendState := func(
		at time.Duration,
		from agent.State,
		to agent.State,
		source string,
		name string,
	) {
		rows = append(rows, timelineStateRow(
			t,
			nextSequence,
			base.Add(at),
			from,
			to,
			event.StateEvidencePayloadV1{
				Version: 1, Source: source, Event: name, Confidence: 1,
			},
		))
		nextSequence++
	}

	appendOutput(2*time.Second, []byte("\x1b[2J\x1b[Hboot \xe7"))
	appendOutput(3*time.Second, []byte("\x95\x8c\x1b["))
	appendOutput(4*time.Second, []byte("2;1Hworking"))
	appendState(5*time.Second, agent.StateStarting, agent.StateWorking, "heuristic", "turn_started")
	appendState(40*time.Second, agent.StateWorking, agent.StateBlocked, "heuristic", "approval")
	appendOutput(45*time.Second, []byte("\x1b]0;drove"))
	appendOutput(46*time.Second, []byte("\x07\x1b[?1049hALT \xf0\x9f"))
	appendOutput(47*time.Second, []byte("\x98\x80\x1b[2;1Hscreen"))
	rows = append(rows, tailEventRow(
		nextSequence,
		base.Add(48*time.Second),
		"s1",
		event.TypeAgentResized,
		"",
		fmt.Sprintf(
			`{"version":1,"rows":20,"columns":60,"output_offset":%d}`,
			nextOffset,
		),
	))
	nextSequence++
	appendState(50*time.Second, agent.StateBlocked, agent.StateWorking, "heuristic", "resolved")
	finalScreenStart := appendOutput(60*time.Second, []byte("\x1b[?1049lback"))
	appendState(80*time.Second, agent.StateWorking, agent.StateBlocked, "heuristic", "approval")
	appendState(90*time.Second, agent.StateBlocked, agent.StateWorking, "heuristic", "resolved")
	appendState(120*time.Second, agent.StateWorking, agent.StateBlocked, "heuristic", "approval")
	appendOutput(121*time.Second, []byte("\rfinal"))
	appendState(130*time.Second, agent.StateBlocked, agent.StateDone, "process", "process_exited")

	return replayAcceptanceFixture{
		base:          base,
		rows:          rows,
		partialOffset: OutputOffset(finalScreenStart + uint64(len("\x1b[?1049lba"))),
	}
}

func replayFixtureDirectly(
	t *testing.T,
	rows []store.EventRow,
	cursor Cursor,
) Frame {
	t.Helper()
	size, err := term.NewSize(initialFrameRows, initialFrameColumns)
	if err != nil {
		t.Fatalf("create direct replay terminal size: %v", err)
	}
	controller, err := term.NewController(size)
	if err != nil {
		t.Fatalf("create direct replay terminal: %v", err)
	}
	defer controller.Close()

	for _, row := range rows {
		if row.Seq > uint64(cursor.Seq) {
			break
		}
		switch event.Type(row.Type) {
		case event.TypeOutputChunk:
			payload, err := event.DecodeOutputChunkPayload(row.Payload)
			if err != nil {
				t.Fatalf("decode direct output at seq %d: %v", row.Seq, err)
			}
			end := payload.Offset + uint64(payload.Len)
			requiredEnd := min(end, uint64(cursor.NextOffset))
			if requiredEnd <= payload.Offset {
				continue
			}
			if err := controller.Write(
				row.OutputAttachment[:requiredEnd-payload.Offset],
			); err != nil {
				t.Fatalf("write direct output at seq %d: %v", row.Seq, err)
			}
		case event.TypeAgentResized:
			payload, err := event.DecodeAgentResizedPayload(row.Payload)
			if err != nil {
				t.Fatalf("decode direct resize at seq %d: %v", row.Seq, err)
			}
			resized, err := term.NewSize(int(payload.Rows), int(payload.Columns))
			if err != nil {
				t.Fatalf("create direct resize at seq %d: %v", row.Seq, err)
			}
			if err := controller.Resize(resized); err != nil {
				t.Fatalf("apply direct resize at seq %d: %v", row.Seq, err)
			}
		}
	}

	snapshot, err := controller.Snapshot()
	if err != nil {
		t.Fatalf("capture direct replay: %v", err)
	}
	view, err := snapshot.View(term.DefaultViewOptions())
	if err != nil {
		t.Fatalf("bound direct replay: %v", err)
	}
	return Frame{
		SessionID:  "s1",
		Cursor:     cursor,
		Rows:       uint16(snapshot.Size().Rows()),
		Columns:    uint16(snapshot.Size().Columns()),
		Lines:      view.Rows(),
		Truncated:  view.Truncated(),
		Fidelity:   FrameFidelityExactOriginReplay,
		Restorable: false,
	}
}
