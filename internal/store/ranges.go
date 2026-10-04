package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/Duang777/drove/internal/event"
)

const (
	// DefaultRangeRows is the normal maximum event count returned by one page.
	DefaultRangeRows = 256
	// MaxRangeRows is the hard maximum event count returned by one page.
	MaxRangeRows = 4096
	// DefaultRangeAttachmentBytes is the normal hydrated byte budget per page.
	DefaultRangeAttachmentBytes = 1 << 20
	// MaxRangeAttachmentBytes is the hard hydrated byte budget per page.
	MaxRangeAttachmentBytes = 8 << 20
)

// ReadLimit bounds one materialized event range page.
type ReadLimit struct {
	Rows            int
	AttachmentBytes int
}

// DefaultReadLimit returns the standard short range-read limits.
func DefaultReadLimit() ReadLimit {
	return ReadLimit{
		Rows:            DefaultRangeRows,
		AttachmentBytes: DefaultRangeAttachmentBytes,
	}
}

// EventPage is a fully materialized bounded session range.
type EventPage struct {
	Rows            []EventRow
	ScannedThrough  uint64
	More            bool
	AttachmentBytes int
}

// SessionBoundary captures one immutable session prefix and the global head
// observed in the same short read transaction.
type SessionBoundary struct {
	GlobalSeq        uint64
	ThroughSeq       uint64
	FirstSeq         uint64
	LastSeq          uint64
	NextOutputOffset uint64
}

// SessionPosition is the canonical position after a session event prefix.
type SessionPosition struct {
	Seq              uint64
	Timestamp        time.Time
	NextOutputOffset uint64
}

// OutputOffsetPosition resolves an inclusive byte selector. PartialOutputSeq
// is non-zero only when the selector falls inside that output event.
type OutputOffsetPosition struct {
	Position         SessionPosition
	PartialOutputSeq uint64
	OutputStart      uint64
	OutputEnd        uint64
}

