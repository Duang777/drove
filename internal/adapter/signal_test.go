package adapter

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/agent"
)

func TestClaudeHookFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/claude-session-start.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	receivedAt := time.Date(2026, time.October, 3, 10, 0, 0, 0, time.UTC)
	signal, err := NewRegistry().For("claude").DecodeHook(raw, "delivery-1", receivedAt)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if signal.Source != SignalSourceHook ||
		signal.Kind != SignalObserved ||
		signal.Vendor != "claude" ||
		signal.VendorEvent != "SessionStart" ||
		signal.Scope != SignalScopeRoot ||
		signal.SessionRef != "claude-session-1" ||
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
	signal, err := NewRegistry().For("codex").DecodeHook(
		raw,
		"delivery-2",
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if signal.Kind != SignalWorking ||
		signal.Vendor != "codex" ||
		signal.SessionRef != "codex-thread-1" ||
		signal.TurnRef != "codex-turn-1" {
		t.Fatalf("signal = %+v", signal)
	}
}

func TestClaudeHookEventMapping(t *testing.T) {
	tests := []struct {
		event        string
		notification string
		agentID      string
		wantKind     SignalKind
		wantScope    SignalScope
	}{
		{event: "SessionStart", wantKind: SignalObserved, wantScope: SignalScopeRoot},
		{event: "UserPromptSubmit", wantKind: SignalWorking, wantScope: SignalScopeRoot},
		{event: "PreToolUse", wantKind: SignalWorking, wantScope: SignalScopeRoot},
		{event: "PostToolUseFailure", wantKind: SignalWorking, wantScope: SignalScopeRoot},
		{event: "PermissionRequest", wantKind: SignalBlocked, wantScope: SignalScopeRoot},
		{event: "Elicitation", wantKind: SignalBlocked, wantScope: SignalScopeRoot},
		{event: "ElicitationResult", wantKind: SignalWorking, wantScope: SignalScopeRoot},
		{event: "Stop", wantKind: SignalIdle, wantScope: SignalScopeRoot},
		{event: "StopFailure", wantKind: SignalIdle, wantScope: SignalScopeRoot},
		{event: "TaskCompleted", wantKind: SignalObserved, wantScope: SignalScopeRoot},
		{
			event:     "SubagentStop",
			agentID:   "subagent-1",
			wantKind:  SignalWorking,
			wantScope: SignalScopeSubagent,
		},
		{
			event:        "Notification",
			notification: "agent_needs_input",
			wantKind:     SignalBlocked,
			wantScope:    SignalScopeRoot,
		},
		{
			event:        "Notification",
			notification: "idle_prompt",
			wantKind:     SignalIdle,
			wantScope:    SignalScopeRoot,
		},
		{
			event:        "Notification",
			notification: "agent_completed",
			wantKind:     SignalObserved,
			wantScope:    SignalScopeRoot,
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
			signal, err := entry.DecodeHook(raw, "delivery-"+test.event, time.Now().UTC())
			if err != nil {
				t.Fatalf("decode hook: %v", err)
			}
			if signal.Kind != test.wantKind || signal.Scope != test.wantScope {
				t.Fatalf("signal = %+v, want kind=%s scope=%s", signal, test.wantKind, test.wantScope)
			}
		})
	}
}

func TestCodexHookEventMapping(t *testing.T) {
	tests := []struct {
		event    string
		agentID  string
		wantKind SignalKind
	}{
		{event: "SessionStart", wantKind: SignalObserved},
		{event: "UserPromptSubmit", wantKind: SignalWorking},
		{event: "PreToolUse", wantKind: SignalWorking},
		{event: "PostToolUse", wantKind: SignalWorking},
		{event: "PermissionRequest", wantKind: SignalBlocked},
		{event: "Stop", wantKind: SignalIdle},
		{event: "Interrupt", wantKind: SignalIdle},
		{event: "SessionEnd", wantKind: SignalObserved},
		{event: "SubagentStop", agentID: "subagent-1", wantKind: SignalWorking},
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
			signal, err := entry.DecodeHook(raw, "delivery-"+test.event, time.Now().UTC())
			if err != nil {
				t.Fatalf("decode hook: %v", err)
			}
			if signal.Kind != test.wantKind {
				t.Fatalf("signal kind = %s, want %s", signal.Kind, test.wantKind)
			}
		})
	}
}

func TestHookDecoderRejectsUnknownAndInvalidPayloads(t *testing.T) {
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
			delivery: "delivery-1",
			wantErr:  ErrUnsupportedHook,
		},
		{
			name:     "unknown claude event",
			vendor:   "claude",
			raw:      `{"hook_event_name":"FutureEvent","session_id":"s1"}`,
			delivery: "delivery-1",
			wantErr:  ErrUnknownHookEvent,
		},
		{
			name:     "unknown notification",
			vendor:   "claude",
			raw:      `{"hook_event_name":"Notification","session_id":"s1","notification_type":"future"}`,
			delivery: "delivery-1",
			wantErr:  ErrUnknownHookEvent,
		},
		{
			name:     "missing session",
			vendor:   "codex",
			raw:      `{"hook_event_name":"Stop"}`,
			delivery: "delivery-1",
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
			delivery: "delivery-1",
			wantErr:  ErrInvalidHookPayload,
		},
	}

	registry := NewRegistry()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := registry.For(test.vendor).DecodeHook(
				[]byte(test.raw),
				test.delivery,
				time.Now().UTC(),
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("decode error = %v, want %v", err, test.wantErr)
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
	signal, err := NewRegistry().For("claude").DecodeHook(
		raw,
		"delivery-1",
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("decode hook: %v", err)
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

func TestNewHeuristicSignalMapsStateAndConfidence(t *testing.T) {
	at := time.Now().UTC()
	signal := NewHeuristicSignal("claude", StateHint{
		State:      agent.StateBlocked,
		Confidence: 0.9,
		Reason:     "claude awaiting input",
	}, at)
	if signal.Source != SignalSourceHeuristic ||
		signal.Kind != SignalBlocked ||
		signal.Vendor != "claude" ||
		signal.Evidence != "claude awaiting input" ||
		signal.Confidence != 0.9 ||
		!signal.ReceivedAt.Equal(at) {
		t.Fatalf("signal = %+v", signal)
	}
}
