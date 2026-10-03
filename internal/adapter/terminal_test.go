package adapter

import (
	"testing"

	"github.com/Duang777/drove/internal/agent"
)

func TestSanitizeTerminalText(t *testing.T) {
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
			if got := sanitizeTerminalText(test.input); got != test.want {
				t.Fatalf("sanitized text = %q, want %q", got, test.want)
			}
		})
	}
}

func TestEntryClassifyUsesSanitizedTerminalText(t *testing.T) {
	entry := NewRegistry().For("claude")
	raw := "Waiting \x1b[2K\x1b[1Gfor your input"

	if _, ok := entry.Heuristic.Classify(raw); ok {
		t.Fatal("raw ANSI-decorated text unexpectedly matched")
	}
	hint, ok := entry.Classify(raw)
	if !ok || hint.State != agent.StateBlocked {
		t.Fatalf("sanitized hint = %+v, %t; want blocked", hint, ok)
	}
}

func TestGenericEntryClassifyHasNoHeuristic(t *testing.T) {
	if _, ok := NewRegistry().For("generic").Classify("\x1b[31mError\x1b[0m"); ok {
		t.Fatal("generic entry returned a heuristic hint")
	}
}
