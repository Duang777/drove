package term

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

func TestOSC9SanitizerPreservesFramingAndAllowlistedPrefix(t *testing.T) {
	const prefix = "Approval requested: "
	tests := []struct {
		name  string
		input []byte
	}{
		{
			name:  "direct bell",
			input: []byte("before\x1b]9;" + prefix + "private-command\x07after"),
		},
		{
			name:  "direct string terminator",
			input: []byte("before\x1b]9;" + prefix + "private-command\x1b\\after"),
		},
		{
			name: "tmux",
			input: []byte(
				"before\x1bPtmux;\x1b\x1b]9;" +
					prefix +
					"private-command\x07\x1b\\after",
			),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sanitizer, err := NewOSC9Sanitizer(prefix)
			if err != nil {
				t.Fatalf("new sanitizer: %v", err)
			}
			got := append(sanitizer.Feed(test.input), sanitizer.Flush()...)
			want := bytes.Replace(
				test.input,
				[]byte("private-command"),
				bytes.Repeat([]byte{'*'}, len("private-command")),
				1,
			)
			if !bytes.Equal(got, want) {
				t.Fatalf("sanitized output = %q, want %q", got, want)
			}
			if len(got) != len(test.input) {
				t.Fatalf("sanitized length = %d, want %d", len(got), len(test.input))
			}

			chunk, err := NewCommittedChunk(
				got,
				uint64(len(got)),
				1,
				time.Date(2026, time.October, 5, 11, 0, 0, 0, time.UTC),
			)
			if err != nil {
				t.Fatalf("new committed chunk: %v", err)
			}
			frames := NewOSC9Scanner().Feed(chunk)
			if len(frames) != 1 {
				t.Fatalf("sanitized frames = %d, want 1", len(frames))
			}
			if body := string(frames[0].Payload()); body !=
				prefix+string(bytes.Repeat([]byte{'*'}, len("private-command"))) {
				t.Fatalf("sanitized body = %q", body)
			}
		})
	}
}

func TestOSC9SanitizerMasksUnknownAndIncompleteBodies(t *testing.T) {
	const prefix = "Approval requested: "
	tests := []struct {
		name  string
		input []byte
		want  []byte
	}{
		{
			name:  "unknown direct",
			input: []byte("plain\x1b]9;Agent turn complete: private\x07tail"),
			want: []byte(
				"plain\x1b]9;" +
					"****************************" +
					"\x07tail",
			),
		},
		{
			name:  "unknown tmux",
			input: []byte("\x1bPtmux;\x1b\x1b]9;title private\x07\x1b\\"),
			want:  []byte("\x1bPtmux;\x1b\x1b]9;*************\x07\x1b\\"),
		},
		{
			name:  "incomplete recognized prefix",
			input: []byte("plain\x1b]9;Approval requ"),
			want:  []byte("plain\x1b]9;*************"),
		},
		{
			name:  "incomplete recognized body",
			input: []byte("plain\x1b]9;" + prefix + "private"),
			want:  []byte("plain\x1b]9;" + prefix + "*******"),
		},
		{
			name:  "foreign control strings unchanged",
			input: []byte("plain\x1b]0;private title\x07\x1bPprivate\x1b\\tail"),
			want:  []byte("plain\x1b]0;private title\x07\x1bPprivate\x1b\\tail"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sanitizer, err := NewOSC9Sanitizer(prefix)
			if err != nil {
				t.Fatalf("new sanitizer: %v", err)
			}
			got := append(sanitizer.Feed(test.input), sanitizer.Flush()...)
			if !bytes.Equal(got, test.want) {
				t.Fatalf("sanitized output = %q, want %q", got, test.want)
			}
			if len(got) != len(test.input) {
				t.Fatalf("sanitized length = %d, want %d", len(got), len(test.input))
			}
		})
	}
}

func TestOSC9SanitizerHandlesEveryBoundary(t *testing.T) {
	const prefix = "Approval requested by "
	input := []byte(
		"before\x1bPtmux;\x1b\x1b]9;" + prefix + "private-server\x07\x1b\\after",
	)
	want := bytes.Replace(
		input,
		[]byte("private-server"),
		bytes.Repeat([]byte{'*'}, len("private-server")),
		1,
	)
	for split := 0; split <= len(input); split++ {
		t.Run(fmt.Sprintf("split-%d", split), func(t *testing.T) {
			sanitizer, err := NewOSC9Sanitizer(prefix)
			if err != nil {
				t.Fatalf("new sanitizer: %v", err)
			}
			got := append([]byte(nil), sanitizer.Feed(input[:split])...)
			got = append(got, sanitizer.Feed(input[split:])...)
			got = append(got, sanitizer.Flush()...)
			if !bytes.Equal(got, want) {
				t.Fatalf("sanitized output = %q, want %q", got, want)
			}
		})
	}
}

func TestNewOSC9SanitizerRejectsUnsafePrefixes(t *testing.T) {
	tests := []string{"", "contains\x07bell", "contains\x1bescape"}
	for _, prefix := range tests {
		if _, err := NewOSC9Sanitizer(prefix); err == nil {
			t.Fatalf("prefix %q was accepted", prefix)
		}
	}
}
