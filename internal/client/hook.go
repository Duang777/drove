package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Duang777/drove/internal/session"
)

const (
	// MaxHookPayloadBytes bounds one vendor hook JSON document.
	MaxHookPayloadBytes = session.MaxSignalPayloadBytes

	hookAttemptTimeout = 750 * time.Millisecond
	hookRetryDelay     = 100 * time.Millisecond
	hookAttemptCount   = 2
)

// HookRelayConfig identifies one session-scoped signal endpoint.
type HookRelayConfig struct {
	AgentID   string
	SignalURL string
	Token     string
}

// HookRelay forwards vendor hook documents using a session capability.
type HookRelay struct {
	signalURL string
	token     string
	client    *http.Client
}

type signalRequest struct {
	Version    int             `json:"version"`
	Vendor     string          `json:"vendor"`
	DeliveryID string          `json:"delivery_id"`
	Payload    json.RawMessage `json:"payload"`
}

// NewHookRelay validates and creates a session-scoped hook relay.
func NewHookRelay(config HookRelayConfig) (*HookRelay, error) {
	if config.AgentID == "" || strings.TrimSpace(config.AgentID) != config.AgentID {
		return nil, errors.New("client: signal agent ID is required")
	}
	if config.Token == "" {
		return nil, errors.New("client: signal token is required")
	}
	if err := validateSignalURL(config.SignalURL, config.AgentID); err != nil {
		return nil, err
	}
	return &HookRelay{
		signalURL: config.SignalURL,
		token:     config.Token,
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Forward sends one vendor hook document with a new delivery ID.
func (r *HookRelay) Forward(
	ctx context.Context,
	vendor string,
	payload []byte,
) error {
	if vendor == "" || strings.TrimSpace(vendor) != vendor {
		return errors.New("client: hook vendor is required")
	}
	if err := validateHookPayload(payload); err != nil {
		return err
	}

	body, err := encodeSignalRequest(signalRequest{
		Version:    1,
		Vendor:     vendor,
		DeliveryID: uuid.NewString(),
		Payload:    append(json.RawMessage(nil), payload...),
	})
	if err != nil {
		return err
	}

	var lastErr error
	for attempt := 0; attempt < hookAttemptCount; attempt++ {
		retry, err := r.forwardAttempt(ctx, body)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retry || attempt+1 == hookAttemptCount || ctx.Err() != nil {
			return err
		}
		if err := waitForHookRetry(ctx); err != nil {
			return fmt.Errorf("client: relay hook signal: %w", err)
		}
	}
	return lastErr
}

func (r *HookRelay) forwardAttempt(
	ctx context.Context,
	body []byte,
) (bool, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, hookAttemptTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(
		attemptCtx,
		http.MethodPost,
		r.signalURL,
		bytes.NewReader(body),
	)
	if err != nil {
		return false, fmt.Errorf("client: create signal request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+r.token)

	response, err := r.client.Do(request)
	if err != nil {
		return true, fmt.Errorf("client: relay hook signal: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent {
		if err := drain(response.Body); err != nil {
			return false, fmt.Errorf("client: read signal response: %w", err)
		}
		return false, nil
	}

	_ = drain(response.Body)
	retry := response.StatusCode == http.StatusTooManyRequests ||
		response.StatusCode == http.StatusServiceUnavailable
	return retry, fmt.Errorf(
		"client: signal endpoint returned %s",
		response.Status,
	)
}

func encodeSignalRequest(request signalRequest) ([]byte, error) {
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(request); err != nil {
		return nil, fmt.Errorf("client: encode signal request: %w", err)
	}
	return body.Bytes(), nil
}

func validateHookPayload(payload []byte) error {
	if len(payload) == 0 {
		return errors.New("client: hook payload is empty")
	}
	if len(payload) > MaxHookPayloadBytes {
		return fmt.Errorf(
			"client: hook payload exceeds %d bytes",
			MaxHookPayloadBytes,
		)
	}
	if !utf8.Valid(payload) {
		return errors.New("client: hook payload is not valid UTF-8")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil || object == nil {
		if err != nil {
			return fmt.Errorf(
				"client: hook payload must be one JSON object: %w",
				err,
			)
		}
		return errors.New("client: hook payload must be one JSON object")
	}
	return nil
}

func validateSignalURL(rawURL, agentID string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return errors.New("client: invalid signal URL")
	}
	if parsed.Scheme != "http" ||
		parsed.Host == "" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.ForceQuery ||
		parsed.Fragment != "" {
		return errors.New("client: signal URL must be an absolute loopback HTTP URL")
	}
	host := parsed.Hostname()
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("client: signal URL host must be loopback")
		}
	}
	wantPath := "/api/v1/agents/" + url.PathEscape(agentID) + "/signal"
	if parsed.EscapedPath() != wantPath {
		return errors.New("client: signal URL path does not match agent ID")
	}
	return nil
}

func waitForHookRetry(ctx context.Context) error {
	timer := time.NewTimer(hookRetryDelay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
