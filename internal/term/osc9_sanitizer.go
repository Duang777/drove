package term

import (
	"bytes"
	"errors"
	"fmt"
)

const osc9MaskByte = '*'

// OSC9Sanitizer masks notification bodies while preserving stream framing.
type OSC9Sanitizer struct {
	state      osc9State
	tmuxPrefix int
	prefixes   [][]byte
	candidate  []byte
	masking    bool
}

// NewOSC9Sanitizer constructs a streaming sanitizer that preserves only the
// supplied notification-body prefixes.
func NewOSC9Sanitizer(prefixes ...string) (*OSC9Sanitizer, error) {
	if len(prefixes) == 0 {
		return nil, errors.New("term: OSC 9 sanitizer requires a safe prefix")
	}
	sanitizer := &OSC9Sanitizer{
		prefixes: make([][]byte, 0, len(prefixes)),
	}
	for _, prefix := range prefixes {
		data := []byte(prefix)
		if len(data) == 0 || len(data) > MaxOSC9PayloadBytes {
			return nil, fmt.Errorf(
				"term: invalid OSC 9 sanitizer prefix length %d",
				len(data),
			)
		}
		if bytes.IndexByte(data, bellByte) >= 0 ||
			bytes.IndexByte(data, escapeByte) >= 0 {
			return nil, errors.New(
				"term: OSC 9 sanitizer prefix contains framing bytes",
			)
		}
		sanitizer.prefixes = append(
			sanitizer.prefixes,
			append([]byte(nil), data...),
		)
	}
	return sanitizer, nil
}

// Feed consumes one terminal-stream fragment and returns bytes safe to retain.
func (s *OSC9Sanitizer) Feed(input []byte) []byte {
	output := make([]byte, 0, len(input))
	for _, value := range input {
		switch s.state {
		case osc9Text:
			output = append(output, value)
			if value == escapeByte {
				s.state = osc9Escape
			}

		case osc9Escape:
			output = append(output, value)
			switch value {
			case escapeByte:
			case ']':
				s.state = osc9Selector
			case 'P':
				s.state = osc9TmuxPrefix
				s.tmuxPrefix = 0
			case 'X', '^', '_':
				s.state = osc9ControlString
			default:
				s.state = osc9Text
			}

		case osc9Selector:
			output = append(output, value)
			switch value {
			case '9':
				s.state = osc9Delimiter
			case bellByte:
				s.state = osc9Text
			case escapeByte:
				s.state = osc9IgnoredEscape
			default:
				s.state = osc9Ignored
			}

		case osc9Delimiter:
			output = append(output, value)
			switch value {
			case ';':
				s.beginPayload(osc9Payload)
			case bellByte:
				s.state = osc9Text
			case escapeByte:
				s.state = osc9IgnoredEscape
			default:
				s.state = osc9Ignored
			}

		case osc9Ignored:
			output = append(output, value)
			switch value {
			case bellByte:
				s.state = osc9Text
			case escapeByte:
				s.state = osc9IgnoredEscape
			}

		case osc9IgnoredEscape:
			output = append(output, value)
			switch value {
			case '\\':
				s.state = osc9Text
			case escapeByte:
			default:
				s.state = osc9Ignored
			}

		case osc9ControlString:
			output = append(output, value)
			if value == escapeByte {
				s.state = osc9ControlStringEscape
			}

		case osc9ControlStringEscape:
			output = append(output, value)
			switch value {
			case '\\':
				s.state = osc9Text
			case escapeByte:
			default:
				s.state = osc9ControlString
			}

		case osc9TmuxPrefix:
			output = append(output, value)
			if value == tmuxOSC9Prefix[s.tmuxPrefix] {
				s.tmuxPrefix++
				if s.tmuxPrefix == len(tmuxOSC9Prefix) {
					s.beginPayload(osc9TmuxPayload)
				}
				continue
			}
			s.tmuxPrefix = 0
			if value == escapeByte {
				s.state = osc9ControlStringEscape
			} else {
				s.state = osc9ControlString
			}

		case osc9Payload:
			switch value {
			case bellByte:
				s.finishPayload(&output)
				output = append(output, value)
				s.state = osc9Text
			case escapeByte:
				s.state = osc9PayloadEscape
			default:
				s.sanitizePayloadByte(&output, value)
			}

		case osc9PayloadEscape:
			switch value {
			case '\\':
				s.finishPayload(&output)
				output = append(output, escapeByte, value)
				s.state = osc9Text
			case bellByte:
				s.sanitizePayloadByte(&output, escapeByte)
				s.finishPayload(&output)
				output = append(output, value)
				s.state = osc9Text
			case escapeByte:
				s.sanitizePayloadByte(&output, escapeByte)
			default:
				s.sanitizePayloadByte(&output, escapeByte)
				s.sanitizePayloadByte(&output, value)
				s.state = osc9Payload
			}

		case osc9TmuxPayload:
			switch value {
			case bellByte:
				s.finishPayload(&output)
				output = append(output, value)
				s.state = osc9ControlString
			case escapeByte:
				s.state = osc9TmuxPayloadEscape
			default:
				s.sanitizePayloadByte(&output, value)
			}

		case osc9TmuxPayloadEscape:
			switch value {
			case '\\':
				s.finishPayload(&output)
				output = append(output, escapeByte, value)
				s.state = osc9Text
			case escapeByte:
				s.state = osc9TmuxPayloadDoubleEscape
			default:
				s.sanitizePayloadByte(&output, escapeByte)
				s.sanitizePayloadByte(&output, value)
				s.state = osc9TmuxPayload
			}

		case osc9TmuxPayloadDoubleEscape:
			switch value {
			case '\\':
				s.finishPayload(&output)
				output = append(output, escapeByte, escapeByte, value)
				s.state = osc9ControlString
			case bellByte:
				output = append(output, escapeByte)
				s.sanitizePayloadByte(&output, escapeByte)
				s.finishPayload(&output)
				output = append(output, value)
				s.state = osc9ControlString
			case escapeByte:
				s.sanitizePayloadByte(&output, escapeByte)
				s.sanitizePayloadByte(&output, escapeByte)
				s.state = osc9TmuxPayloadEscape
			default:
				s.sanitizePayloadByte(&output, escapeByte)
				s.sanitizePayloadByte(&output, escapeByte)
				s.sanitizePayloadByte(&output, value)
				s.state = osc9TmuxPayload
			}
		}
	}
	return output
}

