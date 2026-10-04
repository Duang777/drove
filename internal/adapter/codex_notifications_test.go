package adapter

import (
	"bytes"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/term"
)

func TestCodexOSC9NormalizerAcceptsApprovalPrefixes(t *testing.T) {
	tests := []string{
		"Approval requested: run private-command",
		"Codex wants to edit /private/path",
		"Approval requested by private-server",
	}
	for _, body := range tests {
		t.Run(body[:strings.IndexByte(body, ' ')], func(t *testing.T) {
			at := time.Date(2026, time.October, 5, 9, 0, 0, 0, time.UTC)
			frame := scanCodexOSC9Frame(t, body, at)
			signal, matched, err := NewRegistry().
				For("codex").
				NormalizeOSC9(frame)
			if err != nil {
				t.Fatalf("normalize OSC 9: %v", err)
			}
			if !matched {
				t.Fatal("approval notification was not matched")
			}
			if signal.Source != detect.SourceNotify ||
				signal.Kind != detect.KindPermissionRequested ||
				signal.Vendor != "codex" ||
				signal.VendorEvent != "tui_notification" ||
				signal.Scope != detect.ScopeRoot ||
				signal.Notification != "approval-requested" ||
				signal.Evidence != "approval requested" ||
				signal.Confidence != 1 ||
				signal.DeliveryID != "" ||
				!signal.ReceivedAt.Equal(at) ||
				signal.Terminal == nil ||
				signal.Terminal.Protocol != "osc9" {
				t.Fatalf("signal = %+v", signal)
			}
			if encoded := strings.Join([]string{
				signal.Vendor,
				signal.VendorEvent,
				signal.Notification,
				signal.Evidence,
			}, " "); strings.Contains(encoded, "private") {
				t.Fatalf("signal retained notification body: %+v", signal)
			}
		})
	}
}

func TestCodexOSC9NormalizerIgnoresOtherMessages(t *testing.T) {
	tests := [][]byte{
		[]byte("Agent turn complete: private response"),
		[]byte("Generate a concise, single-line task title"),
		[]byte("Approval requested"),
		[]byte("Codex wants to edit"),
		[]byte("Approval requested by"),
		[]byte{0xff, 0xfe},
	}
	for index, body := range tests {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			frame := scanCodexOSC9Bytes(
				t,
				body,
				time.Date(2026, time.October, 5, 10, 0, 0, 0, time.UTC),
			)
			signal, matched, err := NewRegistry().
				For("codex").
				NormalizeOSC9(frame)
			if err != nil {
				t.Fatalf("normalize OSC 9: %v", err)
			}
			if matched || signal.Source != "" {
				t.Fatalf("unexpected match: matched=%t signal=%+v", matched, signal)
			}
		})
	}
}

func TestCodexOSC9SanitizerRetainsOnlyApprovalPrefix(t *testing.T) {
	entry := NewRegistry().For("codex")
	tests := []struct {
		name   string
		path   string
		secret string
	}{
		{
			name:   "direct",
			path:   "../term/testdata/codex-0.160.0-osc9-direct.hex",
			secret: "redacted-command",
		},
		{
			name:   "tmux",
			path:   "../term/testdata/codex-0.160.0-osc9-tmux.hex",
			secret: "redacted/path",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sanitizer, err := entry.NewOSC9Sanitizer()
			if err != nil {
				t.Fatalf("new OSC 9 sanitizer: %v", err)
			}
			input := loadAdapterOSC9Fixture(t, test.path)
			output := append(sanitizer.Feed(input), sanitizer.Flush()...)
			if len(output) != len(input) {
				t.Fatalf("sanitized length = %d, want %d", len(output), len(input))
			}
			if bytes.Contains(output, []byte(test.secret)) {
				t.Fatalf("sanitized output retained body: %q", output)
			}

			chunk, err := term.NewCommittedChunk(
				output,
				uint64(len(output)),
				17,
				time.Date(2026, time.October, 5, 10, 30, 0, 0, time.UTC),
			)
			if err != nil {
				t.Fatalf("new committed chunk: %v", err)
			}
			frames := term.NewOSC9Scanner().Feed(chunk)
			if len(frames) != 1 {
				t.Fatalf("frames = %d, want 1", len(frames))
			}
			signal, matched, err := entry.NormalizeOSC9(frames[0])
			if err != nil {
				t.Fatalf("normalize sanitized frame: %v", err)
			}
			if !matched || signal.Kind != detect.KindPermissionRequested {
				t.Fatalf("sanitized frame normalization = (%+v, %t)", signal, matched)
			}
		})
	}
}

func TestOnlyCodexSupportsTerminalNotifications(t *testing.T) {
	registry := NewRegistry()
	if !registry.For("codex").SupportsTerminalNotifications() {
		t.Fatal("codex does not support terminal notifications")
	}
	for _, vendor := range []string{"claude", "generic", "unknown"} {
		entry := registry.For(vendor)
		if entry.SupportsTerminalNotifications() {
			t.Fatalf("%s unexpectedly supports terminal notifications", vendor)
		}
		signal, matched, err := entry.NormalizeOSC9(scanCodexOSC9Frame(
			t,
			"Approval requested: command",
			time.Now().UTC(),
		))
		if err != nil || matched || signal.Source != "" {
			t.Fatalf(
				"%s normalization = (%+v, %t, %v)",
				vendor,
				signal,
				matched,
				err,
			)
		}
	}
}

func scanCodexOSC9Frame(
	t *testing.T,
	body string,
	at time.Time,
) term.OSC9Frame {
	t.Helper()
	return scanCodexOSC9Bytes(t, []byte(body), at)
}

func scanCodexOSC9Bytes(
	t *testing.T,
	body []byte,
	at time.Time,
) term.OSC9Frame {
	t.Helper()
	data := append([]byte("\x1b]9;"), body...)
	data = append(data, '\x07')
	chunk, err := term.NewCommittedChunk(data, uint64(len(data)), 17, at)
	if err != nil {
		t.Fatalf("new committed chunk: %v", err)
	}
	frames := term.NewOSC9Scanner().Feed(chunk)
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(frames))
	}
	return frames[0]
}

func loadAdapterOSC9Fixture(t *testing.T, path string) []byte {
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
