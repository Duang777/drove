package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Duang777/drove/internal/agent"
)

// SignalSource identifies where a normalized state signal originated.
type SignalSource string

const (
	// SignalSourceHook is an authoritative vendor hook signal.
	SignalSourceHook SignalSource = "hook"
	// SignalSourceHeuristic is a fallback terminal classification.
	SignalSourceHeuristic SignalSource = "heuristic"
	// SignalSourceTimer is an internal confirmation signal.
	SignalSourceTimer SignalSource = "timer"
)

// SignalKind is the vendor-neutral state meaning of a signal.
type SignalKind string

const (
	SignalObserved SignalKind = "observed"
	SignalWorking  SignalKind = "working"
	SignalBlocked  SignalKind = "blocked"
	SignalIdle     SignalKind = "idle"
	SignalDone     SignalKind = "done"
)

// SignalScope distinguishes the root session from a vendor subagent.
type SignalScope string

const (
	SignalScopeRoot     SignalScope = "root"
	SignalScopeSubagent SignalScope = "subagent"
)

// Signal is the allowlisted representation of hook or heuristic evidence.
// It never contains prompts, tool input, transcripts, or assistant output.
type Signal struct {
	DeliveryID   string
	Source       SignalSource
	Kind         SignalKind
	Vendor       string
	VendorEvent  string
	Scope        SignalScope
	SessionRef   string
	TurnRef      string
	Notification string
	Evidence     string
	Confidence   float64
	OccurredAt   time.Time
	ReceivedAt   time.Time
}

var (
	// ErrUnsupportedHook indicates that a vendor has no hook decoder.
	ErrUnsupportedHook = errors.New("adapter: vendor hooks are unsupported")
	// ErrInvalidHookPayload indicates malformed or incomplete vendor JSON.
	ErrInvalidHookPayload = errors.New("adapter: invalid hook payload")
	// ErrUnknownHookEvent indicates an event outside the vendor allowlist.
	ErrUnknownHookEvent = errors.New("adapter: unknown hook event")
)

type hookDecoder interface {
	Decode([]byte, string, time.Time) (Signal, error)
}

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

func (claudeHookDecoder) Decode(
	raw []byte,
	deliveryID string,
	receivedAt time.Time,
) (Signal, error) {
	var payload claudeHookPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Signal{}, fmt.Errorf("%w: decode claude JSON: %v", ErrInvalidHookPayload, err)
	}
	if err := validateHookEnvelope(deliveryID, payload.HookEventName, payload.SessionID, receivedAt); err != nil {
		return Signal{}, err
	}
	scope, err := normalizeScope(payload.Scope, payload.AgentID, payload.HookEventName)
	if err != nil {
		return Signal{}, err
	}
	occurredAt, err := parseOccurredAt(payload.Timestamp)
	if err != nil {
		return Signal{}, err
	}
	kind, evidence, notification, err := classifyClaudeEvent(
		payload.HookEventName,
		payload.NotificationType,
	)
	if err != nil {
		return Signal{}, err
	}
	return Signal{
		DeliveryID:   deliveryID,
		Source:       SignalSourceHook,
		Kind:         kind,
		Vendor:       "claude",
		VendorEvent:  payload.HookEventName,
		Scope:        scope,
		SessionRef:   payload.SessionID,
		TurnRef:      payload.PromptID,
		Notification: notification,
		Evidence:     evidence,
		Confidence:   1,
		OccurredAt:   occurredAt,
		ReceivedAt:   receivedAt,
	}, nil
}

func (codexHookDecoder) Decode(
	raw []byte,
	deliveryID string,
	receivedAt time.Time,
) (Signal, error) {
	var payload codexHookPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Signal{}, fmt.Errorf("%w: decode codex JSON: %v", ErrInvalidHookPayload, err)
	}
	eventName := payload.HookEventName
	if eventName == "" {
		eventName = payload.EventName
	} else if payload.EventName != "" && payload.EventName != eventName {
		return Signal{}, fmt.Errorf("%w: conflicting codex event names", ErrInvalidHookPayload)
	}
	sessionRef := payload.SessionID
	if sessionRef == "" {
		sessionRef = payload.ThreadID
	}
	if err := validateHookEnvelope(deliveryID, eventName, sessionRef, receivedAt); err != nil {
		return Signal{}, err
	}
	scope, err := normalizeScope(payload.Scope, payload.AgentID, eventName)
	if err != nil {
		return Signal{}, err
	}
	occurredAt, err := parseOccurredAt(payload.Timestamp)
	if err != nil {
		return Signal{}, err
	}
	kind, evidence, err := classifyCodexEvent(eventName)
	if err != nil {
		return Signal{}, err
	}
	return Signal{
		DeliveryID:  deliveryID,
		Source:      SignalSourceHook,
		Kind:        kind,
		Vendor:      "codex",
		VendorEvent: eventName,
		Scope:       scope,
		SessionRef:  sessionRef,
		TurnRef:     payload.TurnID,
		Evidence:    evidence,
		Confidence:  1,
		OccurredAt:  occurredAt,
		ReceivedAt:  receivedAt,
	}, nil
}