// Flush masks an incomplete OSC 9 body and resets the sanitizer.
func (s *OSC9Sanitizer) Flush() []byte {
	output := make([]byte, 0, len(s.candidate)+2)
	switch s.state {
	case osc9Payload, osc9TmuxPayload:
		s.finishPayload(&output)
	case osc9PayloadEscape, osc9TmuxPayloadEscape:
		s.sanitizePayloadByte(&output, escapeByte)
		s.finishPayload(&output)
	case osc9TmuxPayloadDoubleEscape:
		s.sanitizePayloadByte(&output, escapeByte)
		s.sanitizePayloadByte(&output, escapeByte)
		s.finishPayload(&output)
	}
	s.state = osc9Text
	s.tmuxPrefix = 0
	s.candidate = s.candidate[:0]
	s.masking = false
	return output
}

func (s *OSC9Sanitizer) beginPayload(state osc9State) {
	s.state = state
	s.candidate = s.candidate[:0]
	s.masking = false
}

func (s *OSC9Sanitizer) sanitizePayloadByte(output *[]byte, value byte) {
	if s.masking {
		*output = append(*output, osc9MaskByte)
		return
	}
	s.candidate = append(s.candidate, value)
	possible := false
	for _, prefix := range s.prefixes {
		if bytes.HasPrefix(prefix, s.candidate) {
			possible = true
			if len(prefix) == len(s.candidate) {
				*output = append(*output, s.candidate...)
				s.candidate = s.candidate[:0]
				s.masking = true
				return
			}
		}
	}
	if possible {
		return
	}
	*output = appendMask(*output, len(s.candidate))
	s.candidate = s.candidate[:0]
	s.masking = true
}

func (s *OSC9Sanitizer) finishPayload(output *[]byte) {
	*output = appendMask(*output, len(s.candidate))
	s.candidate = s.candidate[:0]
	s.masking = false
}

func appendMask(output []byte, count int) []byte {
	for range count {
		output = append(output, osc9MaskByte)
	}
	return output
}
