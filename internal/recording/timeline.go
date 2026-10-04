package recording

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
)

const blockedLeadIn = 30 * time.Second

var (
	// ErrInvalidBlockedOccurrence reports a non-positive Blocked ordinal.
	ErrInvalidBlockedOccurrence = errors.New("recording: invalid blocked occurrence")
	// ErrBlockedOccurrenceNotFound reports an ordinal outside the captured timeline.
	ErrBlockedOccurrenceNotFound = errors.New("recording: blocked occurrence not found")
)

// StateSpan is one half-open state interval derived from state-change events.
type StateSpan struct {
	State          agent.State `json:"state"`
	Start          Cursor      `json:"start"`
	End            *Cursor     `json:"end,omitempty"`
	StartAt        time.Time   `json:"start_at"`
	EndAt          *time.Time  `json:"end_at,omitempty"`
	DurationMillis *int64      `json:"duration_ms,omitempty"`
	Source         string      `json:"source,omitempty"`
	Rule           string      `json:"rule,omitempty"`
	Reason         string      `json:"reason,omitempty"`
}

// BlockedOccurrence identifies one entry into the Blocked state.
type BlockedOccurrence struct {
	Number         int       `json:"number"`
	Span           StateSpan `json:"span"`
	Jump           Cursor    `json:"jump"`
	FrameAvailable bool      `json:"frame_available"`
}

// OutputCoverage reports the complete output range and retained subranges.
type OutputCoverage struct {
	Range    OutputRange   `json:"range"`
	Retained []OutputRange `json:"retained"`
	Missing  []OutputRange `json:"missing"`
}

// Timeline is a captured projection of one immutable session prefix.
type Timeline struct {
	SessionID      string              `json:"session_id"`
	AgentID        string              `json:"agent_id"`
	Captured       Cursor              `json:"captured"`
	CapturedAt     time.Time           `json:"captured_at"`
	DurationMillis int64               `json:"duration_ms"`
	Output         OutputCoverage      `json:"output"`
	Spans          []StateSpan         `json:"spans"`
	Blocked        []BlockedOccurrence `json:"blocked"`
}

// Timeline captures a session boundary and projects its state and output history.
func (a *Archive) Timeline(ctx context.Context, sessionID string) (Timeline, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	boundary, rows, err := a.readCapturedRows(ctx, sessionID, false)
	if err != nil {
		return Timeline{}, err
	}

	result := Timeline{
		SessionID: sessionID,
		Captured: Cursor{
			Seq:        Seq(boundary.LastSeq),
			NextOffset: OutputOffset(boundary.NextOutputOffset),
		},
		Output: OutputCoverage{
			Range:   OutputRange{End: OutputOffset(boundary.NextOutputOffset)},
			Missing: make([]OutputRange, 0),
		},
		Spans:   make([]StateSpan, 0),
		Blocked: make([]BlockedOccurrence, 0),
	}
	if len(rows) == 0 {
		return Timeline{}, fmt.Errorf("%w: %q", ErrUnknownSession, sessionID)
	}
	result.AgentID = rows[0].AgentID
	result.CapturedAt = rows[len(rows)-1].Timestamp
	result.DurationMillis = rows[len(rows)-1].Timestamp.Sub(rows[0].Timestamp).Milliseconds()
	if result.DurationMillis < 0 {
		result.DurationMillis = 0
	}

	cursor := Origin
	var currentState agent.State
	for _, row := range rows {
		if result.AgentID == "" {
			result.AgentID = row.AgentID
		}
		if err := advanceProjectionCursor(&cursor, row); err != nil {
			return Timeline{}, err
		}
		if event.Type(row.Type) == event.TypeOutputChunk {
			payload, decodeErr := event.DecodeOutputChunkPayload(row.Payload)
			if decodeErr != nil {
				return Timeline{}, fmt.Errorf(
					"recording: decode timeline output at seq %d: %w",
					row.Seq,
					decodeErr,
				)
			}
			if !row.OutputAttachmentPresent {
				end, addErr := checkedOutputEnd(payload.Offset, payload.Len)
				if addErr != nil {
					return Timeline{}, fmt.Errorf(
						"recording: timeline output at seq %d: %w",
						row.Seq,
						addErr,
					)
				}
				result.Output.Missing = appendOutputRange(
					result.Output.Missing,
					OutputRange{
						Start: OutputOffset(payload.Offset),
						End:   OutputOffset(end),
					},
				)
			}
		}
		if event.Type(row.Type) != event.TypeStateChanged {
			continue
		}

		from := agent.State(row.From)
		to := agent.State(row.To)
		if !agent.Valid(from) || !agent.Valid(to) ||
			!agent.CanRecoverTransition(from, to) {
			return Timeline{}, fmt.Errorf(
				"recording: invalid state transition at seq %d: %q -> %q",
				row.Seq,
				row.From,
				row.To,
			)
		}
		if currentState != "" && currentState != from {
			return Timeline{}, fmt.Errorf(
				"recording: state chain mismatch at seq %d: projected %q, event starts from %q",
				row.Seq,
				currentState,
				from,
			)
		}
		source, rule, decodeErr := decodeStateAttribution(row)
		if decodeErr != nil {
			return Timeline{}, decodeErr
		}
		if len(result.Spans) != 0 {
			closeSpan(&result.Spans[len(result.Spans)-1], cursor, row.Timestamp)
		}
		result.Spans = append(result.Spans, StateSpan{
			State:   to,
			Start:   cursor,
			StartAt: row.Timestamp,
			Source:  source,
			Rule:    rule,
			Reason:  row.Reason,
		})
		currentState = to
	}

	if cursor != result.Captured {
		return Timeline{}, fmt.Errorf(
			"recording: timeline cursor {%s,%s} does not match boundary {%s,%s}",
			cursor.Seq,
			cursor.NextOffset,
			result.Captured.Seq,
			result.Captured.NextOffset,
		)
	}
	if len(result.Spans) != 0 && terminalState(result.Spans[len(result.Spans)-1].State) {
		closeSpan(
			&result.Spans[len(result.Spans)-1],
			result.Captured,
			result.CapturedAt,
		)
	}
	result.Output.Retained = retainedOutputRanges(
		result.Output.Range,
		result.Output.Missing,
	)

	for _, span := range result.Spans {
		if span.State != agent.StateBlocked {
			continue
		}
		jump, jumpErr := a.resolveTimelineLeadIn(
			ctx,
			sessionID,
			span.StartAt.Add(-blockedLeadIn),
			boundary.ThroughSeq,
		)
		if jumpErr != nil {
			return Timeline{}, jumpErr
		}
		result.Blocked = append(result.Blocked, BlockedOccurrence{
			Number:         len(result.Blocked) + 1,
			Span:           span,
			Jump:           jump,
			FrameAvailable: frameRangeAvailable(jump.NextOffset, result.Output.Missing),
		})
	}
	return result, nil
}

