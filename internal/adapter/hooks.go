package adapter

import (
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
	// ErrIgnoredHookPayload indicates a valid vendor-internal event with no Drove meaning.
	ErrIgnoredHookPayload = errors.New("adapter: ignored hook payload")
)

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
