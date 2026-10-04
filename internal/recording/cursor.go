// Package recording reads durable terminal recordings and derives replay views.
package recording

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Seq is a durable global event sequence. Zero denotes the recording origin.
type Seq uint64

// ParseSeq parses a canonical unsigned decimal event sequence.
func ParseSeq(value string) (Seq, error) {
	parsed, err := parseDecimal("sequence", value)
	return Seq(parsed), err
}

// String formats the sequence as canonical unsigned decimal.
func (s Seq) String() string {
	return strconv.FormatUint(uint64(s), 10)
}

// MarshalJSON encodes the sequence as a JSON string for browser precision.
func (s Seq) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

// UnmarshalJSON accepts only a canonical unsigned decimal JSON string.
func (s *Seq) UnmarshalJSON(data []byte) error {
	if s == nil {
		return errors.New("recording: cannot decode sequence into nil receiver")
	}
	value, err := decodeDecimalString("sequence", data)
	if err != nil {
		return err
	}
	parsed, err := ParseSeq(value)
	if err != nil {
		return err
	}
	*s = parsed
	return nil
}

// OutputOffset is the first persisted output byte not yet consumed.
type OutputOffset uint64

// ParseOutputOffset parses a canonical unsigned decimal output offset.
func ParseOutputOffset(value string) (OutputOffset, error) {
	parsed, err := parseDecimal("output offset", value)
	return OutputOffset(parsed), err
}

// String formats the output offset as canonical unsigned decimal.
func (o OutputOffset) String() string {
	return strconv.FormatUint(uint64(o), 10)
}

// MarshalJSON encodes the output offset as a JSON string for browser precision.
func (o OutputOffset) MarshalJSON() ([]byte, error) {
	return json.Marshal(o.String())
}

// UnmarshalJSON accepts only a canonical unsigned decimal JSON string.
func (o *OutputOffset) UnmarshalJSON(data []byte) error {
	if o == nil {
		return errors.New("recording: cannot decode output offset into nil receiver")
	}
	value, err := decodeDecimalString("output offset", data)
	if err != nil {
		return err
	}
	parsed, err := ParseOutputOffset(value)
	if err != nil {
		return err
	}
	*o = parsed
	return nil
}

// Cursor identifies the last consumed session event and next output byte.
type Cursor struct {
	Seq        Seq          `json:"seq"`
	NextOffset OutputOffset `json:"next_offset"`
}

// UnmarshalJSON decodes and validates the complete cursor shape.
func (c *Cursor) UnmarshalJSON(data []byte) error {
	if c == nil {
		return errors.New("recording: cannot decode cursor into nil receiver")
	}
	type cursorJSON Cursor
	var decoded cursorJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("recording: decode cursor: %w", err)
	}
	cursor := Cursor(decoded)
	if err := cursor.Validate(); err != nil {
		return err
	}
	*c = cursor
	return nil
}

// Origin is the position before the first event and output byte.
var Origin = Cursor{}

// NewCursor validates and constructs a canonical cursor shape.
func NewCursor(seq Seq, nextOffset OutputOffset) (Cursor, error) {
	cursor := Cursor{Seq: seq, NextOffset: nextOffset}
	if err := cursor.Validate(); err != nil {
		return Cursor{}, err
	}
	return cursor, nil
}

// ParseCursor parses canonical decimal cursor fields.
func ParseCursor(seq, nextOffset string) (Cursor, error) {
	parsedSeq, err := ParseSeq(seq)
	if err != nil {
		return Cursor{}, err
	}
	parsedOffset, err := ParseOutputOffset(nextOffset)
	if err != nil {
		return Cursor{}, err
	}
	return NewCursor(parsedSeq, parsedOffset)
}

// Validate rejects cursor shapes that cannot identify a recording position.
// Agreement with a particular event log is checked by Archive resolution.
func (c Cursor) Validate() error {
	if c.Seq == 0 && c.NextOffset != 0 {
		return errors.New("recording: origin sequence requires zero next offset")
	}
	return nil
}

// SelectorKind identifies one mutually exclusive recording selector.
type SelectorKind uint8

const (
	// SelectorCursor resolves an exact full cursor.
	SelectorCursor SelectorKind = iota + 1
	// SelectorSequence resolves the session position at a global sequence.
	SelectorSequence
	// SelectorTime resolves the session position at a timestamp.
	SelectorTime
	// SelectorOutputOffset resolves an inclusive output byte offset.
	SelectorOutputOffset
)

// SelectorInput is the boundary form accepted from transport parsers.
// Exactly one field must be populated.
type SelectorInput struct {
	Cursor *Cursor
	Seq    *Seq
	At     *time.Time
	Offset *OutputOffset
}

// Selector is a validated, mutually exclusive recording selector.
type Selector struct {
	kind   SelectorKind
	cursor Cursor
	seq    Seq
	at     time.Time
	offset OutputOffset
}

// NewSelector rejects missing, mixed, and malformed selector forms.
func NewSelector(input SelectorInput) (Selector, error) {
	count := 0
	if input.Cursor != nil {
		count++
	}
	if input.Seq != nil {
		count++
	}
	if input.At != nil {
		count++
	}
	if input.Offset != nil {
		count++
	}
	if count != 1 {
		return Selector{}, errors.New("recording: selector requires exactly one value")
	}

	switch {
	case input.Cursor != nil:
		if err := input.Cursor.Validate(); err != nil {
			return Selector{}, err
		}
		return Selector{kind: SelectorCursor, cursor: *input.Cursor}, nil
	case input.Seq != nil:
		return Selector{kind: SelectorSequence, seq: *input.Seq}, nil
	case input.At != nil:
		if input.At.IsZero() {
			return Selector{}, errors.New("recording: selector timestamp is required")
		}
		return Selector{kind: SelectorTime, at: input.At.UTC()}, nil
	default:
		return Selector{kind: SelectorOutputOffset, offset: *input.Offset}, nil
	}
}

// Kind returns the selector variant.
func (s Selector) Kind() SelectorKind {
	return s.kind
}

// Cursor returns the exact cursor value when selected.
func (s Selector) Cursor() (Cursor, bool) {
	return s.cursor, s.kind == SelectorCursor
}

// Sequence returns the global sequence value when selected.
func (s Selector) Sequence() (Seq, bool) {
	return s.seq, s.kind == SelectorSequence
}

// Time returns the UTC timestamp value when selected.
func (s Selector) Time() (time.Time, bool) {
	return s.at, s.kind == SelectorTime
}

// OutputOffset returns the byte offset value when selected.
func (s Selector) OutputOffset() (OutputOffset, bool) {
	return s.offset, s.kind == SelectorOutputOffset
}

func parseDecimal(name, value string) (uint64, error) {
	if value == "" {
		return 0, fmt.Errorf("recording: %s is empty", name)
	}
	if value != "0" && value[0] == '0' {
		return 0, fmt.Errorf("recording: %s %q is not canonical decimal", name, value)
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("recording: %s %q is not unsigned decimal", name, value)
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("recording: parse %s %q: %w", name, value, err)
	}
	return parsed, nil
}

func decodeDecimalString(name string, data []byte) (string, error) {
	if len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' {
		return "", fmt.Errorf("recording: %s must be a JSON string", name)
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return "", fmt.Errorf("recording: decode %s: %w", name, err)
	}
	return value, nil
}