// NewHeuristicSignal converts an adapter hint into Detector input.
func NewHeuristicSignal(vendor string, hint StateHint, receivedAt time.Time) Signal {
	kind := SignalObserved
	switch hint.State {
	case agent.StateWorking:
		kind = SignalWorking
	case agent.StateBlocked:
		kind = SignalBlocked
	case agent.StateIdle:
		kind = SignalIdle
	case agent.StateDone:
		kind = SignalDone
	}
	return Signal{
		Source:      SignalSourceHeuristic,
		Kind:        kind,
		Vendor:      vendor,
		VendorEvent: "terminal_hint",
		Scope:       SignalScopeRoot,
		Evidence:    hint.Reason,
		Confidence:  hint.Confidence,
		ReceivedAt:  receivedAt,
	}
}

func validateHookEnvelope(
	deliveryID string,
	eventName string,
	sessionRef string,
	receivedAt time.Time,
) error {
	if deliveryID == "" || strings.TrimSpace(deliveryID) != deliveryID || len(deliveryID) > 128 {
		return fmt.Errorf("%w: delivery_id must contain 1 to 128 non-space-edge bytes", ErrInvalidHookPayload)
	}
	if eventName == "" || len(eventName) > 64 {
		return fmt.Errorf("%w: hook event name is required", ErrInvalidHookPayload)
	}
	if sessionRef == "" || len(sessionRef) > 256 {
		return fmt.Errorf("%w: vendor session ID is required", ErrInvalidHookPayload)
	}
	if receivedAt.IsZero() {
		return fmt.Errorf("%w: receive time is required", ErrInvalidHookPayload)
	}
	return nil
}

func normalizeScope(rawScope, agentID, eventName string) (SignalScope, error) {
	switch rawScope {
	case "", string(SignalScopeRoot):
		if agentID != "" || strings.HasPrefix(eventName, "Subagent") {
			return SignalScopeSubagent, nil
		}
		return SignalScopeRoot, nil
	case string(SignalScopeSubagent):
		return SignalScopeSubagent, nil
	default:
		return "", fmt.Errorf("%w: unknown scope %q", ErrInvalidHookPayload, rawScope)
	}
}

func parseOccurredAt(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: invalid timestamp: %v", ErrInvalidHookPayload, err)
	}
	return parsed, nil
}

func classifyClaudeEvent(eventName, notificationType string) (SignalKind, string, string, error) {
	switch eventName {
	case "UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure",
		"PostToolBatch", "ElicitationResult", "SubagentStart", "SubagentStop":
		return SignalWorking, "activity", "", nil
	case "PermissionRequest", "Elicitation":
		return SignalBlocked, "human_input_required", "", nil
	case "Stop", "StopFailure":
		return SignalIdle, "turn_stopped", "", nil
	case "Notification":
		kind, evidence, err := classifyClaudeNotification(notificationType)
		return kind, evidence, notificationType, err
	case "Setup", "SessionStart", "SessionEnd", "UserPromptExpansion",
		"PermissionDenied", "TaskCreated", "TaskCompleted", "TeammateIdle",
		"PreCompact", "PostCompact", "PreModelSwitch", "PostModelSwitch",
		"InstructionsLoaded", "MessageDisplay", "ConfigChange", "CwdChanged",
		"DirectoryAdded", "FileChanged", "WorktreeCreate", "WorktreeRemove":
		return SignalObserved, "lifecycle", "", nil
	default:
		return "", "", "", fmt.Errorf("%w: claude event %q", ErrUnknownHookEvent, eventName)
	}
}

func classifyClaudeNotification(notificationType string) (SignalKind, string, error) {
	switch notificationType {
	case "permission_prompt", "elicitation_dialog", "elicitation_url_dialog",
		"agent_needs_input", "quota_auto_resume_stale":
		return SignalBlocked, "human_input_required", nil
	case "idle_prompt":
		return SignalIdle, "idle_prompt", nil
	case "elicitation_complete", "elicitation_response":
		return SignalWorking, "activity", nil
	case "auth_success", "agent_completed", "quota_auto_resume_fired",
		"quota_auto_resume_disabled":
		return SignalObserved, "notification", nil
	default:
		return "", "", fmt.Errorf(
			"%w: claude notification %q",
			ErrUnknownHookEvent,
			notificationType,
		)
	}
}

func classifyCodexEvent(eventName string) (SignalKind, string, error) {
	switch eventName {
	case "UserPromptSubmit", "PreToolUse", "PostToolUse", "SubagentStart", "SubagentStop":
		return SignalWorking, "activity", nil
	case "PermissionRequest":
		return SignalBlocked, "human_input_required", nil
	case "Stop", "Interrupt":
		return SignalIdle, "turn_stopped", nil
	case "SessionStart", "SessionEnd", "PreCompact", "PostCompact":
		return SignalObserved, "lifecycle", nil
	default:
		return "", "", fmt.Errorf("%w: codex event %q", ErrUnknownHookEvent, eventName)
	}
}
