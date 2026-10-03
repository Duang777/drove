package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Duang777/drove/internal/detect"
)

var (
	// ErrUnsupportedHook indicates that a vendor has no hook normalizer.
	ErrUnsupportedHook = errors.New("adapter: vendor hooks are unsupported")
	// ErrInvalidHookPayload indicates malformed or incomplete vendor JSON.
	ErrInvalidHookPayload = errors.New("adapter: invalid hook payload")
	// ErrUnknownHookEvent indicates an event outside the vendor allowlist.
	ErrUnknownHookEvent = errors.New("adapter: unknown hook event")
)

type claudeHookDecoder struct{}
type codexHookDecoder struct{}

type claudeHookPayload struct {
	HookEventName    string `json:"hook_event_name"`
	SessionID        string `json:"session_id"`
	PromptID         string `json:"prompt_id"`
	AgentID          string `json:"agent_id"`
	Scope            string `json:"scope"`
	NotificationType string `json:"notification_type"`
	Timestamp        string `json:"timestamp"`
}

type codexHookPayload struct {
	HookEventName string `json:"hook_event_name"`
	EventName     string `json:"event_name"`
	SessionID     string `json:"session_id"`
	ThreadID      string `json:"thread_id"`
	TurnID        string `json:"turn_id"`
	AgentID       string `json:"agent_id"`
	Scope         string `json:"scope"`
	Timestamp     string `json:"timestamp"`
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

func (codexHookDecoder) NormalizeHook(input HookInput) (detect.Signal, error) {
	var payload codexHookPayload
	if err := json.Unmarshal(input.Payload, &payload); err != nil {
		return detect.Signal{}, fmt.Errorf(
			"%w: decode codex JSON: %v",
			ErrInvalidHookPayload,
			err,
		)
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
		Kind:            kind,
		Vendor:          "codex",
		VendorEvent:     eventName,
		Scope:           scope,
		VendorSessionID: sessionID,
		VendorTurnID:    payload.TurnID,
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

func validateHookEnvelope(
	deliveryID string,
	eventName string,
	sessionID string,
	receivedAt time.Time,
) error {
	if deliveryID == "" || strings.TrimSpace(deliveryID) != deliveryID ||
		len(deliveryID) > 128 {
		return fmt.Errorf(
			"%w: delivery_id must contain 1 to 128 non-space-edge bytes",
			ErrInvalidHookPayload,
		)
	}
	if eventName == "" || len(eventName) > 64 {
		return fmt.Errorf("%w: hook event name is required", ErrInvalidHookPayload)
	}
	if sessionID == "" || len(sessionID) > 256 {
		return fmt.Errorf("%w: vendor session ID is required", ErrInvalidHookPayload)
	}
	if receivedAt.IsZero() {
		return fmt.Errorf("%w: receive time is required", ErrInvalidHookPayload)
	}
	return nil
}

func normalizeScope(
	rawScope string,
	agentID string,
	eventName string,
) (detect.Scope, error) {
	switch rawScope {
	case "", string(detect.ScopeRoot):
		if agentID != "" || strings.HasPrefix(eventName, "Subagent") {
			return detect.ScopeSubagent, nil
		}
		return detect.ScopeRoot, nil
	case string(detect.ScopeSubagent):
		return detect.ScopeSubagent, nil
	default:
		return "", fmt.Errorf(
			"%w: unknown scope %q",
			ErrInvalidHookPayload,
			rawScope,
		)
	}
}

func parseOccurredAt(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf(
			"%w: invalid timestamp: %v",
			ErrInvalidHookPayload,
			err,
		)
	}
	return parsed, nil
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
