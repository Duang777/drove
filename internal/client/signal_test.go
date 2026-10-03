package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRelaySignalPostsVersionedEnvelopeWithSessionToken(t *testing.T) {
	var received signalRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer session-token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content type = %q", r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	payload := []byte(`{"hook_event_name":"SessionStart","session_id":"vendor-session"}`)
	if err := RelaySignal(
		context.Background(),
		server.URL+"/api/v1/agents/agent-1/signal",
		"session-token",
		"claude",
		"delivery-1",
		payload,
	); err != nil {
		t.Fatalf("relay signal: %v", err)
	}
	if received.Version != 1 ||
		received.Vendor != "claude" ||
		received.DeliveryID != "delivery-1" ||
		string(received.Payload) != string(payload) {
		t.Fatalf("request = %+v", received)
	}
}

func TestRelaySignalPreservesInLimitPayloadSize(t *testing.T) {
	prefix := `{"hook_event_name":"SessionStart","session_id":"vendor-session","padding":"`
	suffix := `"}`
	payload := []byte(prefix + strings.Repeat(
		"<",
		MaxHookPayloadBytes-len(prefix)-len(suffix),
	) + suffix)
	if len(payload) != MaxHookPayloadBytes {
		t.Fatalf("payload size = %d, want %d", len(payload), MaxHookPayloadBytes)
	}

	var requestBodyBytes int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestBodyBytes = r.ContentLength
		var received signalRequest
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if len(received.Payload) != len(payload) {
			t.Errorf("received payload size = %d, want %d", len(received.Payload), len(payload))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if err := RelaySignal(
		context.Background(),
		server.URL,
		"session-token",
		"claude",
		"delivery-large",
		payload,
	); err != nil {
		t.Fatalf("relay signal: %v", err)
	}
	if requestBodyBytes > MaxHookPayloadBytes+2048 {
		t.Fatalf("request body size = %d, exceeds endpoint envelope limit", requestBodyBytes)
	}
}

func TestRelaySignalRejectsUnsafeURLAndInvalidPayload(t *testing.T) {
	tests := []struct {
		name      string
		signalURL string
		payload   []byte
	}{
		{
			name:      "HTTPS is not local daemon contract",
			signalURL: "https://127.0.0.1:7373/api/v1/agents/a/signal",
			payload:   []byte(`{}`),
		},
		{
			name:      "remote host",
			signalURL: "http://example.com/api/v1/agents/a/signal",
			payload:   []byte(`{}`),
		},
		{
			name:      "credentials",
			signalURL: "http://user@127.0.0.1:7373/api/v1/agents/a/signal",
			payload:   []byte(`{}`),
		},
		{
			name:      "JSON array",
			signalURL: "http://127.0.0.1:7373/api/v1/agents/a/signal",
			payload:   []byte(`[]`),
		},
		{
			name:      "trailing JSON",
			signalURL: "http://127.0.0.1:7373/api/v1/agents/a/signal",
			payload:   []byte(`{} {}`),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := RelaySignal(
				context.Background(),
				test.signalURL,
				"token",
				"claude",
				"delivery-1",
				test.payload,
			)
			if err == nil {
				t.Fatal("relay succeeded, want validation error")
			}
		})
	}
}

func TestRelaySignalReturnsEndpointError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	err := RelaySignal(
		context.Background(),
		server.URL,
		"wrong",
		"codex",
		"delivery-1",
		[]byte(`{"hook_event_name":"SessionStart"}`),
	)
	if err == nil || !strings.Contains(err.Error(), "401 Unauthorized") {
		t.Fatalf("relay error = %v, want endpoint status", err)
	}
}
