package recording

import (
	"context"
	"errors"
	"fmt"

	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
	"github.com/Duang777/drove/internal/term"
)

const (
	// FrameFidelityExactOriginReplay means the frame was rebuilt from byte zero.
	FrameFidelityExactOriginReplay = "exact_origin_replay"
	maxRetentionReadAttempts       = 3
	initialFrameRows               = 40
	initialFrameColumns            = 120
)

var (
	// ErrInvalidFrameSelector reports a missing, mixed, or unsupported selector.
	ErrInvalidFrameSelector = errors.New("recording: invalid frame selector")
	// ErrRetentionChanged reports repeated output pruning during frame replay.
	ErrRetentionChanged = errors.New("recording: output retention changed during replay")
)

// Frame is an exact bounded visible terminal view reconstructed from origin.
type Frame struct {
	SessionID  string   `json:"session_id"`
	Cursor     Cursor   `json:"cursor"`
	Rows       uint16   `json:"rows"`
	Columns    uint16   `json:"columns"`
	Lines      []string `json:"lines"`
	Truncated  bool     `json:"truncated"`
	Fidelity   string   `json:"fidelity"`
	Restorable bool     `json:"restorable"`
}

type frameTarget struct {
	cursor        Cursor
	replayThrough uint64
	partialOutput uint64
}

type frameViewOptions struct {
	bottomRows     int
	maxCellsPerRow int
	maxBytes       int
}

func defaultFrameViewOptions() frameViewOptions {
	return frameViewOptions{
		bottomRows:     term.MaxViewRows,
		maxCellsPerRow: term.MaxViewCellsPerRow,
		maxBytes:       term.MaxViewBytes,
	}
}

// Frame resolves one sequence, time, or output-offset selector and replays it.
func (a *Archive) Frame(
	ctx context.Context,
	sessionID string,
	selector *Selector,
) (Frame, error) {
	return a.frame(ctx, sessionID, selector, defaultFrameViewOptions())
}

