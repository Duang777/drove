package term

import (
	"bytes"
	"testing"
)

func TestStripTerminalText(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "SGR colors",
			input: "\x1b[1;31mError:\x1b[0m failed",
			want:  "Error: failed",
		},
		{
			name:  "CSI cursor and erase",
			input: "Waiting \x1b[2K\x1b[1Gfor your input",
			want:  "Waiting for your input",
		},
		{
			name:  "OSC terminated by bell",
			input: "before\x1b]0;window title\x07after",
			want:  "beforeafter",
		},
		{
			name:  "OSC terminated by string terminator",
			input: "before\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\after",
			want:  "beforelinkafter",
		},
		{
			name:  "DCS string",
			input: "before\x1bP1;2;3+qpayload\x1b\\after",
			want:  "beforeafter",
		},
		{
			name:  "SOS string",
			input: "before\x1bXpayload\x1b\\after",
			want:  "beforeafter",
		},
		{
			name:  "PM string",
			input: "before\x1b^payload\x1b\\after",
			want:  "beforeafter",
		},
		{
			name:  "APC string",
			input: "before\x1b_payload\x1b\\after",
			want:  "beforeafter",
		},
		{
			name:  "single-character escapes",
			input: "\x1b7Waiting for your input\x1b8",
			want:  "Waiting for your input",
		},
		{
			name:  "character set designation",
			input: "before\x1b(Bafter",
			want:  "beforeafter",
		},
		{
			name:  "incomplete sequence",
			input: "before\x1b[31",
			want:  "before",
		},
		{
			name:  "plain Unicode including continuation bytes",
			input: "请选择一个选项 🎛️",
			want:  "请选择一个选项 🎛️",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := StripString(test.input); got != test.want {
				t.Fatalf("stripped text = %q, want %q", got, test.want)
			}
		})
	}
}

func TestStripperCarriesSequencesAcrossEveryFeedBoundary(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  []byte
	}{
		{
			name:  "CSI",
			input: []byte("before\x1b[38;5;123mafter"),
			want:  []byte("beforeafter"),
		},
		{
			name:  "OSC bell",
			input: []byte("before\x1b]0;title\x07after"),
			want:  []byte("beforeafter"),
		},
		{
			name:  "OSC string terminator",
			input: []byte("before\x1b]0;title\x1b\\after"),
			want:  []byte("beforeafter"),
		},
		{
			name:  "DCS",
			input: []byte("before\x1bPpayload\x1b\\after"),
			want:  []byte("beforeafter"),
		},
		{
			name:  "escape intermediate",
			input: []byte("before\x1b(Bafter"),
			want:  []byte("beforeafter"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for split := 0; split <= len(test.input); split++ {
				var stripper Stripper
				got := stripper.Feed(test.input[:split])
				got = append(got, stripper.Feed(test.input[split:])...)
				got = append(got, stripper.Flush()...)
				if !bytes.Equal(got, test.want) {
					t.Fatalf(
						"split %d output = %q, want %q",
						split,
						got,
						test.want,
					)
				}
			}
		})
	}
}

func TestStripperPreservesInvalidNonControlBytes(t *testing.T) {
	input := []byte{'a', 0xff, 0xc0, 0x80, 'b'}
	var stripper Stripper
	got := stripper.Feed(input[:2])
	got = append(got, stripper.Feed(input[2:])...)
	got = append(got, stripper.Flush()...)
	if !bytes.Equal(got, input) {
		t.Fatalf("output = %x, want %x", got, input)
	}
}

func TestStripperFlushDiscardsIncompleteSequenceAndResets(t *testing.T) {
	var stripper Stripper
	if got := stripper.Feed([]byte("before\x1b[31")); string(got) != "before" {
		t.Fatalf("first output = %q, want before", got)
	}
	if got := stripper.Flush(); len(got) != 0 {
		t.Fatalf("flush output = %q, want empty", got)
	}
	if got := stripper.Feed([]byte("after")); string(got) != "after" {
		t.Fatalf("output after reset = %q, want after", got)
	}
}
