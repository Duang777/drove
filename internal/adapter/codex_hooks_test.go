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
