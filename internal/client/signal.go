package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Duang777/drove/internal/session"
)

const (
	// MaxHookPayloadBytes bounds one vendor hook JSON document.
	MaxHookPayloadBytes = session.MaxSignalPayloadBytes
)

type signalRequest struct {
	Version    int             `json:"version"`
	Vendor     string          `json:"vendor"`
	DeliveryID string          `json:"delivery_id"`
	Payload    json.RawMessage `json:"payload"`
}

// RelaySignal forwards one vendor hook document to its injected session URL.
func RelaySignal(
	ctx context.Context,
	signalURL string,
	token string,
	vendor string,
	deliveryID string,
	payload []byte,
) error {
	if err := validateSignalURL(signalURL); err != nil {
		return err
	}
	if token == "" {
		return errors.New("client: signal token is required")
	}
	if vendor != "claude" && vendor != "codex" {
		return fmt.Errorf("client: unsupported hook vendor %q", vendor)
	}
	if strings.TrimSpace(deliveryID) == "" {
		return errors.New("client: delivery ID is required")
	}
	if len(payload) == 0 {
		return errors.New("client: hook payload is empty")
	}
	if len(payload) > MaxHookPayloadBytes {
		return fmt.Errorf("client: hook payload exceeds %d bytes", MaxHookPayloadBytes)
	}
	if !utf8.Valid(payload) {
		return errors.New("client: hook payload is not valid UTF-8")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		return fmt.Errorf("client: hook payload must be one JSON object: %w", err)
	}
	if object == nil {
		return errors.New("client: hook payload must be one JSON object")
	}

	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(signalRequest{
		Version:    1,
		Vendor:     vendor,
		DeliveryID: deliveryID,
		Payload:    json.RawMessage(payload),
	}); err != nil {
		return fmt.Errorf("client: encode signal request: %w", err)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		signalURL,
		&body,
	)
	if err != nil {
		return fmt.Errorf("client: create signal request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)

	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return fmt.Errorf("client: relay hook signal: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusBadRequest {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf(
			"client: signal endpoint returned %s: %s",
			response.Status,
			strings.TrimSpace(string(message)),
		)
	}
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf(
			"client: signal endpoint returned %s, want 204 No Content",
			response.Status,
		)
	}
	return drain(response.Body)
}

func validateSignalURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("client: parse signal URL: %w", err)
	}
	if parsed.Scheme != "http" ||
		parsed.Host == "" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return fmt.Errorf("client: signal URL must be a loopback HTTP URL")
	}
	host := parsed.Hostname()
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("client: signal URL host %q must be loopback", host)
		}
	}
	return nil
}