func (a *Archive) frame(
	ctx context.Context,
	sessionID string,
	selector *Selector,
	options frameViewOptions,
) (Frame, error) {
	if a == nil || a.store == nil {
		return Frame{}, errors.New("recording: archive is not configured")
	}
	if selector == nil {
		return Frame{}, fmt.Errorf("%w: exactly one selector is required", ErrInvalidFrameSelector)
	}
	if selector.Kind() != SelectorSequence &&
		selector.Kind() != SelectorTime &&
		selector.Kind() != SelectorOutputOffset {
		return Frame{}, fmt.Errorf(
			"%w: selector kind %d is unsupported",
			ErrInvalidFrameSelector,
			selector.Kind(),
		)
	}
	viewOptions, err := term.NewViewOptions(
		options.bottomRows,
		options.maxCellsPerRow,
		options.maxBytes,
	)
	if err != nil {
		return Frame{}, fmt.Errorf("%w: %v", ErrInvalidFrameSelector, err)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	boundary, found, err := a.store.SessionBoundary(ctx, sessionID, nil)
	if err != nil {
		return Frame{}, fmt.Errorf("recording: capture frame boundary: %w", err)
	}
	if !found {
		return Frame{}, fmt.Errorf("%w: %q", ErrUnknownSession, sessionID)
	}
	target, err := a.resolveFrameTarget(ctx, sessionID, boundary, *selector)
	if err != nil {
		return Frame{}, err
	}

	for attempt := 0; attempt < maxRetentionReadAttempts; attempt++ {
		generation := a.store.OutputRetentionGeneration()
		key := frameCacheKey{
			sessionID:           sessionID,
			sequence:            target.cursor.Seq,
			outputOffset:        target.cursor.NextOffset,
			bottomRows:          options.bottomRows,
			maxCellsPerRow:      options.maxCellsPerRow,
			maxBytes:            options.maxBytes,
			retentionGeneration: generation,
		}
		if cached, ok := a.frames.Get(key); ok {
			if a.store.OutputRetentionGeneration() == generation {
				return cached, nil
			}
			continue
		}

		frame, renderErr := a.renderFrame(
			ctx,
			sessionID,
			target,
			viewOptions,
		)
		if a.store.OutputRetentionGeneration() != generation {
			continue
		}
		if renderErr != nil {
			return Frame{}, renderErr
		}
		a.frames.Add(key, frame)
		if a.store.OutputRetentionGeneration() != generation {
			continue
		}
		return frame, nil
	}
	return Frame{}, ErrRetentionChanged
}

func (a *Archive) resolveFrameTarget(
	ctx context.Context,
	sessionID string,
	boundary store.SessionBoundary,
	selector Selector,
) (frameTarget, error) {
	cursor, partialOutput, err := a.resolveStart(
		ctx,
		sessionID,
		boundary,
		&selector,
	)
	if err != nil {
		return frameTarget{}, err
	}
	target := frameTarget{
		cursor:        cursor,
		replayThrough: uint64(cursor.Seq),
		partialOutput: partialOutput,
	}
	if partialOutput != 0 {
		target.cursor.Seq = Seq(partialOutput)
		target.replayThrough = partialOutput
	}
	return target, nil
}

func (a *Archive) renderFrame(
	ctx context.Context,
	sessionID string,
	target frameTarget,
	viewOptions term.ViewOptions,
) (Frame, error) {
	size, err := term.NewSize(initialFrameRows, initialFrameColumns)
	if err != nil {
		return Frame{}, fmt.Errorf("recording: create frame origin: %w", err)
	}
	controller, err := term.NewController(size, func([]byte) error { return nil })
	if err != nil {
		return Frame{}, fmt.Errorf("recording: create frame terminal: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = controller.Close()
		}
	}()

	cursor := Origin
	missing := make([]OutputRange, 0)
	after := uint64(0)
	for after < target.replayThrough {
		page, readErr := a.store.ReadSessionRange(
			ctx,
			sessionID,
			after,
			target.replayThrough,
			a.limit,
			true,
		)
		if readErr != nil {
			return Frame{}, fmt.Errorf("recording: read frame events: %w", readErr)
		}
		for _, row := range page.Rows {
			switch event.Type(row.Type) {
			case event.TypeOutputChunk:
				payload, decodeErr := event.DecodeOutputChunkPayload(row.Payload)
				if decodeErr != nil {
					return Frame{}, fmt.Errorf(
						"recording: decode frame output at seq %d: %w",
						row.Seq,
						decodeErr,
					)
				}
				if payload.Offset != uint64(cursor.NextOffset) {
					return Frame{}, fmt.Errorf(
						"recording: non-contiguous frame output at seq %d: offset %d, want %s",
						row.Seq,
						payload.Offset,
						cursor.NextOffset,
					)
				}
				end, endErr := checkedOutputEnd(payload.Offset, payload.Len)
				if endErr != nil {
					return Frame{}, fmt.Errorf(
						"recording: frame output at seq %d: %w",
						row.Seq,
						endErr,
					)
				}
				requiredEnd := end
				if row.Seq == target.partialOutput {
					requiredEnd = uint64(target.cursor.NextOffset)
					if requiredEnd <= payload.Offset || requiredEnd >= end {
						return Frame{}, fmt.Errorf(
							"recording: invalid partial frame offset %d in [%d,%d)",
							requiredEnd,
							payload.Offset,
							end,
						)
					}
				}
				requiredBytes := requiredEnd - payload.Offset
				if !row.OutputAttachmentPresent {
					missing = appendOutputRange(missing, OutputRange{
						Start: OutputOffset(payload.Offset),
						End:   OutputOffset(requiredEnd),
					})
				} else {
					if uint64(len(row.OutputAttachment)) < requiredBytes {
						return Frame{}, fmt.Errorf(
							"recording: output attachment at seq %d has length %d, need %d",
							row.Seq,
							len(row.OutputAttachment),
							requiredBytes,
						)
					}
					if writeErr := controller.Write(
						row.OutputAttachment[:requiredBytes],
					); writeErr != nil {
						return Frame{}, fmt.Errorf(
							"recording: apply frame output at seq %d: %w",
							row.Seq,
							writeErr,
						)
					}
				}
				cursor.NextOffset = OutputOffset(requiredEnd)
			case event.TypeAgentResized:
				payload, decodeErr := event.DecodeAgentResizedPayload(row.Payload)
				if decodeErr != nil {
					return Frame{}, fmt.Errorf(
						"recording: decode frame resize at seq %d: %w",
						row.Seq,
						decodeErr,
					)
				}
				if payload.OutputOffset != uint64(cursor.NextOffset) {
					return Frame{}, fmt.Errorf(
						"recording: frame resize at seq %d has output offset %d, want %s",
						row.Seq,
						payload.OutputOffset,
						cursor.NextOffset,
					)
				}
				resized, sizeErr := term.NewSize(
					int(payload.Rows),
					int(payload.Columns),
				)
				if sizeErr != nil {
					return Frame{}, fmt.Errorf(
						"recording: validate frame resize at seq %d: %w",
						row.Seq,
						sizeErr,
					)
				}
				if resizeErr := controller.Resize(resized); resizeErr != nil {
					return Frame{}, fmt.Errorf(
						"recording: apply frame resize at seq %d: %w",
						row.Seq,
						resizeErr,
					)
				}
			}
			cursor.Seq = Seq(row.Seq)
		}
		after = page.ScannedThrough
		if !page.More {
			break
		}
	}
	if cursor != target.cursor {
		return Frame{}, fmt.Errorf(
			"recording: rendered frame cursor {%s,%s}, want {%s,%s}",
			cursor.Seq,
			cursor.NextOffset,
			target.cursor.Seq,
			target.cursor.NextOffset,
		)
	}
	if len(missing) != 0 {
		return Frame{}, &OutputExpiredError{
			SessionID: sessionID,
			Missing:   missing,
		}
	}

	snapshot, err := controller.Snapshot()
	if err != nil {
		return Frame{}, fmt.Errorf("recording: snapshot replayed frame: %w", err)
	}
	view, err := snapshot.View(viewOptions)
	if err != nil {
		return Frame{}, fmt.Errorf("recording: bound replayed frame: %w", err)
	}
	frame := Frame{
		SessionID:  sessionID,
		Cursor:     target.cursor,
		Rows:       uint16(snapshot.Size().Rows()),
		Columns:    uint16(snapshot.Size().Columns()),
		Lines:      append([]string{}, view.Rows()...),
		Truncated:  view.Truncated(),
		Fidelity:   FrameFidelityExactOriginReplay,
		Restorable: false,
	}
	if err := controller.Close(); err != nil {
		return Frame{}, fmt.Errorf("recording: close frame terminal: %w", err)
	}
	closed = true
	return frame, nil
}