// Blocked returns one one-based Blocked occurrence from a captured timeline.
func (a *Archive) Blocked(
	ctx context.Context,
	sessionID string,
	number int,
) (BlockedOccurrence, error) {
	if number <= 0 {
		return BlockedOccurrence{}, fmt.Errorf(
			"%w: %d",
			ErrInvalidBlockedOccurrence,
			number,
		)
	}
	timeline, err := a.Timeline(ctx, sessionID)
	if err != nil {
		return BlockedOccurrence{}, err
	}
	if number > len(timeline.Blocked) {
		return BlockedOccurrence{}, fmt.Errorf(
			"%w: %d",
			ErrBlockedOccurrenceNotFound,
			number,
		)
	}
	return timeline.Blocked[number-1], nil
}

func (a *Archive) readCapturedRows(
	ctx context.Context,
	sessionID string,
	hydrateOutput bool,
) (store.SessionBoundary, []store.EventRow, error) {
	if a == nil || a.store == nil {
		return store.SessionBoundary{}, nil, errors.New(
			"recording: archive is not configured",
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	boundary, found, err := a.store.SessionBoundary(ctx, sessionID, nil)
	if err != nil {
		return store.SessionBoundary{}, nil, fmt.Errorf(
			"recording: capture session boundary: %w",
			err,
		)
	}
	if !found {
		return store.SessionBoundary{}, nil, fmt.Errorf(
			"%w: %q",
			ErrUnknownSession,
			sessionID,
		)
	}

	rows := make([]store.EventRow, 0)
	after := uint64(0)
	for {
		page, readErr := a.store.ReadSessionRange(
			ctx,
			sessionID,
			after,
			boundary.ThroughSeq,
			a.limit,
			hydrateOutput,
		)
		if readErr != nil {
			return store.SessionBoundary{}, nil, fmt.Errorf(
				"recording: read captured session: %w",
				readErr,
			)
		}
		rows = append(rows, page.Rows...)
		if !page.More {
			break
		}
		after = page.ScannedThrough
	}
	return boundary, rows, nil
}

func (a *Archive) resolveTimelineLeadIn(
	ctx context.Context,
	sessionID string,
	at time.Time,
	throughSeq uint64,
) (Cursor, error) {
	position, found, err := a.store.ResolveSessionTime(
		ctx,
		sessionID,
		at,
		throughSeq,
	)
	if err != nil {
		return Cursor{}, fmt.Errorf("recording: resolve Blocked lead-in: %w", err)
	}
	if !found {
		return Origin, nil
	}
	return cursorFromPosition(position), nil
}

func advanceProjectionCursor(cursor *Cursor, row store.EventRow) error {
	switch event.Type(row.Type) {
	case event.TypeOutputChunk:
		payload, err := event.DecodeOutputChunkPayload(row.Payload)
		if err != nil {
			return fmt.Errorf(
				"recording: decode output at seq %d: %w",
				row.Seq,
				err,
			)
		}
		if payload.Offset != uint64(cursor.NextOffset) {
			return fmt.Errorf(
				"recording: non-contiguous output at seq %d: offset %d, want %s",
				row.Seq,
				payload.Offset,
				cursor.NextOffset,
			)
		}
		end, err := checkedOutputEnd(payload.Offset, payload.Len)
		if err != nil {
			return fmt.Errorf("recording: output at seq %d: %w", row.Seq, err)
		}
		cursor.NextOffset = OutputOffset(end)
	case event.TypeAgentResized:
		payload, err := event.DecodeAgentResizedPayload(row.Payload)
		if err != nil {
			return fmt.Errorf(
				"recording: decode resize at seq %d: %w",
				row.Seq,
				err,
			)
		}
		if payload.OutputOffset != uint64(cursor.NextOffset) {
			return fmt.Errorf(
				"recording: resize at seq %d has output offset %d, want %s",
				row.Seq,
				payload.OutputOffset,
				cursor.NextOffset,
			)
		}
	}
	cursor.Seq = Seq(row.Seq)
	return nil
}

func checkedOutputEnd(offset uint64, length int) (uint64, error) {
	if length < 0 || uint64(length) > math.MaxUint64-offset {
		return 0, fmt.Errorf("output offset %d plus length %d overflows", offset, length)
	}
	return offset + uint64(length), nil
}

func decodeStateAttribution(row store.EventRow) (string, string, error) {
	if row.Payload == "" {
		return "", "", nil
	}
	var version struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal([]byte(row.Payload), &version); err != nil {
		return "", "", fmt.Errorf(
			"recording: decode state evidence version at seq %d: %w",
			row.Seq,
			err,
		)
	}
	switch version.Version {
	case 0:
		return "", "", fmt.Errorf(
			"recording: state evidence version is required at seq %d",
			row.Seq,
		)
	case 1:
		var payload event.StateEvidencePayloadV1
		if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
			return "", "", fmt.Errorf(
				"recording: decode state evidence at seq %d: %w",
				row.Seq,
				err,
			)
		}
		if err := payload.Validate(); err != nil {
			return "", "", fmt.Errorf(
				"recording: validate state evidence at seq %d: %w",
				row.Seq,
				err,
			)
		}
		return payload.Source, "", nil
	case 2:
		var payload event.StateEvidencePayloadV2
		if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
			return "", "", fmt.Errorf(
				"recording: decode state evidence at seq %d: %w",
				row.Seq,
				err,
			)
		}
		if err := payload.Validate(); err != nil {
			return "", "", fmt.Errorf(
				"recording: validate state evidence at seq %d: %w",
				row.Seq,
				err,
			)
		}
		return payload.Source, "", nil
	case 3:
		var payload event.StateEvidencePayloadV3
		if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
			return "", "", fmt.Errorf(
				"recording: decode state evidence at seq %d: %w",
				row.Seq,
				err,
			)
		}
		if err := payload.Validate(); err != nil {
			return "", "", fmt.Errorf(
				"recording: validate state evidence at seq %d: %w",
				row.Seq,
				err,
			)
		}
		rule := ""
		if payload.Screen != nil {
			rule = payload.Screen.Rule
		}
		return payload.Source, rule, nil
	default:
		return "", "", nil
	}
}

