package adapter

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Duang777/drove/internal/detect"
)

type codexHookDecoder struct{}

type codexHookPayload struct {
	Type          string   `json:"type"`
	HookEventName string   `json:"hook_event_name"`
	EventName     string   `json:"event_name"`
	SessionID     string   `json:"session_id"`
	ThreadID      string   `json:"thread_id"`
	NotifyThread  string   `json:"thread-id"`
	AgentID       string   `json:"agent_id"`
	Scope         string   `json:"scope"`
	Timestamp     string   `json:"timestamp"`
	InputMessages []string `json:"input-messages"`
}

func (codexHookDecoder) NormalizeHook(input HookInput) (detect.Signal, error) {
	var payload codexHookPayload
	if err := json.Unmarshal(input.Payload, &payload); err != nil {
		return detect.Signal{}, fmt.Errorf(
			"%w: decode codex JSON: %v",
			ErrInvalidHookPayload,
			err,
		)
	}
	if payload.Type != "" {
		return normalizeCodexNotify(input, payload)
	}
	eventName := payload.HookEventName
	if eventName == "" {
		eventName = payload.EventName
	} else if payload.EventName != "" && payload.EventName != eventName {
		return detect.Signal{}, fmt.Errorf(
			"%w: conflicting codex event names",
			ErrInvalidHookPayload,
		)
	}
	sessionID := payload.SessionID
	if sessionID == "" {
		sessionID = payload.ThreadID
	}
	if err := validateHookEnvelope(
		input.DeliveryID,
		eventName,
		sessionID,
		input.ReceivedAt,
	); err != nil {
		return detect.Signal{}, err
	}
	scope, err := normalizeScope(payload.Scope, payload.AgentID, eventName)
	if err != nil {
		return detect.Signal{}, err
	}
	occurredAt, err := parseOccurredAt(payload.Timestamp)
	if err != nil {
		return detect.Signal{}, err
	}
	kind, evidence, err := classifyCodexEvent(eventName)
	if err != nil {
		return detect.Signal{}, err
	}
	signal, err := detect.NewHookSignal(detect.Signal{
		Kind:             kind,
		Vendor:           "codex",
		VendorEvent:      eventName,
		Scope:            scope,
		VendorSessionRef: sessionID,
		Evidence:         evidence,
		Confidence:       1,
		OccurredAt:       occurredAt,
		ReceivedAt:       input.ReceivedAt,
		DeliveryID:       input.DeliveryID,
	})
	if err != nil {
		return detect.Signal{}, fmt.Errorf("%w: %v", ErrInvalidHookPayload, err)
	}
	return signal, nil
}

func normalizeCodexNotify(
	input HookInput,
	payload codexHookPayload,
) (detect.Signal, error) {
	if payload.Type != "agent-turn-complete" {
		return detect.Signal{}, fmt.Errorf(
			"%w: codex notify %q",
			ErrUnknownHookEvent,
			payload.Type,
		)
	}
	if len(payload.InputMessages) == 1 &&
		strings.HasPrefix(
			payload.InputMessages[0],
			"Generate a concise, single-line task title",
		) {
		return detect.Signal{}, ErrIgnoredHookPayload
	}
	if err := validateHookEnvelope(
		input.DeliveryID,
		payload.Type,
		payload.NotifyThread,
		input.ReceivedAt,
	); err != nil {
		return detect.Signal{}, err
	}
	signal, err := detect.NewNotifySignal(detect.Signal{
		Kind:             detect.KindTurnStopped,
		Vendor:           "codex",
		VendorEvent:      payload.Type,
		Scope:            detect.ScopeRoot,
		VendorSessionRef: payload.NotifyThread,
		Evidence:         "turn stopped",
		Confidence:       1,
		ReceivedAt:       input.ReceivedAt,
		DeliveryID:       input.DeliveryID,
	})
	if err != nil {
		return detect.Signal{}, fmt.Errorf("%w: %v", ErrInvalidHookPayload, err)
	}
	return signal, nil
}

func classifyCodexEvent(eventName string) (detect.Kind, string, error) {
	switch eventName {
	case "SessionStart":
		return detect.KindSessionStarted, "session started", nil
	case "UserPromptSubmit":
		return detect.KindTurnStarted, "turn started", nil
	case "PreToolUse", "PostToolUse":
		return detect.KindToolActivity, "tool activity", nil
	case "PermissionRequest":
		return detect.KindPermissionRequested, "permission requested", nil
	case "PreCompact", "PostCompact":
		return detect.KindObserved, "compact", nil
	case "SubagentStart":
		return detect.KindSubagentStarted, "subagent started", nil
	case "SubagentStop":
		return detect.KindSubagentStopped, "subagent stopped", nil
	case "Stop":
		return detect.KindTurnStopped, "turn stopped", nil
	case "Interrupt":
		return detect.KindInterrupted, "interrupted", nil
	case "SessionEnd":
		return detect.KindSessionEnded, "session ended", nil
	default:
		return "", "", fmt.Errorf(
			"%w: codex event %q",
			ErrUnknownHookEvent,
			eventName,
		)
	}
}
