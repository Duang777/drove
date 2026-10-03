package adapter

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Duang777/drove/internal/detect"
)

func TestClaudeHookFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/claude-session-start.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	receivedAt := time.Date(2026, time.October, 3, 10, 0, 0, 0, time.UTC)
	signal, err := NewRegistry().For("claude").NormalizeHook(HookInput{
		Payload:    raw,
		DeliveryID: deliveryID("claude-fixture"),
		ReceivedAt: receivedAt,
	})
	if err != nil {
		t.Fatalf("normalize fixture: %v", err)
	}
	if signal.Source != detect.SourceHook ||
		signal.Kind != detect.KindSessionStarted ||
		signal.Vendor != "claude" ||
		signal.VendorEvent != "SessionStart" ||
		signal.Scope != detect.ScopeRoot ||
		signal.VendorSessionID != "claude-session-1" ||
		signal.Confidence != 1 ||
		!signal.ReceivedAt.Equal(receivedAt) {
		t.Fatalf("signal = %+v", signal)
	}
}

func TestCodexHookFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/codex-user-prompt-submit.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	signal, err := NewRegistry().For("codex").NormalizeHook(HookInput{
		Payload:    raw,
		DeliveryID: deliveryID("codex-fixture"),
		ReceivedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("normalize fixture: %v", err)
	}
	if signal.Kind != detect.KindTurnStarted ||
		signal.Vendor != "codex" ||
		signal.VendorSessionID != "codex-thread-1" ||
		signal.VendorTurnID != "codex-turn-1" {
		t.Fatalf("signal = %+v", signal)
	}
}

func TestClaudeHookEventMapping(t *testing.T) {
	tests := []struct {
		event        string
		notification string
		agentID      string
		wantKind     detect.Kind
		wantScope    detect.Scope
		wantNotice   string
	}{
		{event: "SessionStart", wantKind: detect.KindSessionStarted, wantScope: detect.ScopeRoot},
		{event: "UserPromptSubmit", wantKind: detect.KindTurnStarted, wantScope: detect.ScopeRoot},
		{event: "PreToolUse", wantKind: detect.KindToolActivity, wantScope: detect.ScopeRoot},
		{event: "PostToolUseFailure", wantKind: detect.KindToolActivity, wantScope: detect.ScopeRoot},
		{event: "PermissionRequest", wantKind: detect.KindPermissionRequested, wantScope: detect.ScopeRoot},
		{event: "PermissionDenied", wantKind: detect.KindPermissionResolved, wantScope: detect.ScopeRoot},
		{event: "Elicitation", wantKind: detect.KindHumanInputRequired, wantScope: detect.ScopeRoot},
		{event: "ElicitationResult", wantKind: detect.KindHumanInputResolved, wantScope: detect.ScopeRoot},
		{event: "Stop", wantKind: detect.KindTurnStopped, wantScope: detect.ScopeRoot},
		{event: "StopFailure", wantKind: detect.KindTurnFailed, wantScope: detect.ScopeRoot},
		{event: "TaskCompleted", wantKind: detect.KindTaskCompleted, wantScope: detect.ScopeRoot},
		{
			event:     "SubagentStop",
			agentID:   "subagent-1",
			wantKind:  detect.KindSubagentStopped,
			wantScope: detect.ScopeSubagent,
		},
		{
			event:        "Notification",
			notification: "agent_needs_input",
			wantKind:     detect.KindHumanInputRequired,
			wantScope:    detect.ScopeRoot,
			wantNotice:   "agent_needs_input",
		},
		{
			event:        "Notification",
			notification: "idle_prompt",
			wantKind:     detect.KindIdlePrompt,
			wantScope:    detect.ScopeRoot,
			wantNotice:   "idle_prompt",
		},
		{
			event:        "Notification",
			notification: "agent_completed",
			wantKind:     detect.KindTaskCompleted,
			wantScope:    detect.ScopeRoot,
			wantNotice:   "agent_completed",
		},
		{
			event:        "Notification",
			notification: "future_notification",
			wantKind:     detect.KindObserved,
			wantScope:    detect.ScopeRoot,
			wantNotice:   "other",
		},
	}

	entry := NewRegistry().For("claude")
	for _, test := range tests {
		t.Run(test.event+"/"+test.notification, func(t *testing.T) {
			raw, err := json.Marshal(map[string]string{
				"hook_event_name":   test.event,
				"session_id":        "session-1",
				"agent_id":          test.agentID,
				"notification_type": test.notification,
			})
			if err != nil {
				t.Fatalf("encode hook: %v", err)
			}
			signal, err := entry.NormalizeHook(HookInput{
				Payload:    raw,
				DeliveryID: deliveryID(test.event + test.notification),
				ReceivedAt: time.Now().UTC(),
			})
			if err != nil {
				t.Fatalf("normalize hook: %v", err)
			}
			if signal.Kind != test.wantKind ||
				signal.Scope != test.wantScope ||
				signal.Notification != test.wantNotice {
				t.Fatalf(
					"signal = %+v, want kind=%s scope=%s notification=%q",
					signal,
					test.wantKind,
					test.wantScope,
					test.wantNotice,
				)
			}
		})
	}
}

