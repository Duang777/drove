package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestHookRelayPostsVersionedEnvelopeWithSessionToken(t *testing.T) {
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

	relay, err := NewHookRelay(HookRelayConfig{
		AgentID:   "agent-1",
		SignalURL: server.URL + "/api/v1/agents/agent-1/signal",
		Token:     "session-token",
	})
	if err != nil {
		t.Fatalf("new relay: %v", err)
	}
	payload := []byte(`{"hook_event_name":"SessionStart","session_id":"vendor-session"}`)
	if err := relay.Forward(context.Background(), "claude", payload); err != nil {
		t.Fatalf("forward hook: %v", err)
	}

	deliveryID, err := uuid.Parse(received.DeliveryID)
	if err != nil || deliveryID.String() != received.DeliveryID {
		t.Fatalf("delivery ID = %q, parse error = %v", received.DeliveryID, err)
	}
	if received.Version != 1 ||
		received.Vendor != "claude" ||
		string(received.Payload) != string(payload) {
		t.Fatalf("request = %+v", received)
	}
}

func TestHookRelayPreservesInLimitPayloadSize(t *testing.T) {
	prefix := `{"hook_event_name":"SessionStart","session_id":"vendor-session","padding":"`
	suffix := `"}`
	payload := []byte(prefix + strings.Repeat(
		"<",
		MaxHookPayloadBytes-len(prefix)-len(suffix),
	) + suffix)

	var requestBodyBytes int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestBodyBytes = r.ContentLength
		var received signalRequest
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if len(received.Payload) != len(payload) {
			t.Errorf("payload size = %d, want %d", len(received.Payload), len(payload))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	relay, err := NewHookRelay(HookRelayConfig{
		AgentID:   "agent-1",
		SignalURL: server.URL + "/api/v1/agents/agent-1/signal",
		Token:     "session-token",
	})
	if err != nil {
		t.Fatalf("new relay: %v", err)
	}
	if err := relay.Forward(context.Background(), "claude", payload); err != nil {
		t.Fatalf("forward hook: %v", err)
	}
	if requestBodyBytes > MaxHookPayloadBytes+4096 {
		t.Fatalf("request body size = %d, exceeds endpoint limit", requestBodyBytes)
	}
}

func TestNewHookRelayRejectsUnsafeOrMismatchedConfiguration(t *testing.T) {
	tests := []HookRelayConfig{
		{
			AgentID:   "agent-1",
			SignalURL: "https://127.0.0.1:7373/api/v1/agents/agent-1/signal",
			Token:     "token",
		},
		{
			AgentID:   "agent-1",
			SignalURL: "http://example.com/api/v1/agents/agent-1/signal",
			Token:     "token",
		},
		{
			AgentID:   "agent-1",
			SignalURL: "http://user@127.0.0.1:7373/api/v1/agents/agent-1/signal",
			Token:     "token",
		},
		{
			AgentID:   "agent-1",
			SignalURL: "http://127.0.0.1:7373/api/v1/agents/agent-1/signal?retry=1",
			Token:     "token",
		},
		{
			AgentID:   "agent-1",
			SignalURL: "http://127.0.0.1:7373/api/v1/agents/agent-1/signal#fragment",
			Token:     "token",
		},
		{
			AgentID:   "agent-1",
			SignalURL: "http://127.0.0.1:7373/api/v1/agents/other/signal",
			Token:     "token",
		},
		{
			AgentID:   "agent-1",
			SignalURL: "http://127.0.0.1:7373/api/v1/agents/%61gent-1/signal",
			Token:     "token",
		},
		{
			SignalURL: "http://127.0.0.1:7373/api/v1/agents/agent-1/signal",
			Token:     "token",
		},
		{
			AgentID:   "agent-1",
			SignalURL: "http://127.0.0.1:7373/api/v1/agents/agent-1/signal",
		},
	}

	for index, config := range tests {
		if _, err := NewHookRelay(config); err == nil {
			t.Fatalf("config %d succeeded: %+v", index, config)
		}
	}
}

func TestHookRelayRejectsInvalidPayloadBeforeRequest(t *testing.T) {
	relay, err := NewHookRelay(HookRelayConfig{
		AgentID:   "agent-1",
		SignalURL: "http://127.0.0.1:7373/api/v1/agents/agent-1/signal",
		Token:     "token",
	})
	if err != nil {
		t.Fatalf("new relay: %v", err)
	}
	relay.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid payload reached transport")
		return nil, nil
	})

	for _, test := range []struct {
		name    string
		vendor  string
		payload []byte
	}{
		{name: "missing vendor", payload: []byte(`{}`)},
		{name: "empty", vendor: "claude"},
		{name: "too large", vendor: "claude", payload: []byte(strings.Repeat("x", MaxHookPayloadBytes+1))},
		{name: "invalid UTF-8", vendor: "claude", payload: []byte{0xff}},
		{name: "array", vendor: "claude", payload: []byte(`[]`)},
		{name: "null", vendor: "claude", payload: []byte(`null`)},
		{name: "trailing JSON", vendor: "claude", payload: []byte(`{} {}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := relay.Forward(context.Background(), test.vendor, test.payload); err == nil {
				t.Fatal("forward succeeded, want validation error")
			}
		})
	}
}

func TestHookRelayRetriesTransientFailureWithSameDeliveryID(t *testing.T) {
	tests := []struct {
		name       string
		firstCode  int
		firstError error
	}{
		{name: "network", firstError: errors.New("network unavailable")},
		{name: "too many requests", firstCode: http.StatusTooManyRequests},
		{name: "service unavailable", firstCode: http.StatusServiceUnavailable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			relay := newTestHookRelay(t)
			var requests []signalRequest
			relay.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				deadline, ok := r.Context().Deadline()
				if !ok {
					t.Fatal("request has no deadline")
				}
				if remaining := time.Until(deadline); remaining < hookAttemptTimeout-100*time.Millisecond ||
					remaining > hookAttemptTimeout {
					t.Fatalf("request deadline in %s, want %s", remaining, hookAttemptTimeout)
				}
				var request signalRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				requests = append(requests, request)
				if len(requests) == 1 {
					if test.firstError != nil {
						return nil, test.firstError
					}
					return hookResponse(test.firstCode, ""), nil
				}
				return hookResponse(http.StatusNoContent, ""), nil
			})

			startedAt := time.Now()
			if err := relay.Forward(context.Background(), "claude", []byte(`{}`)); err != nil {
				t.Fatalf("forward hook: %v", err)
			}
			if len(requests) != 2 {
				t.Fatalf("request count = %d, want 2", len(requests))
			}
			if requests[0].DeliveryID != requests[1].DeliveryID {
				t.Fatalf(
					"delivery IDs = %q and %q",
					requests[0].DeliveryID,
					requests[1].DeliveryID,
				)
			}
			if elapsed := time.Since(startedAt); elapsed < hookRetryDelay {
				t.Fatalf("retry delay = %s, want at least %s", elapsed, hookRetryDelay)
			}
		})
	}
}

func TestHookRelayUsesPerAttemptTimeout(t *testing.T) {
	relay := newTestHookRelay(t)
	var attempts int
	relay.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		deadline, ok := r.Context().Deadline()
		if !ok {
			t.Fatal("request has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining < hookAttemptTimeout-100*time.Millisecond ||
			remaining > hookAttemptTimeout {
			t.Fatalf("request deadline in %s, want %s", remaining, hookAttemptTimeout)
		}
		return hookResponse(http.StatusNoContent, ""), nil
	})

	if err := relay.Forward(context.Background(), "claude", []byte(`{}`)); err != nil {
		t.Fatalf("forward hook: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestHookRelayDoesNotRetryPermanentFailureOrExposeBody(t *testing.T) {
	relay := newTestHookRelay(t)
	var requests int
	relay.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return hookResponse(http.StatusUnauthorized, "secret response"), nil
	})

	err := relay.Forward(context.Background(), "codex", []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "401 Unauthorized") {
		t.Fatalf("forward error = %v, want endpoint status", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("forward error exposed response body: %v", err)
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want 1", requests)
	}
}

func TestHookRelayDoesNotFollowRedirects(t *testing.T) {
	var redirectedRequests int
	redirected := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedRequests++
	}))
	defer redirected.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", redirected.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	relay, err := NewHookRelay(HookRelayConfig{
		AgentID:   "agent-1",
		SignalURL: source.URL + "/api/v1/agents/agent-1/signal",
		Token:     "session-token",
	})
	if err != nil {
		t.Fatalf("new relay: %v", err)
	}
	err = relay.Forward(context.Background(), "claude", []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "307 Temporary Redirect") {
		t.Fatalf("forward error = %v, want redirect status", err)
	}
	if redirectedRequests != 0 {
		t.Fatalf("followed redirect with %d requests", redirectedRequests)
	}
}

func newTestHookRelay(t *testing.T) *HookRelay {
	t.Helper()
	relay, err := NewHookRelay(HookRelayConfig{
		AgentID:   "agent-1",
		SignalURL: "http://127.0.0.1:7373/api/v1/agents/agent-1/signal",
		Token:     "session-token",
	})
	if err != nil {
		t.Fatalf("new relay: %v", err)
	}
	return relay
}

func hookResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmtStatus(status),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func fmtStatus(status int) string {
	return fmt.Sprintf("%d %s", status, http.StatusText(status))
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
