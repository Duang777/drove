// Package term provides bounded terminal byte-stream helpers.
package term

const (
	escapeByte = 0x1b
	bellByte   = 0x07
)

type parserState uint8

const (
	stateText parserState = iota
	stateEscape
	stateEscapeIntermediate
	stateCSI
	stateOSC
	stateOSCEscape
	stateControlString
	stateControlStringEscape
)

// Stripper removes terminal control sequences while retaining parser state.
type Stripper struct {
	state parserState
}

// Feed removes complete and partial terminal control sequences from input.
func (s *Stripper) Feed(input []byte) []byte {
	output := make([]byte, 0, len(input))
	for _, value := range input {
		switch s.state {
		case stateText:
			if value == escapeByte {
				s.state = stateEscape
			} else {
				output = append(output, value)
			}
		case stateEscape:
			switch {
			case value == escapeByte:
				s.state = stateEscape
			case value == '[':
				s.state = stateCSI
			case value == ']':
				s.state = stateOSC
			case value == 'P' || value == 'X' || value == '^' || value == '_':
				s.state = stateControlString
			case value >= 0x20 && value <= 0x2f:
				s.state = stateEscapeIntermediate
			case value >= 0x30 && value <= 0x7e:
				s.state = stateText
			default:
				s.state = stateText
				output = append(output, value)
			}
		case stateEscapeIntermediate:
			switch {
			case value == escapeByte:
				s.state = stateEscape
			case value >= 0x20 && value <= 0x2f:
			case value >= 0x30 && value <= 0x7e:
				s.state = stateText
			default:
				s.state = stateText
				output = append(output, value)
			}
		case stateCSI:
			switch {
			case value == escapeByte:
				s.state = stateEscape
			case value >= 0x40 && value <= 0x7e:
				s.state = stateText
			}
		case stateOSC:
			switch value {
			case bellByte:
				s.state = stateText
			case escapeByte:
				s.state = stateOSCEscape
			}
		case stateOSCEscape:
			switch value {
			case '\\':
				s.state = stateText
			case escapeByte:
				s.state = stateOSCEscape
			default:
				s.state = stateOSC
			}
		case stateControlString:
			if value == escapeByte {
				s.state = stateControlStringEscape
			}
		case stateControlStringEscape:
			switch value {
			case '\\':
				s.state = stateText
			case escapeByte:
				s.state = stateControlStringEscape
			default:
				s.state = stateControlString
			}
		}
	}
	return output
}

// Flush discards an incomplete terminal control sequence and resets the parser.
func (s *Stripper) Flush() []byte {
	s.state = stateText
	return nil
}

// Strip removes terminal control sequences from one complete byte slice.
func Strip(input []byte) []byte {
	var stripper Stripper
	output := stripper.Feed(input)
	return append(output, stripper.Flush()...)
}

// StripString removes terminal control sequences from one complete string.
func StripString(input string) string {
	return string(Strip([]byte(input)))
}