func TestCodexHookEventMapping(t *testing.T) {
	tests := []struct {
		event    string
		agentID  string
		wantKind detect.Kind
	}{
		{event: "SessionStart", wantKind: detect.KindSessionStarted},
		{event: "UserPromptSubmit", wantKind: detect.KindTurnStarted},
		{event: "PreToolUse", wantKind: detect.KindToolActivity},
		{event: "PostToolUse", wantKind: detect.KindToolActivity},
		{event: "PermissionRequest", wantKind: detect.KindPermissionRequested},
		{event: "Stop", wantKind: detect.KindTurnStopped},
		{event: "Interrupt", wantKind: detect.KindInterrupted},
		{event: "SessionEnd", wantKind: detect.KindSessionEnded},
		{event: "SubagentStop", agentID: "subagent-1", wantKind: detect.KindSubagentStopped},
	}

	entry := NewRegistry().For("codex")
	for _, test := range tests {
		t.Run(test.event, func(t *testing.T) {
			raw, err := json.Marshal(map[string]string{
				"hook_event_name": test.event,
				"session_id":      "session-1",
				"agent_id":        test.agentID,
			})
			if err != nil {
				t.Fatalf("encode hook: %v", err)
			}
			signal, err := entry.NormalizeHook(HookInput{
				Payload:    raw,
				DeliveryID: deliveryID(test.event),
				ReceivedAt: time.Now().UTC(),
			})
			if err != nil {
				t.Fatalf("normalize hook: %v", err)
			}
			if signal.Kind != test.wantKind {
				t.Fatalf("signal kind = %s, want %s", signal.Kind, test.wantKind)
			}
		})
	}
}

func TestHookNormalizerRejectsUnknownAndInvalidPayloads(t *testing.T) {
	tests := []struct {
		name     string
		vendor   string
		raw      string
		delivery string
		wantErr  error
	}{
		{
			name:     "unsupported vendor",
			vendor:   "generic",
			raw:      `{"hook_event_name":"SessionStart","session_id":"s1"}`,
			delivery: deliveryID("unsupported"),
			wantErr:  ErrUnsupportedHook,
		},
		{
			name:     "unknown claude event",
			vendor:   "claude",
			raw:      `{"hook_event_name":"FutureEvent","session_id":"s1"}`,
			delivery: deliveryID("unknown"),
			wantErr:  ErrUnknownHookEvent,
		},
		{
			name:     "missing session",
			vendor:   "codex",
			raw:      `{"hook_event_name":"Stop"}`,
			delivery: deliveryID("missing-session"),
			wantErr:  ErrInvalidHookPayload,
		},
		{
			name:    "missing delivery",
			vendor:  "claude",
			raw:     `{"hook_event_name":"SessionStart","session_id":"s1"}`,
			wantErr: ErrInvalidHookPayload,
		},
		{
			name:     "malformed JSON",
			vendor:   "claude",
			raw:      `{`,
			delivery: deliveryID("malformed"),
			wantErr:  ErrInvalidHookPayload,
		},
	}

	registry := NewRegistry()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := registry.For(test.vendor)
			_, err := entry.NormalizeHook(HookInput{
				Payload:    []byte(test.raw),
				DeliveryID: test.delivery,
				ReceivedAt: time.Now().UTC(),
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("normalize error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestHookSignalNeverRetainsSensitiveFields(t *testing.T) {
	raw := []byte(`{
		"hook_event_name":"UserPromptSubmit",
		"session_id":"session-1",
		"prompt":"do not retain",
		"tool_input":{"secret":"do not retain"},
		"transcript_path":"/private/transcript"
	}`)
	signal, err := NewRegistry().For("claude").NormalizeHook(HookInput{
		Payload:    raw,
		DeliveryID: deliveryID("redaction"),
		ReceivedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("normalize hook: %v", err)
	}
	encoded, err := json.Marshal(signal)
	if err != nil {
		t.Fatalf("encode signal: %v", err)
	}
	for _, sensitive := range []string{"do not retain", "tool_input", "transcript"} {
		if strings.Contains(string(encoded), sensitive) {
			t.Fatalf("normalized signal retained %q: %s", sensitive, encoded)
		}
	}
}

func deliveryID(label string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(label)).String()
}
