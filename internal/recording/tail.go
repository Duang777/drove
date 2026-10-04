package recording

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"time"

	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
)

var (
	// ErrUnknownSession reports that the recording has no events for a session.
	ErrUnknownSession = errors.New("recording: unknown session")
	// ErrInvalidCursor reports a cursor that does not match immutable history.
	ErrInvalidCursor = errors.New("recording: invalid cursor")
)

// OutputRange is a half-open range of terminal output bytes.
type OutputRange struct {
	Start OutputOffset `json:"start"`
	End   OutputOffset `json:"end"`
}

// OutputExpiredError reports output bytes whose durable attachment is gone.
type OutputExpiredError struct {
	SessionID string        `json:"session_id"`
	Missing   []OutputRange `json:"missing"`
}

func (e *OutputExpiredError) Error() string {
	if e == nil || len(e.Missing) == 0 {
		return "recording: output expired"
	}
	return fmt.Sprintf(
		"recording: output expired for session %q in [%s,%s)",
		e.SessionID,
		e.Missing[0].Start,
		e.Missing[0].End,
	)
}

// CommitClock wakes readers after a durable global sequence advances.
type CommitClock interface {
	HighWatermark() uint64
	WaitForCommit(context.Context, uint64) (uint64, error)
}

type rangeStore interface {
	SessionBoundary(
		context.Context,
		string,
		*uint64,
	) (store.SessionBoundary, bool, error)
	ReadSessionRange(
		context.Context,
		string,
		uint64,
		uint64,
		store.ReadLimit,
		bool,
	) (store.EventPage, error)
	ResolveSessionSequence(
		context.Context,
		string,
		uint64,
		uint64,
	) (store.SessionPosition, bool, error)
	ResolveSessionTime(
		context.Context,
		string,
		time.Time,
		uint64,
	) (store.SessionPosition, bool, error)
	ResolveSessionOutputOffset(
		context.Context,
		string,
		uint64,
		uint64,
	) (store.OutputOffsetPosition, bool, error)
	OutputRetentionGeneration() uint64
}

// Archive reads immutable terminal recordings and follows their durable tail.
type Archive struct {
	store  rangeStore
	clock  CommitClock
	limit  store.ReadLimit
	frames *frameCache
}

// NewArchive constructs a recording reader over a Store and commit clock.
func NewArchive(st *store.Store, clock CommitClock) *Archive {
	return &Archive{
		store:  st,
		clock:  clock,
		limit:  store.DefaultReadLimit(),
		frames: newFrameCache(defaultFrameCacheEntries),
	}
}

// RawItem is one ordered item from a raw terminal tail.
type RawItem interface {
	isRawItem()
}

// RawOutput is one retained output chunk or requested chunk suffix.
type RawOutput struct {
	AgentID    string
	Sequence   Seq
	Offset     OutputOffset
	Data       []byte
	Cursor     Cursor
	Historical bool
}

func (RawOutput) isRawItem() {}

// Resize is one durable terminal resize.
type Resize struct {
	AgentID      string
	Sequence     Seq
	Rows         uint16
	Columns      uint16
	OutputOffset OutputOffset
	Cursor       Cursor
	Historical   bool
}

func (Resize) isRawItem() {}

// CaughtUp marks the boundary between captured history and future commits.
type CaughtUp struct {
	Cursor Cursor
}

func (CaughtUp) isRawItem()   {}
func (CaughtUp) isEventItem() {}

// EventItem is one ordered item from an event tail.
type EventItem interface {
	isEventItem()
}

// EventRecord carries one immutable event envelope and its resulting cursor.
type EventRecord struct {
	Event      event.Event
	Cursor     Cursor
	Historical bool
}

func (EventRecord) isEventItem() {}

type tailState struct {
	ctx              context.Context
	cancel           context.CancelFunc
	archive          *Archive
	sessionID        string
	initialHead      uint64
	scannedThrough   uint64
	startCursor      Cursor
	cursor           Cursor
	partialOutputSeq uint64
	caughtUp         bool
	failed           error
	closeOnce        sync.Once
}

// RawTail reads retained terminal output and resizes in durable order.
type RawTail struct {
	state   *tailState
	pending []RawItem
}

// EventTail reads immutable event envelopes in durable order.
type EventTail struct {
	state   *tailState
	pending []EventItem
}

// TailRaw captures a history boundary and opens an independently cancelable tail.
func (a *Archive) TailRaw(
	ctx context.Context,
	sessionID string,
	selector *Selector,
) (*RawTail, error) {
	state, err := a.newTailState(ctx, sessionID, selector)
	if err != nil {
		return nil, err
	}
	return &RawTail{state: &state}, nil
}