// SessionBoundary captures the durable global head and one session prefix.
// A nil throughSeq selects the current global head.
func (s *Store) SessionBoundary(
	ctx context.Context,
	sessionID string,
	throughSeq *uint64,
) (SessionBoundary, bool, error) {
	if sessionID == "" {
		return SessionBoundary{}, false, errors.New(
			"store: session boundary requires a session ID",
		)
	}
	tx, err := s.readDB().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SessionBoundary{}, false, fmt.Errorf(
			"store: begin session boundary read: %w",
			err,
		)
	}
	defer tx.Rollback()

	var globalRaw sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(seq) FROM events`).Scan(&globalRaw); err != nil {
		return SessionBoundary{}, false, fmt.Errorf(
			"store: read session boundary global sequence: %w",
			err,
		)
	}
	if globalRaw.Valid && globalRaw.Int64 < 0 {
		return SessionBoundary{}, false, fmt.Errorf(
			"store: invalid negative global sequence %d",
			globalRaw.Int64,
		)
	}
	globalSeq := uint64(globalRaw.Int64)
	through := globalSeq
	if throughSeq != nil {
		if *throughSeq > math.MaxInt64 {
			return SessionBoundary{}, false, fmt.Errorf(
				"store: session boundary sequence %d exceeds SQLite range",
				*throughSeq,
			)
		}
		if *throughSeq > globalSeq {
			return SessionBoundary{}, false, fmt.Errorf(
				"store: session boundary sequence %d exceeds durable head %d",
				*throughSeq,
				globalSeq,
			)
		}
		through = *throughSeq
	}

	boundary := SessionBoundary{GlobalSeq: globalSeq, ThroughSeq: through}
	var firstRaw, lastRaw sql.NullInt64
	if err := tx.QueryRowContext(
		ctx,
		`SELECT MIN(seq), MAX(seq)
		 FROM events
		 WHERE session_id = ? AND seq <= ?`,
		sessionID,
		through,
	).Scan(&firstRaw, &lastRaw); err != nil {
		return SessionBoundary{}, false, fmt.Errorf(
			"store: read session sequence boundary: %w",
			err,
		)
	}
	if !firstRaw.Valid {
		if err := tx.Commit(); err != nil {
			return SessionBoundary{}, false, fmt.Errorf(
				"store: finish empty session boundary read: %w",
				err,
			)
		}
		return boundary, false, nil
	}
	if firstRaw.Int64 <= 0 || lastRaw.Int64 <= 0 {
		return SessionBoundary{}, false, fmt.Errorf(
			"store: invalid session boundary %d..%d",
			firstRaw.Int64,
			lastRaw.Int64,
		)
	}
	boundary.FirstSeq = uint64(firstRaw.Int64)
	boundary.LastSeq = uint64(lastRaw.Int64)

	var payload string
	err = tx.QueryRowContext(
		ctx,
		`SELECT payload
		 FROM events
		 WHERE session_id = ? AND type = ? AND seq <= ?
		 ORDER BY seq DESC
		 LIMIT 1`,
		sessionID,
		string(event.TypeOutputChunk),
		through,
	).Scan(&payload)
	switch {
	case err == nil:
		chunk, decodeErr := event.DecodeOutputChunkPayload(payload)
		if decodeErr != nil {
			return SessionBoundary{}, false, fmt.Errorf(
				"store: decode session boundary output: %w",
				decodeErr,
			)
		}
		nextOffset, addErr := addOutputOffset(chunk.Offset, chunk.Len)
		if addErr != nil {
			return SessionBoundary{}, false, fmt.Errorf(
				"store: compute session boundary output: %w",
				addErr,
			)
		}
		boundary.NextOutputOffset = nextOffset
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return SessionBoundary{}, false, fmt.Errorf(
			"store: read session boundary output: %w",
			err,
		)
	}

	if err := tx.Commit(); err != nil {
		return SessionBoundary{}, false, fmt.Errorf(
			"store: finish session boundary read: %w",
			err,
		)
	}
	return boundary, true, nil
}

// ReadSessionRange materializes a bounded page in (afterSeq, throughSeq].
// It closes the SQL rows before returning and never waits for future events.
func (s *Store) ReadSessionRange(
	ctx context.Context,
	sessionID string,
	afterSeq uint64,
	throughSeq uint64,
	limit ReadLimit,
	hydrateOutput bool,
) (EventPage, error) {
	if sessionID == "" {
		return EventPage{}, errors.New("store: session range requires a session ID")
	}
	if afterSeq > throughSeq {
		return EventPage{}, fmt.Errorf(
			"store: session range starts after its boundary: %d > %d",
			afterSeq,
			throughSeq,
		)
	}
	if afterSeq > math.MaxInt64 || throughSeq > math.MaxInt64 {
		return EventPage{}, errors.New("store: session range sequence exceeds SQLite range")
	}
	if err := limit.validate(); err != nil {
		return EventPage{}, err
	}
	if afterSeq == throughSeq {
		return EventPage{ScannedThrough: throughSeq}, nil
	}

	hydrate := 0
	if hydrateOutput {
		hydrate = 1
	}
	rows, err := s.readDB().QueryContext(
		ctx,
		`SELECT e.seq, e.ts, e.type, e.session_id, e.agent_id, e.from_state,
		        e.to_state, e.reason, e.payload,
		        CASE WHEN o.event_seq IS NULL THEN 0 ELSE 1 END,
		        CASE WHEN ? = 1 THEN o.data ELSE NULL END
		 FROM events AS e
		 LEFT JOIN output_chunks AS o ON o.event_seq = e.seq
		 WHERE e.session_id = ? AND e.seq > ? AND e.seq <= ?
		 ORDER BY e.seq ASC
		 LIMIT ?`,
		hydrate,
		sessionID,
		afterSeq,
		throughSeq,
		limit.Rows+1,
	)
	if err != nil {
		return EventPage{}, fmt.Errorf("store: query session range: %w", err)
	}
	defer rows.Close()

	page := EventPage{Rows: make([]EventRow, 0, limit.Rows)}
	for rows.Next() {
		row, scanErr := scanRangeRow(rows)
		if scanErr != nil {
			return EventPage{}, scanErr
		}
		if len(page.Rows) == limit.Rows {
			page.More = true
			break
		}
		if row.Type == string(event.TypeOutputChunk) {
			payload, decodeErr := event.DecodeOutputChunkPayload(row.Payload)
			if decodeErr != nil {
				return EventPage{}, fmt.Errorf(
					"store: decode output metadata at seq %d: %w",
					row.Seq,
					decodeErr,
				)
			}
			if payload.DataB64 != "" {
				return EventPage{}, fmt.Errorf(
					"store: output metadata at seq %d contains embedded data",
					row.Seq,
				)
			}
			if hydrateOutput && row.OutputAttachmentPresent &&
				len(row.OutputAttachment) != payload.Len {
				return EventPage{}, fmt.Errorf(
					"store: output attachment at seq %d has length %d, want %d",
					row.Seq,
					len(row.OutputAttachment),
					payload.Len,
				)
			}
		}
		if hydrateOutput && row.OutputAttachmentPresent {
			if len(row.OutputAttachment) > limit.AttachmentBytes {
				return EventPage{}, fmt.Errorf(
					"store: attachment at seq %d exceeds page byte limit",
					row.Seq,
				)
			}
			if page.AttachmentBytes > limit.AttachmentBytes-len(row.OutputAttachment) {
				page.More = true
				break
			}
			page.AttachmentBytes += len(row.OutputAttachment)
		}
		page.Rows = append(page.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return EventPage{}, fmt.Errorf("store: iterate session range: %w", err)
	}
	if page.More {
		if len(page.Rows) == 0 {
			return EventPage{}, errors.New("store: session range made no progress")
		}
		page.ScannedThrough = page.Rows[len(page.Rows)-1].Seq
	} else {
		page.ScannedThrough = throughSeq
	}
	return page, nil
}

// ResolveSessionSequence returns the canonical session position at or before
// a global sequence, bounded by a captured durable prefix.
func (s *Store) ResolveSessionSequence(
	ctx context.Context,
	sessionID string,
	sequence uint64,
	throughSeq uint64,
) (SessionPosition, bool, error) {
	if sequence > throughSeq {
		return SessionPosition{}, false, fmt.Errorf(
			"store: sequence selector %d exceeds boundary %d",
			sequence,
			throughSeq,
		)
	}
	return s.positionThrough(ctx, sessionID, sequence)
}

// ResolveSessionTime resolves the greatest (timestamp, sequence) pair at or
// before at, then returns the canonical position through that sequence.
func (s *Store) ResolveSessionTime(
	ctx context.Context,
	sessionID string,
	at time.Time,
	throughSeq uint64,
) (SessionPosition, bool, error) {
	if at.IsZero() {
		return SessionPosition{}, false, errors.New(
			"store: timestamp selector is required",
		)
	}
	var selected EventRow
	found := false
	after := uint64(0)
	for {
		page, err := s.ReadSessionRange(
			ctx,
			sessionID,
			after,
			throughSeq,
			DefaultReadLimit(),
			false,
		)
		if err != nil {
			return SessionPosition{}, false, err
		}
		for _, row := range page.Rows {
			if row.Timestamp.After(at) {
				continue
			}
			if !found || row.Timestamp.After(selected.Timestamp) ||
				(row.Timestamp.Equal(selected.Timestamp) && row.Seq > selected.Seq) {
				selected = row
				found = true
			}
		}
		if !page.More {
			break
		}
		after = page.ScannedThrough
	}
	if !found {
		return SessionPosition{}, false, nil
	}
	return s.positionThrough(ctx, sessionID, selected.Seq)
}

// ResolveSessionOutputOffset resolves an inclusive output offset. A selector
// inside a chunk reports that chunk separately because it is not a full cursor.
func (s *Store) ResolveSessionOutputOffset(
	ctx context.Context,
	sessionID string,
	offset uint64,
	throughSeq uint64,
) (OutputOffsetPosition, bool, error) {
	position := SessionPosition{}
	after := uint64(0)
	for {
		page, err := s.ReadSessionRange(
			ctx,
			sessionID,
			after,
			throughSeq,
			DefaultReadLimit(),
			false,
		)
		if err != nil {
			return OutputOffsetPosition{}, false, err
		}
		for _, row := range page.Rows {
			if row.Type == string(event.TypeOutputChunk) {
				payload, decodeErr := event.DecodeOutputChunkPayload(row.Payload)
				if decodeErr != nil {
					return OutputOffsetPosition{}, false, fmt.Errorf(
						"store: resolve output offset at seq %d: %w",
						row.Seq,
						decodeErr,
					)
				}
				if payload.Offset != position.NextOutputOffset {
					return OutputOffsetPosition{}, false, fmt.Errorf(
						"store: non-contiguous output at seq %d: offset %d, want %d",
						row.Seq,
						payload.Offset,
						position.NextOutputOffset,
					)
				}
				end, addErr := addOutputOffset(payload.Offset, payload.Len)
				if addErr != nil {
					return OutputOffsetPosition{}, false, fmt.Errorf(
						"store: resolve output offset at seq %d: %w",
						row.Seq,
						addErr,
					)
				}
				if offset == payload.Offset {
					position.NextOutputOffset = offset
					return OutputOffsetPosition{Position: position}, true, nil
				}
				if offset > payload.Offset && offset < end {
					position.NextOutputOffset = offset
					return OutputOffsetPosition{
						Position:         position,
						PartialOutputSeq: row.Seq,
						OutputStart:      payload.Offset,
						OutputEnd:        end,
					}, true, nil
				}
				position.NextOutputOffset = end
			} else if row.Type == string(event.TypeAgentResized) {
				payload, decodeErr := event.DecodeAgentResizedPayload(row.Payload)
				if decodeErr != nil {
					return OutputOffsetPosition{}, false, fmt.Errorf(
						"store: resolve resize offset at seq %d: %w",
						row.Seq,
						decodeErr,
					)
				}
				if payload.OutputOffset != position.NextOutputOffset {
					return OutputOffsetPosition{}, false, fmt.Errorf(
						"store: resize at seq %d has output offset %d, want %d",
						row.Seq,
						payload.OutputOffset,
						position.NextOutputOffset,
					)
				}
			}
			position.Seq = row.Seq
			position.Timestamp = row.Timestamp
		}
		if !page.More {
			break
		}
		after = page.ScannedThrough
	}
	if offset != position.NextOutputOffset {
		return OutputOffsetPosition{}, false, nil
	}
	return OutputOffsetPosition{Position: position}, true, nil
}

func (s *Store) positionThrough(
	ctx context.Context,
	sessionID string,
	throughSeq uint64,
) (SessionPosition, bool, error) {
	position := SessionPosition{}
	found := false
	after := uint64(0)
	for {
		page, err := s.ReadSessionRange(
			ctx,
			sessionID,
			after,
			throughSeq,
			DefaultReadLimit(),
			false,
		)
		if err != nil {
			return SessionPosition{}, false, err
		}
		for _, row := range page.Rows {
			if err := advanceSessionPosition(&position, row); err != nil {
				return SessionPosition{}, false, err
			}
			found = true
		}
		if !page.More {
			return position, found, nil
		}
		after = page.ScannedThrough
	}
}

func advanceSessionPosition(position *SessionPosition, row EventRow) error {
	switch event.Type(row.Type) {
	case event.TypeOutputChunk:
		payload, err := event.DecodeOutputChunkPayload(row.Payload)
		if err != nil {
			return fmt.Errorf("store: decode output at seq %d: %w", row.Seq, err)
		}
		if payload.Offset != position.NextOutputOffset {
			return fmt.Errorf(
				"store: non-contiguous output at seq %d: offset %d, want %d",
				row.Seq,
				payload.Offset,
				position.NextOutputOffset,
			)
		}
		nextOffset, err := addOutputOffset(payload.Offset, payload.Len)
		if err != nil {
			return fmt.Errorf("store: advance output at seq %d: %w", row.Seq, err)
		}
		position.NextOutputOffset = nextOffset
	case event.TypeAgentResized:
		payload, err := event.DecodeAgentResizedPayload(row.Payload)
		if err != nil {
			return fmt.Errorf("store: decode resize at seq %d: %w", row.Seq, err)
		}
		if payload.OutputOffset != position.NextOutputOffset {
			return fmt.Errorf(
				"store: resize at seq %d has output offset %d, want %d",
				row.Seq,
				payload.OutputOffset,
				position.NextOutputOffset,
			)
		}
	}
	position.Seq = row.Seq
	position.Timestamp = row.Timestamp
	return nil
}

func scanRangeRow(rows *sql.Rows) (EventRow, error) {
	var row EventRow
	var rawSeq int64
	var timestamp string
	var attachmentPresent int
	if err := rows.Scan(
		&rawSeq,
		&timestamp,
		&row.Type,
		&row.SessionID,
		&row.AgentID,
		&row.From,
		&row.To,
		&row.Reason,
		&row.Payload,
		&attachmentPresent,
		&row.OutputAttachment,
	); err != nil {
		return EventRow{}, fmt.Errorf("store: scan session range: %w", err)
	}
	if rawSeq <= 0 {
		return EventRow{}, fmt.Errorf("store: invalid session range sequence %d", rawSeq)
	}
	row.Seq = uint64(rawSeq)
	parsed, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return EventRow{}, fmt.Errorf(
			"store: parse session range timestamp at seq %d: %w",
			row.Seq,
			err,
		)
	}
	row.Timestamp = parsed
	switch attachmentPresent {
	case 0:
	case 1:
		row.OutputAttachmentPresent = true
	default:
		return EventRow{}, fmt.Errorf(
			"store: invalid attachment presence %d at seq %d",
			attachmentPresent,
			row.Seq,
		)
	}
	return row, nil
}

func (l ReadLimit) validate() error {
	if l.Rows <= 0 || l.Rows > MaxRangeRows {
		return fmt.Errorf(
			"store: range row limit %d is outside 1..%d",
			l.Rows,
			MaxRangeRows,
		)
	}
	if l.AttachmentBytes < event.MaxOutputChunkBytes ||
		l.AttachmentBytes > MaxRangeAttachmentBytes {
		return fmt.Errorf(
			"store: range attachment limit %d is outside %d..%d",
			l.AttachmentBytes,
			event.MaxOutputChunkBytes,
			MaxRangeAttachmentBytes,
		)
	}
	return nil
}

func addOutputOffset(offset uint64, length int) (uint64, error) {
	if length < 0 || uint64(length) > math.MaxUint64-offset {
		return 0, fmt.Errorf("output offset %d plus length %d overflows", offset, length)
	}
	return offset + uint64(length), nil
}

func (s *Store) readDB() *sql.DB {
	if s.readers != nil {
		return s.readers
	}
	return s.db
}
