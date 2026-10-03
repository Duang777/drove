package adapter

import (
	"encoding/json"
	"fmt"

	"github.com/Duang777/drove/internal/detect"
)

type claudeHookDecoder struct{}

type claudeHookPayload struct {
	HookEventName    string `json:"hook_event_name"`
	SessionID        string `json:"session_id"`
	PromptID         string `json:"prompt_id"`
	AgentID          string `json:"agent_id"`
	Scope            string `json:"scope"`
	NotificationType string `json:"notification_type"`
	Timestamp        string `json:"timestamp"`
}

func (claudeHookDecoder) NormalizeHook(input HookInput) (detect.Signal, error) {
	var payload claudeHookPayload
	if err := json.Unmarshal(input.Payload, &payload); err != nil {
		return detect.Signal{}, fmt.Errorf(
			"%w: decode claude JSON: %v",
			ErrInvalidHookPayload,
			err,
		)
	}
	if err := validateHookEnvelope(
		input.DeliveryID,
		payload.HookEventName,
		payload.SessionID,
		input.ReceivedAt,
	); err != nil {
		return detect.Signal{}, err
	}
	scope, err := normalizeScope(
		payload.Scope,
		payload.AgentID,
		payload.HookEventName,
	)
	if err != nil {
		return detect.Signal{}, err
	}
	occurredAt, err := parseOccurredAt(payload.Timestamp)
	if err != nil {
		return detect.Signal{}, err
	}
	kind, evidence, notification, err := classifyClaudeEvent(
		payload.HookEventName,
		payload.NotificationType,
	)
	if err != nil {
		return detect.Signal{}, err
	}
	signal, err := detect.NewHookSignal(detect.Signal{
		Kind:            kind,
		Vendor:          "claude",
		VendorEvent:     payload.HookEventName,
		Scope:           scope,
		VendorSessionID: payload.SessionID,
		VendorTurnID:    payload.PromptID,
		Notification:    notification,
		Evidence:        evidence,
		Confidence:      1,
		OccurredAt:      occurredAt,
		ReceivedAt:      input.ReceivedAt,
		DeliveryID:      input.DeliveryID,
	})
	if err != nil {
		return detect.Signal{}, fmt.Errorf("%w: %v", ErrInvalidHookPayload, err)
	}
	return signal, nil
}

func classifyClaudeEvent(
	eventName string,
	notificationType string,
) (detect.Kind, string, string, error) {
	switch eventName {
	case "SessionStart":
		return detect.KindSessionStarted, "session started", "", nil
	case "UserPromptSubmit":
		return detect.KindTurnStarted, "turn started", "", nil
	case "PreToolUse", "PostToolUse", "PostToolUseFailure", "PostToolBatch":
		return detect.KindToolActivity, "tool activity", "", nil
	case "PermissionRequest":
		return detect.KindPermissionRequested, "permission requested", "", nil
	case "PermissionDenied":
		return detect.KindPermissionResolved, "permission resolved", "", nil
	case "Elicitation":
		return detect.KindHumanInputRequired, "human input required", "", nil
	case "ElicitationResult":
		return detect.KindHumanInputResolved, "human input resolved", "", nil
	case "Notification":
		kind, evidence, normalized := classifyClaudeNotification(notificationType)
		return kind, evidence, normalized, nil
	case "Stop":
		return detect.KindTurnStopped, "turn stopped", "", nil
	case "StopFailure":
		return detect.KindTurnFailed, "turn failed", "", nil
	case "SubagentStart":
		return detect.KindSubagentStarted, "subagent started", "", nil
	case "SubagentStop":
		return detect.KindSubagentStopped, "subagent stopped", "", nil
	case "TaskCompleted":
		return detect.KindTaskCompleted, "task completed", "", nil
	case "SessionEnd":
		return detect.KindSessionEnded, "session ended", "", nil
	default:
		return "", "", "", fmt.Errorf(
			"%w: claude event %q",
			ErrUnknownHookEvent,
			eventName,
		)
	}
}

func classifyClaudeNotification(
	notificationType string,
) (detect.Kind, string, string) {
	switch notificationType {
	case "permission_prompt",
		"elicitation_dialog",
		"elicitation_url_dialog",
		"agent_needs_input",
		"quota_auto_resume_stale":
		return detect.KindHumanInputRequired, "human input required", notificationType
	case "idle_prompt":
		return detect.KindIdlePrompt, "idle prompt", notificationType
	case "auth_success", "elicitation_complete", "elicitation_response":
		return detect.KindHumanInputResolved, "human input resolved", notificationType
	case "agent_completed":
		return detect.KindTaskCompleted, "task completed", notificationType
	case "quota_auto_resume_fired", "quota_auto_resume_disabled":
		return detect.KindObserved, "notification", notificationType
	default:
		return detect.KindObserved, "notification", "other"
	}
}
