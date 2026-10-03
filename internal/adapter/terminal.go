package adapter

import "strings"

const (
	escapeByte = 0x1b
	bellByte   = 0x07
)

func sanitizeTerminalText(input string) string {
	var output strings.Builder
	output.Grow(len(input))

	for index := 0; index < len(input); {
		switch input[index] {
		case escapeByte:
			index = consumeEscapeSequence(input, index)
		default:
			output.WriteByte(input[index])
			index++
		}
	}
	return output.String()
}

func consumeEscapeSequence(input string, index int) int {
	if index+1 >= len(input) {
		return len(input)
	}

	switch input[index+1] {
	case '[':
		return consumeCSI(input, index+2)
	case ']':
		return consumeControlString(input, index+2, true)
	case 'P', 'X', '^', '_':
		return consumeControlString(input, index+2, false)
	}

	next := input[index+1]
	if next < 0x20 || next > 0x7e {
		return index + 1
	}
	cursor := index + 1
	for cursor < len(input) && input[cursor] >= 0x20 && input[cursor] <= 0x2f {
		cursor++
	}
	if cursor < len(input) && input[cursor] >= 0x30 && input[cursor] <= 0x7e {
		return cursor + 1
	}
	return len(input)
}

func consumeCSI(input string, index int) int {
	for index < len(input) {
		if input[index] >= 0x40 && input[index] <= 0x7e {
			return index + 1
		}
		index++
	}
	return len(input)
}

func consumeControlString(input string, index int, bellTerminates bool) int {
	for index < len(input) {
		switch {
		case bellTerminates && input[index] == bellByte:
			return index + 1
		case input[index] == escapeByte &&
			index+1 < len(input) &&
			input[index+1] == '\\':
			return index + 2
		default:
			index++
		}
	}
	return len(input)
}
