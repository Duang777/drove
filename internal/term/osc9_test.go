package term

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOSC9ScannerAcceptsDirectTerminators(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		body  string
	}{
		{
			name: "bell",
			input: loadOSC9Fixture(
				t,
				"testdata/codex-0.160.0-osc9-direct.hex",
			),
			body: "Approval requested: redacted-command",
		},
		{
			name:  "string terminator",
			input: []byte("\x1b]9;Approval requested: command\x1b\\"),
			body:  "Approval requested: command",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			at := time.Date(2026, time.October, 5, 1, 0, 0, 0, time.UTC)
			frames := feedOSC9Chunks(t, NewOSC9Scanner(), test.input, nil, at)
			if len(frames) != 1 {
				t.Fatalf("frames = %d, want 1", len(frames))
			}
			assertOSC9Frame(
				t,
				frames[0],
				test.body,
				uint64(len(test.input)),
				1,
				at,
			)
		})
	}
}

func TestOSC9ScannerAcceptsCodexTmuxPassthrough(t *testing.T) {
	input := loadOSC9Fixture(
		t,
		"testdata/codex-0.160.0-osc9-tmux.hex",
	)
	at := time.Date(2026, time.October, 5, 2, 0, 0, 0, time.UTC)
	frames := feedOSC9Chunks(t, NewOSC9Scanner(), input, nil, at)
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(frames))
	}
	wantOffset := uint64(bytes.IndexByte(input, bellByte) + 1)
	assertOSC9Frame(
		t,
		frames[0],
		"Codex wants to edit redacted/path",
		wantOffset,
		1,
		at,
	)
}

func TestOSC9ScannerCarriesFramesAcrossEveryBoundary(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
	}{
		{
			name:  "direct bell",
			input: []byte("before\x1b]9;Approval requested: command\x07after"),
		},
		{
			name:  "direct string terminator",
			input: []byte("before\x1b]9;Approval requested by server\x1b\\after"),
		},
		{
			name: "tmux",
			input: []byte(
				"before\x1bPtmux;\x1b\x1b]9;Codex wants to edit file\x07\x1b\\after",
			),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for split := 0; split <= len(test.input); split++ {
				t.Run(fmt.Sprintf("split-%d", split), func(t *testing.T) {
					at := time.Date(2026, time.October, 5, 3, 0, 0, 0, time.UTC)
					frames := feedOSC9Chunks(
						t,
						NewOSC9Scanner(),
						test.input,
						[]int{split},
						at,
					)
					if len(frames) != 1 {
						t.Fatalf("frames = %d, want 1", len(frames))
					}
				})
			}
		})
	}
}

func TestOSC9ScannerAcceptsByteAtATimeAndMultipleFrames(t *testing.T) {
	input := []byte(
		"\x1b]9;Approval requested: one\x07" +
			"plain" +
			"\x1b]9;Approval requested by two\x1b\\",
	)
	splits := make([]int, 0, len(input)-1)
	for index := 1; index < len(input); index++ {
		splits = append(splits, index)
	}
	frames := feedOSC9Chunks(
		t,
		NewOSC9Scanner(),
		input,
		splits,
		time.Date(2026, time.October, 5, 4, 0, 0, 0, time.UTC),
	)
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want 2", len(frames))
	}
	if got := string(frames[0].Payload()); got != "Approval requested: one" {
		t.Fatalf("first payload = %q", got)
	}
	if got := string(frames[1].Payload()); got != "Approval requested by two" {
		t.Fatalf("second payload = %q", got)
	}
}

func TestOSC9ScannerIgnoresForeignAndMalformedControlStrings(t *testing.T) {
	input := []byte(
		"\x1b]0;Approval requested: title\x07" +
			"\x1bPother;\x1b]9;Approval requested: hidden\x07\x1b\\" +
			"\x1bPtmux;broken\x1b\\" +
			"\x1b]9broken\x07",
	)
	frames := feedOSC9Chunks(
		t,
		NewOSC9Scanner(),
		input,
		nil,
		time.Date(2026, time.October, 5, 5, 0, 0, 0, time.UTC),
	)
	if len(frames) != 0 {
		t.Fatalf("frames = %d, want 0", len(frames))
	}
}

