package adapter

import (
	"errors"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/detect"
)

func TestCodexHookAcceptsEventAndThreadAliases(t *testing.T) {
	signal, err := NewRegistry().For("codex").NormalizeHook(HookInput{
		DeliveryID: deliveryID("codex-aliases"),
		ReceivedAt: time.Now().UTC(),
		Payload: []byte(`{
			"event_name":"UserPromptSubmit",
			"thread_id":"thread-1",
			"turn_id":"turn-1"
		}`),
	})
	if err != nil {
		t.Fatalf("normalize aliases: %v", err)
	}
	if signal.Kind != detect.KindTurnStarted ||
		signal.VendorSessionID != "thread-1" ||
		signal.VendorTurnID != "turn-1" {
		t.Fatalf("signal = %+v", signal)
	}
}

func TestCodexHookRejectsConflictingEventNames(t *testing.T) {
	_, err := NewRegistry().For("codex").NormalizeHook(HookInput{
		DeliveryID: deliveryID("codex-conflict"),
		ReceivedAt: time.Now().UTC(),
		Payload: []byte(`{
			"hook_event_name":"SessionStart",
			"event_name":"Stop",
			"session_id":"session-1"
		}`),
	})
	if !errors.Is(err, ErrInvalidHookPayload) {
		t.Fatalf("normalize error = %v, want ErrInvalidHookPayload", err)
	}
}

func TestCodexNotifyNormalizesOnlyBoundedIdentifiers(t *testing.T) {
	signal, err := NewRegistry().For("codex").NormalizeHook(HookInput{
		DeliveryID: deliveryID("codex-notify"),
		ReceivedAt: time.Now().UTC(),
		Payload: []byte(`{
			"type":"agent-turn-complete",
			"thread-id":"thread-1",
			"turn-id":"turn-1",
			"cwd":"/secret/project",
			"input-messages":["private prompt"],
			"last-assistant-message":"private response"
		}`),
	})
	if err != nil {
		t.Fatalf("normalize notify: %v", err)
	}
	if signal.Source != detect.SourceNotify ||
		signal.Kind != detect.KindTurnStopped ||
		signal.VendorEvent != "agent-turn-complete" ||
		signal.VendorSessionID != "thread-1" ||
		signal.VendorTurnID != "turn-1" ||
		signal.Evidence != "turn stopped" {
		t.Fatalf("signal = %+v", signal)
	}
	for _, sensitive := range []string{"/secret/project", "private prompt", "private response"} {
		if signal.Evidence == sensitive ||
			signal.VendorSessionID == sensitive ||
			signal.VendorTurnID == sensitive {
			t.Fatalf("signal retained sensitive value %q: %+v", sensitive, signal)
		}
	}
}

func TestCodexNotifyIgnoresInternalTitleTurn(t *testing.T) {
	_, err := NewRegistry().For("codex").NormalizeHook(HookInput{
		DeliveryID: deliveryID("codex-title"),
		ReceivedAt: time.Now().UTC(),
		Payload: []byte(`{
			"type":"agent-turn-complete",
			"thread-id":"title-thread",
			"turn-id":"title-turn",
			"input-messages":[
				"Generate a concise, single-line task title for this work"
			]
		}`),
	})
	if !errors.Is(err, ErrIgnoredHookPayload) {
		t.Fatalf("normalize title notify error = %v, want ErrIgnoredHookPayload", err)
	}
}

func TestCodexNotifyRejectsUnknownType(t *testing.T) {
	_, err := NewRegistry().For("codex").NormalizeHook(HookInput{
		DeliveryID: deliveryID("codex-notify-unknown"),
		ReceivedAt: time.Now().UTC(),
		Payload: []byte(`{
			"type":"approval-requested",
			"thread-id":"thread-1"
		}`),
	})
	if !errors.Is(err, ErrUnknownHookEvent) {
		t.Fatalf("normalize notify error = %v, want ErrUnknownHookEvent", err)
	}
}
