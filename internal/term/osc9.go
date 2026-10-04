package term

import "time"

const (
	// MaxOSC9PayloadBytes bounds one in-progress OSC 9 notification body.
	MaxOSC9PayloadBytes = 4 * 1024
)

type osc9State uint8

const (
	osc9Text osc9State = iota
	osc9Escape
	osc9Selector
	osc9Delimiter
	osc9Ignored
	osc9IgnoredEscape
	osc9ControlString
	osc9ControlStringEscape
	osc9TmuxPrefix
	osc9Payload
	osc9PayloadEscape
	osc9TmuxPayload
	osc9TmuxPayloadEscape
	osc9TmuxPayloadDoubleEscape
)

var tmuxOSC9Prefix = [...]byte{
	't', 'm', 'u', 'x', ';',
	escapeByte, escapeByte, ']', '9', ';',
}

// OSC9Frame is one copied notification body with committed output provenance.
type OSC9Frame struct {
	payload       []byte
	outputOffset  uint64
	lastOutputSeq uint64
	committedAt   time.Time
}

// Payload returns a copy of the OSC 9 body.
func (f OSC9Frame) Payload() []byte {
	return append([]byte(nil), f.payload...)
}

// OutputOffset returns the exclusive committed offset at the frame terminator.
func (f OSC9Frame) OutputOffset() uint64 {
	return f.outputOffset
}

// LastOutputSeq returns the final committed sequence for the containing batch.
func (f OSC9Frame) LastOutputSeq() uint64 {
	return f.lastOutputSeq
}

// CommittedAt returns the commit time for the containing output batch.
func (f OSC9Frame) CommittedAt() time.Time {
	return f.committedAt
}

// OSC9Scanner extracts bounded OSC 9 frames from a committed terminal stream.
type OSC9Scanner struct {
	state      osc9State
	tmuxPrefix int
	payload    []byte
	overflow   bool
}

// NewOSC9Scanner constructs an empty OSC 9 scanner.
func NewOSC9Scanner() *OSC9Scanner {
	return &OSC9Scanner{}
}

// Feed consumes one committed terminal-output batch.
func (s *OSC9Scanner) Feed(chunk CommittedChunk) []OSC9Frame {
	data := chunk.Bytes()
	if len(data) == 0 || chunk.OutputOffset() < uint64(len(data)) {
		s.Reset()
		return nil
	}

	baseOffset := chunk.OutputOffset() - uint64(len(data))
	frames := make([]OSC9Frame, 0, 1)
	for index, value := range data {
		offset := baseOffset + uint64(index) + 1
		switch s.state {
		case osc9Text:
			if value == escapeByte {
				s.state = osc9Escape
			}

		case osc9Escape:
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
			switch value {
			case bellByte:
				s.state = osc9Text
			case escapeByte:
				s.state = osc9IgnoredEscape
			}

		case osc9IgnoredEscape:
			switch value {
			case '\\':
				s.state = osc9Text
			case escapeByte:
			default:
				s.state = osc9Ignored
			}

		case osc9ControlString:
			if value == escapeByte {
				s.state = osc9ControlStringEscape
			}

		case osc9ControlStringEscape:
			switch value {
			case '\\':
				s.state = osc9Text
			case escapeByte:
			default:
				s.state = osc9ControlString
			}

		case osc9TmuxPrefix:
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
				if frame, ok := s.finish(offset, chunk); ok {
					frames = append(frames, frame)
				}
				s.state = osc9Text
			case escapeByte:
				s.state = osc9PayloadEscape
			default:
				s.appendPayload(value)
			}

		case osc9PayloadEscape:
			switch value {
			case '\\':
				if frame, ok := s.finish(offset, chunk); ok {
					frames = append(frames, frame)
				}
				s.state = osc9Text
			case bellByte:
				s.appendPayload(escapeByte)
				if frame, ok := s.finish(offset, chunk); ok {
					frames = append(frames, frame)
				}
				s.state = osc9Text
			case escapeByte:
				s.appendPayload(escapeByte)
			default:
				s.appendPayload(escapeByte)
				s.appendPayload(value)
				s.state = osc9Payload
			}

		case osc9TmuxPayload:
			switch value {
			case bellByte:
				if frame, ok := s.finish(offset, chunk); ok {
					frames = append(frames, frame)
				}
				s.state = osc9ControlString
			case escapeByte:
				s.state = osc9TmuxPayloadEscape
			default:
				s.appendPayload(value)
			}

		case osc9TmuxPayloadEscape:
			switch value {
			case '\\':
				s.discardPayload()
				s.state = osc9Text
			case escapeByte:
				s.state = osc9TmuxPayloadDoubleEscape
			default:
				s.appendPayload(escapeByte)
				s.appendPayload(value)
				s.state = osc9TmuxPayload
			}

		case osc9TmuxPayloadDoubleEscape:
			switch value {
			case '\\':
				if frame, ok := s.finish(offset, chunk); ok {
					frames = append(frames, frame)
				}
				s.state = osc9ControlString
			case bellByte:
				s.appendPayload(escapeByte)
				if frame, ok := s.finish(offset, chunk); ok {
					frames = append(frames, frame)
				}
				s.state = osc9ControlString
			case escapeByte:
				s.appendPayload(escapeByte)
				s.state = osc9TmuxPayloadEscape
			default:
				s.appendPayload(escapeByte)
				s.appendPayload(value)
				s.state = osc9TmuxPayload
			}
		}
	}
	return frames
}

// Reset discards any incomplete frame and returns to text scanning.
func (s *OSC9Scanner) Reset() {
	s.state = osc9Text
	s.tmuxPrefix = 0
	s.discardPayload()
}

func (s *OSC9Scanner) beginPayload(state osc9State) {
	s.payload = s.payload[:0]
	s.overflow = false
	s.state = state
}

func (s *OSC9Scanner) appendPayload(value byte) {
	if s.overflow {
		return
	}
	if len(s.payload) == MaxOSC9PayloadBytes {
		s.overflow = true
		return
	}
	s.payload = append(s.payload, value)
}

func (s *OSC9Scanner) finish(
	outputOffset uint64,
	chunk CommittedChunk,
) (OSC9Frame, bool) {
	if s.overflow {
		s.discardPayload()
		return OSC9Frame{}, false
	}
	frame := OSC9Frame{
		payload:       append([]byte(nil), s.payload...),
		outputOffset:  outputOffset,
		lastOutputSeq: chunk.LastSeq(),
		committedAt:   chunk.CommittedAt(),
	}
	s.discardPayload()
	return frame, true
}

func (s *OSC9Scanner) discardPayload() {
	s.payload = s.payload[:0]
	s.overflow = false
}
