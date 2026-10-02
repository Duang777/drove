package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSendInputPostsDataAndAcceptsNoContent(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.EscapedPath() != "/api/v1/agents/agent%2Fone/input" {
			t.Errorf("path = %q, want escaped agent path", r.URL.EscapedPath())
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content type = %q, want application/json", got)
		}
		var body inputRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body.Data != "continue\n" {
			t.Errorf("data = %q, want %q", body.Data, "continue\n")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c := New(strings.TrimPrefix(server.URL, "http://"))
	if err := c.SendInput(context.Background(), "agent/one", []byte("continue\n")); err != nil {
		t.Fatalf("send input: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func TestSendInputRejectsInvalidUTF8BeforeRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()

	c := New(strings.TrimPrefix(server.URL, "http://"))
	err := c.SendInput(context.Background(), "agent", []byte{0xff})
	if err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("send input error = %v, want UTF-8 error", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("calls = %d, want 0", calls.Load())
	}
}

func TestSendInputReturnsServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"session: agent is not attached to a PTY"}`, http.StatusConflict)
	}))
	defer server.Close()

	c := New(strings.TrimPrefix(server.URL, "http://"))
	err := c.SendInput(context.Background(), "agent", []byte("continue\n"))
	if err == nil || !strings.Contains(err.Error(), "409 Conflict") {
		t.Fatalf("send input error = %v, want 409 context", err)
	}
}