func closeSpan(span *StateSpan, end Cursor, endAt time.Time) {
	closedAt := endAt
	closedCursor := end
	duration := endAt.Sub(span.StartAt).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	span.End = &closedCursor
	span.EndAt = &closedAt
	span.DurationMillis = &duration
}

func terminalState(state agent.State) bool {
	return state == agent.StateDone || state == agent.StateStopped
}

func appendOutputRange(ranges []OutputRange, next OutputRange) []OutputRange {
	if next.Start == next.End {
		return ranges
	}
	if len(ranges) != 0 && ranges[len(ranges)-1].End == next.Start {
		ranges[len(ranges)-1].End = next.End
		return ranges
	}
	return append(ranges, next)
}

func retainedOutputRanges(full OutputRange, missing []OutputRange) []OutputRange {
	if full.Start == full.End {
		return []OutputRange{}
	}
	retained := make([]OutputRange, 0, len(missing)+1)
	start := full.Start
	for _, hole := range missing {
		if start < hole.Start {
			retained = append(retained, OutputRange{Start: start, End: hole.Start})
		}
		start = hole.End
	}
	if start < full.End {
		retained = append(retained, OutputRange{Start: start, End: full.End})
	}
	return retained
}

func frameRangeAvailable(head OutputOffset, missing []OutputRange) bool {
	for _, hole := range missing {
		if hole.Start < head {
			return false
		}
	}
	return true
}