// TailEvents captures a history boundary and opens an independently cancelable tail.
func (a *Archive) TailEvents(
	ctx context.Context,
	sessionID string,
	selector *Selector,
) (*EventTail, error) {
	state, err := a.newTailState(ctx, sessionID, selector)
	if err != nil {
		return nil, err
	}
	return &EventTail{state: &state}, nil
}

func (a *Archive) newTailState(
	ctx context.Context,
	sessionID string,
	selector *Selector,
) (tailState, error) {
	if a == nil || a.store == nil || a.clock == nil {
		return tailState{}, errors.New("recording: archive is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	boundary, found, err := a.store.SessionBoundary(ctx, sessionID, nil)
	if err != nil {
		return tailState{}, fmt.Errorf("recording: capture session boundary: %w", err)
	}
	if !found {
		return tailState{}, fmt.Errorf("%w: %q", ErrUnknownSession, sessionID)
	}
	position, partialOutputSeq, err := a.resolveStart(
		ctx,
		sessionID,
		boundary,
		selector,
	)
	if err != nil {
		return tailState{}, err
	}
	tailContext, cancel := context.WithCancel(ctx)
	return tailState{
		ctx:              tailContext,
		cancel:           cancel,
		archive:          a,
		sessionID:        sessionID,
		initialHead:      boundary.GlobalSeq,
		scannedThrough:   uint64(position.Seq),
		startCursor:      position,
		cursor:           position,
		partialOutputSeq: partialOutputSeq,
	}, nil
}

func (a *Archive) resolveStart(
	ctx context.Context,
	sessionID string,
	boundary store.SessionBoundary,
	selector *Selector,
) (Cursor, uint64, error) {
	if selector == nil {
		return Origin, 0, nil
	}

	switch selector.Kind() {
	case SelectorCursor:
		cursor, _ := selector.Cursor()
		if cursor == Origin {
			return Origin, 0, nil
		}
		if uint64(cursor.Seq) > boundary.ThroughSeq {
			return Cursor{}, 0, fmt.Errorf(
				"%w: sequence %s exceeds durable head %d",
				ErrInvalidCursor,
				cursor.Seq,
				boundary.ThroughSeq,
			)
		}
		position, found, err := a.store.ResolveSessionSequence(
			ctx,
			sessionID,
			uint64(cursor.Seq),
			boundary.ThroughSeq,
		)
		if err != nil {
			return Cursor{}, 0, fmt.Errorf("recording: resolve cursor: %w", err)
		}
		if !found ||
			position.Seq != uint64(cursor.Seq) ||
			position.NextOutputOffset != uint64(cursor.NextOffset) {
			return Cursor{}, 0, fmt.Errorf(
				"%w: {%s,%s} does not match session history",
				ErrInvalidCursor,
				cursor.Seq,
				cursor.NextOffset,
			)
		}
		return cursor, 0, nil
	case SelectorSequence:
		sequence, _ := selector.Sequence()
		if sequence == 0 {
			return Origin, 0, nil
		}
		if uint64(sequence) > boundary.ThroughSeq {
			return Cursor{}, 0, fmt.Errorf(
				"%w: sequence %s exceeds durable head %d",
				ErrInvalidCursor,
				sequence,
				boundary.ThroughSeq,
			)
		}
		position, found, err := a.store.ResolveSessionSequence(
			ctx,
			sessionID,
			uint64(sequence),
			boundary.ThroughSeq,
		)
		if err != nil {
			return Cursor{}, 0, fmt.Errorf("recording: resolve sequence: %w", err)
		}
		if !found {
			return Origin, 0, nil
		}
		return cursorFromPosition(position), 0, nil
	case SelectorTime:
		at, _ := selector.Time()
		position, found, err := a.store.ResolveSessionTime(
			ctx,
			sessionID,
			at,
			boundary.ThroughSeq,
		)
		if err != nil {
			return Cursor{}, 0, fmt.Errorf("recording: resolve timestamp: %w", err)
		}
		if !found {
			return Origin, 0, nil
		}
		return cursorFromPosition(position), 0, nil
	case SelectorOutputOffset:
		offset, _ := selector.OutputOffset()
		position, found, err := a.store.ResolveSessionOutputOffset(
			ctx,
			sessionID,
			uint64(offset),
			boundary.ThroughSeq,
		)
		if err != nil {
			return Cursor{}, 0, fmt.Errorf("recording: resolve output offset: %w", err)
		}
		if !found {
			return Cursor{}, 0, fmt.Errorf(
				"%w: output offset %s does not match session history",
				ErrInvalidCursor,
				offset,
			)
		}
		cursor := cursorFromPosition(position.Position)
		cursor.NextOffset = offset
		return cursor, position.PartialOutputSeq, nil
	default:
		return Cursor{}, 0, errors.New("recording: invalid selector kind")
	}
}

// StartCursor returns the validated position from which this raw tail starts.
func (t *RawTail) StartCursor() Cursor {
	if t == nil {
		return Origin
	}
	return t.state.startCursor
}

// Next returns the next raw output, resize, or caught-up marker.
func (t *RawTail) Next() (RawItem, error) {
	for {
		if len(t.pending) != 0 {
			item := t.pending[0]
			t.pending = t.pending[1:]
			return item, nil
		}
		if t.state.failed != nil {
			return nil, t.state.failed
		}
		if !t.state.caughtUp && t.state.scannedThrough >= t.state.initialHead {
			t.state.caughtUp = true
			return CaughtUp{Cursor: t.state.cursor}, nil
		}
		high, err := t.state.nextHigh()
		if err != nil {
			return nil, err
		}
		page, err := t.state.archive.store.ReadSessionRange(
			t.state.ctx,
			t.state.sessionID,
			t.state.scannedThrough,
			high,
			t.state.archive.limit,
			true,
		)
		if err != nil {
			return nil, fmt.Errorf("recording: read raw tail: %w", err)
		}
		for _, row := range page.Rows {
			item, emit, advanceErr := t.rawItem(row)
			if advanceErr != nil {
				t.state.failed = advanceErr
				break
			}
			if emit {
				t.pending = append(t.pending, item)
			}
		}
		if t.state.failed == nil {
			t.state.scannedThrough = page.ScannedThrough
		}
	}
}

func (t *RawTail) rawItem(row store.EventRow) (RawItem, bool, error) {
	historical := row.Seq <= t.state.initialHead
	switch event.Type(row.Type) {
	case event.TypeOutputChunk:
		payload, start, err := t.state.outputPayload(row)
		if err != nil {
			return nil, false, err
		}
		if !row.OutputAttachmentPresent {
			return nil, false, &OutputExpiredError{
				SessionID: t.state.sessionID,
				Missing: []OutputRange{{
					Start: OutputOffset(start),
					End:   OutputOffset(payload.Offset + uint64(payload.Len)),
				}},
			}
		}
		if len(row.OutputAttachment) != payload.Len {
			return nil, false, fmt.Errorf(
				"recording: output attachment at seq %d has length %d, want %d",
				row.Seq,
				len(row.OutputAttachment),
				payload.Len,
			)
		}
		sliceAt := start - payload.Offset
		data := append([]byte(nil), row.OutputAttachment[sliceAt:]...)
		t.state.advanceOutput(row, payload)
		return RawOutput{
			AgentID:    row.AgentID,
			Sequence:   Seq(row.Seq),
			Offset:     OutputOffset(start),
			Data:       data,
			Cursor:     t.state.cursor,
			Historical: historical,
		}, true, nil
	case event.TypeAgentResized:
		payload, err := event.DecodeAgentResizedPayload(row.Payload)
		if err != nil {
			return nil, false, fmt.Errorf(
				"recording: decode resize at seq %d: %w",
				row.Seq,
				err,
			)
		}
		if payload.OutputOffset != uint64(t.state.cursor.NextOffset) {
			return nil, false, fmt.Errorf(
				"recording: resize at seq %d has output offset %d, want %s",
				row.Seq,
				payload.OutputOffset,
				t.state.cursor.NextOffset,
			)
		}
		t.state.cursor.Seq = Seq(row.Seq)
		return Resize{
			AgentID:      row.AgentID,
			Sequence:     Seq(row.Seq),
			Rows:         payload.Rows,
			Columns:      payload.Columns,
			OutputOffset: OutputOffset(payload.OutputOffset),
			Cursor:       t.state.cursor,
			Historical:   historical,
		}, true, nil
	default:
		if err := t.state.advanceNonOutput(row); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
}

// Close cancels this raw tail without affecting other readers.
func (t *RawTail) Close() error {
	if t == nil {
		return nil
	}
	t.state.closeOnce.Do(t.state.cancel)
	return nil
}

// StartCursor returns the validated position from which this event tail starts.
func (t *EventTail) StartCursor() Cursor {
	if t == nil {
		return Origin
	}
	return t.state.startCursor
}

// Next returns the next event envelope or caught-up marker.
func (t *EventTail) Next() (EventItem, error) {
	for {
		if len(t.pending) != 0 {
			item := t.pending[0]
			t.pending = t.pending[1:]
			return item, nil
		}
		if t.state.failed != nil {
			return nil, t.state.failed
		}
		if !t.state.caughtUp && t.state.scannedThrough >= t.state.initialHead {
			t.state.caughtUp = true
			return CaughtUp{Cursor: t.state.cursor}, nil
		}
		high, err := t.state.nextHigh()
		if err != nil {
			return nil, err
		}
		page, err := t.state.archive.store.ReadSessionRange(
			t.state.ctx,
			t.state.sessionID,
			t.state.scannedThrough,
			high,
			t.state.archive.limit,
			false,
		)
		if err != nil {
			return nil, fmt.Errorf("recording: read event tail: %w", err)
		}
		for _, row := range page.Rows {
			if err := t.state.advanceRow(row); err != nil {
				t.state.failed = err
				break
			}
			t.pending = append(t.pending, EventRecord{
				Event: event.Event{
					Seq:       row.Seq,
					Timestamp: row.Timestamp,
					Type:      event.Type(row.Type),
					AgentID:   row.AgentID,
					SessionID: row.SessionID,
					From:      row.From,
					To:        row.To,
					Reason:    row.Reason,
					Payload:   row.Payload,
				},
				Cursor:     t.state.cursor,
				Historical: row.Seq <= t.state.initialHead,
			})
		}
		if t.state.failed == nil {
			t.state.scannedThrough = page.ScannedThrough
		}
	}
}

// Close cancels this event tail without affecting other readers.
func (t *EventTail) Close() error {
	if t == nil {
		return nil
	}
	t.state.closeOnce.Do(t.state.cancel)
	return nil
}

func (t *tailState) nextHigh() (uint64, error) {
	if !t.caughtUp {
		return t.initialHead, nil
	}
	for {
		if err := t.ctx.Err(); err != nil {
			return 0, io.EOF
		}
		high := t.archive.clock.HighWatermark()
		if high > t.scannedThrough {
			return high, nil
		}
		high, err := t.archive.clock.WaitForCommit(t.ctx, t.scannedThrough)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return 0, io.EOF
			}
			return 0, fmt.Errorf("recording: wait for committed tail: %w", err)
		}
		if high > t.scannedThrough {
			return high, nil
		}
	}
}

func (t *tailState) advanceRow(row store.EventRow) error {
	switch event.Type(row.Type) {
	case event.TypeOutputChunk:
		payload, _, err := t.outputPayload(row)
		if err != nil {
			return err
		}
		t.advanceOutput(row, payload)
		return nil
	default:
		return t.advanceNonOutput(row)
	}
}

func (t *tailState) outputPayload(
	row store.EventRow,
) (event.OutputChunkPayloadV1, uint64, error) {
	payload, err := event.DecodeOutputChunkPayload(row.Payload)
	if err != nil {
		return event.OutputChunkPayloadV1{}, 0, fmt.Errorf(
			"recording: decode output at seq %d: %w",
			row.Seq,
			err,
		)
	}
	if payload.Offset > math.MaxUint64-uint64(payload.Len) {
		return event.OutputChunkPayloadV1{}, 0, fmt.Errorf(
			"recording: output at seq %d overflows its byte offset",
			row.Seq,
		)
	}
	end := payload.Offset + uint64(payload.Len)
	start := payload.Offset
	if row.Seq == t.partialOutputSeq {
		start = uint64(t.cursor.NextOffset)
		if start <= payload.Offset || start >= end {
			return event.OutputChunkPayloadV1{}, 0, fmt.Errorf(
				"recording: invalid partial output position %d in [%d,%d)",
				start,
				payload.Offset,
				end,
			)
		}
		t.partialOutputSeq = 0
	} else if payload.Offset != uint64(t.cursor.NextOffset) {
		return event.OutputChunkPayloadV1{}, 0, fmt.Errorf(
			"recording: non-contiguous output at seq %d: offset %d, want %s",
			row.Seq,
			payload.Offset,
			t.cursor.NextOffset,
		)
	}
	return payload, start, nil
}

func (t *tailState) advanceOutput(
	row store.EventRow,
	payload event.OutputChunkPayloadV1,
) {
	t.cursor.Seq = Seq(row.Seq)
	t.cursor.NextOffset = OutputOffset(payload.Offset + uint64(payload.Len))
}

func (t *tailState) advanceNonOutput(row store.EventRow) error {
	if event.Type(row.Type) == event.TypeAgentResized {
		payload, err := event.DecodeAgentResizedPayload(row.Payload)
		if err != nil {
			return fmt.Errorf("recording: decode resize at seq %d: %w", row.Seq, err)
		}
		if payload.OutputOffset != uint64(t.cursor.NextOffset) {
			return fmt.Errorf(
				"recording: resize at seq %d has output offset %d, want %s",
				row.Seq,
				payload.OutputOffset,
				t.cursor.NextOffset,
			)
		}
	}
	t.cursor.Seq = Seq(row.Seq)
	return nil
}

func cursorFromPosition(position store.SessionPosition) Cursor {
	return Cursor{
		Seq:        Seq(position.Seq),
		NextOffset: OutputOffset(position.NextOutputOffset),
	}
}