func TestOSC9ScannerRecoversAfterOversizedFrame(t *testing.T) {
	oversized := bytes.Repeat([]byte("x"), MaxOSC9PayloadBytes+1)
	input := append([]byte("\x1b]9;"), oversized...)
	input = append(input, bellByte)
	input = append(input, []byte("\x1b]9;Approval requested: recovered\x07")...)

	frames := feedOSC9Chunks(
		t,
		NewOSC9Scanner(),
		input,
		nil,
		time.Date(2026, time.October, 5, 6, 0, 0, 0, time.UTC),
	)
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(frames))
	}
	if got := string(frames[0].Payload()); got != "Approval requested: recovered" {
		t.Fatalf("payload = %q", got)
	}
}

func TestOSC9ScannerResetDiscardsIncompleteFrame(t *testing.T) {
	scanner := NewOSC9Scanner()
	at := time.Date(2026, time.October, 5, 7, 0, 0, 0, time.UTC)
	if frames := feedOSC9Chunks(
		t,
		scanner,
		[]byte("\x1b]9;private incomplete text"),
		nil,
		at,
	); len(frames) != 0 {
		t.Fatalf("incomplete frames = %d, want 0", len(frames))
	}
	scanner.Reset()
	frames := feedOSC9Chunks(
		t,
		scanner,
		[]byte("\x1b]9;Approval requested: after reset\x07"),
		nil,
		at.Add(time.Second),
	)
	if len(frames) != 1 {
		t.Fatalf("frames after reset = %d, want 1", len(frames))
	}
}

func TestOSC9FrameReturnsPayloadCopies(t *testing.T) {
	frames := feedOSC9Chunks(
		t,
		NewOSC9Scanner(),
		[]byte("\x1b]9;Approval requested: copy\x07"),
		nil,
		time.Date(2026, time.October, 5, 8, 0, 0, 0, time.UTC),
	)
	first := frames[0].Payload()
	first[0] = 'X'
	if got := string(frames[0].Payload()); got != "Approval requested: copy" {
		t.Fatalf("payload after caller mutation = %q", got)
	}
}

func loadOSC9Fixture(t *testing.T, path string) []byte {
	t.Helper()

	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read OSC 9 fixture: %v", err)
	}
	data, err := hex.DecodeString(strings.Join(strings.Fields(string(encoded)), ""))
	if err != nil {
		t.Fatalf("decode OSC 9 fixture: %v", err)
	}
	return data
}

func feedOSC9Chunks(
	t *testing.T,
	scanner *OSC9Scanner,
	input []byte,
	splits []int,
	start time.Time,
) []OSC9Frame {
	t.Helper()
	boundaries := append([]int{0}, splits...)
	boundaries = append(boundaries, len(input))
	var (
		frames []OSC9Frame
		seq    uint64
	)
	for index := 0; index+1 < len(boundaries); index++ {
		begin := boundaries[index]
		end := boundaries[index+1]
		if begin == end {
			continue
		}
		seq++
		committedAt := start.Add(time.Duration(seq-1) * time.Millisecond)
		chunk, err := NewCommittedChunk(
			input[begin:end],
			uint64(end),
			seq,
			committedAt,
		)
		if err != nil {
			t.Fatalf("new committed chunk %d..%d: %v", begin, end, err)
		}
		frames = append(frames, scanner.Feed(chunk)...)
	}
	return frames
}

func assertOSC9Frame(
	t *testing.T,
	frame OSC9Frame,
	payload string,
	offset uint64,
	seq uint64,
	at time.Time,
) {
	t.Helper()
	if got := string(frame.Payload()); got != payload {
		t.Fatalf("payload = %q, want %q", got, payload)
	}
	if frame.OutputOffset() != offset {
		t.Fatalf("output offset = %d, want %d", frame.OutputOffset(), offset)
	}
	if frame.LastOutputSeq() != seq {
		t.Fatalf("last output seq = %d, want %d", frame.LastOutputSeq(), seq)
	}
	if !frame.CommittedAt().Equal(at) {
		t.Fatalf("committed at = %v, want %v", frame.CommittedAt(), at)
	}
}
