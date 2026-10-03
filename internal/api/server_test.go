package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/session"
	"github.com/Duang777/drove/internal/store"
)

const testControlToken = "test-control-token"

func TestHandleCreateRejectsInvalidModeWithoutHistory(t *testing.T) {
	server, manager, st := newTestServer(t)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/agents",
		strings.NewReader(`{"vendor":"generic","command":"/bin/true","mode":"batch"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	serveAuthorized(server, rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), session.ErrInvalidMode.Error()) {
		t.Fatalf("body = %q, want invalid mode error", rec.Body.String())
	}
	if got := manager.List(); len(got) != 0 {
		t.Fatalf("manager retained %d agents, want 0", len(got))
	}
	lastSeq, err := st.LastSeq()
	if err != nil {
		t.Fatalf("last seq: %v", err)
	}
	if lastSeq != 0 {
		t.Fatalf("last seq = %d, want 0", lastSeq)
	}
}

func TestHandleCreateAcceptsLowercaseOneshotMode(t *testing.T) {
	server, _, _ := newTestServer(t)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/agents",
		strings.NewReader(`{"vendor":"generic","name":"api-agent","command":"/bin/cat","mode":"oneshot"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	serveAuthorized(server, rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var status session.Status
	if err := json.NewDecoder(rec.Body).Decode(&status); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if status.Mode != agent.RunModeOneshot || status.PID <= 0 {
		t.Fatalf("status = %+v, want live oneshot session", status)
	}
}

func TestHandleInputWritesAndAuditsWithoutContent(t *testing.T) {
	server, manager, _ := newTestServer(t)
	status, err := manager.Start(context.Background(), session.StartRequest{
		Name:    "input-agent",
		Command: "/bin/cat",
		Mode:    agent.RunModeInteractive,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/agents/"+status.AgentID+"/input",
		strings.NewReader(`{"data":"secret\n"}`),
	)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	rec := httptest.NewRecorder()
	serveAuthorized(server, rec, req)

	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("response = %d %q, want 204 with empty body", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		rows, replayErr := manager.Replay(status.AgentID)
		if replayErr != nil {
			t.Fatalf("replay: %v", replayErr)
		}
		hasAudit := false
		hasOutput := false
		for _, row := range rows {
			if row.Type == string(event.TypeAgentInput) {
				hasAudit = true
				if row.Payload != `{"version":1,"bytes":7}` || strings.Contains(row.Payload, "secret") {
					t.Fatalf("input audit = %+v", row)
				}
			}
			if row.Type == string(event.TypeOutput) && strings.Contains(row.Payload, "secret") {
				hasOutput = true
			}
		}
		if hasAudit && hasOutput {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("input audit or echoed output missing: %+v", rows)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHandleInputValidatesRequest(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        []byte
		wantStatus  int
	}{
		{
			name:       "missing content type",
			body:       []byte(`{"data":"input"}`),
			wantStatus: http.StatusUnsupportedMediaType,
		},
		{
			name:        "malformed JSON",
			contentType: "application/json",
			body:        []byte(`{"data":`),
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "unknown field",
			contentType: "application/json",
			body:        []byte(`{"data":"input","extra":true}`),
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "trailing JSON",
			contentType: "application/json",
			body:        []byte(`{"data":"input"} {}`),
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "invalid UTF-8",
			contentType: "application/json",
			body:        append([]byte(`{"data":"`), 0xff, '"', '}'),
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "empty input",
			contentType: "application/json",
			body:        []byte(`{"data":""}`),
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "oversized input",
			contentType: "application/json",
			body:        []byte(`{"data":"` + strings.Repeat("x", session.MaxInputBytes+1) + `"}`),
			wantStatus:  http.StatusRequestEntityTooLarge,
		},
		{
			name:        "unknown agent",
			contentType: "application/json",
			body:        []byte(`{"data":"input"}`),
			wantStatus:  http.StatusNotFound,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _, _ := newTestServer(t)
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/agents/missing/input",
				bytes.NewReader(test.body),
			)
			if test.contentType != "" {
				req.Header.Set("Content-Type", test.contentType)
			}
			rec := httptest.NewRecorder()
			serveAuthorized(server, rec, req)
			if rec.Code != test.wantStatus {
				t.Fatalf("response = %d %q, want %d", rec.Code, rec.Body.String(), test.wantStatus)
			}
		})
	}
}

func TestHandleInputRejectsDetachedAndClosedManager(t *testing.T) {
	t.Run("detached", func(t *testing.T) {
		server, manager, _ := newTestServer(t)
		status, err := manager.Start(context.Background(), session.StartRequest{
			Name:    "done-agent",
			Command: "/bin/sh",
			Args:    []string{"-c", "exit 0"},
			Mode:    agent.RunModeOneshot,
		})
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			current, statusErr := manager.Status(agent.ID(status.AgentID))
			if statusErr != nil {
				t.Fatalf("status: %v", statusErr)
			}
			if current.PID == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("agent remained attached: %+v", current)
			}
			time.Sleep(time.Millisecond)
		}

		req := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/agents/"+status.AgentID+"/input",
			strings.NewReader(`{"data":"input"}`),
		)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		serveAuthorized(server, rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("response = %d %q, want 409", rec.Code, rec.Body.String())
		}
	})

	t.Run("closed manager", func(t *testing.T) {
		server, manager, _ := newTestServer(t)
		if err := manager.Close(); err != nil {
			t.Fatalf("close manager: %v", err)
		}
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/agents/missing/input",
			strings.NewReader(`{"data":"input"}`),
		)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		serveAuthorized(server, rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("response = %d %q, want 503", rec.Code, rec.Body.String())
		}
	})
}

func TestSignalEndpointUsesSessionCredentialAndLoopbackOnly(t *testing.T) {
	server, manager, _ := newTestServer(t)
	tokenPath := filepath.Join(t.TempDir(), "signal.token")
	status, err := manager.Start(context.Background(), session.StartRequest{
		Vendor:  "claude",
		Command: "/bin/sh",
		Args: []string{
			"-c",
			`printf '%s' "$DROVE_SIGNAL_TOKEN" > "$1"; exec /bin/cat`,
			"sh",
			tokenPath,
		},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	token := waitForSignalTokenFile(t, tokenPath)
	body := `{
		"version":1,
		"vendor":"claude",
		"delivery_id":"550e8400-e29b-41d4-a716-446655440000",
		"payload":{
			"hook_event_name":"Elicitation",
			"session_id":"vendor-session"
		}
	}`

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/agents/"+status.AgentID+"/signal",
		strings.NewReader(body),
	)
	req.RemoteAddr = "127.0.0.1:43210"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("signal response = %d %q, want 204", rec.Code, rec.Body.String())
	}
	current, err := manager.Status(agent.ID(status.AgentID))
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if current.State != agent.StateBlocked {
		t.Fatalf("state = %s, want blocked", current.State)
	}

	for _, test := range []struct {
		name       string
		remoteAddr string
		token      string
		wantStatus int
	}{
		{
			name:       "control token is isolated",
			remoteAddr: "127.0.0.1:43210",
			token:      testControlToken,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "remote caller",
			remoteAddr: "192.0.2.10:43210",
			token:      token,
			wantStatus: http.StatusForbidden,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/agents/"+status.AgentID+"/signal",
				strings.NewReader(body),
			)
			request.RemoteAddr = test.remoteAddr
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+test.token)
			response := httptest.NewRecorder()
			server.mux.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf(
					"response = %d %q, want %d",
					response.Code,
					response.Body.String(),
					test.wantStatus,
				)
			}
		})
	}
}

func TestSignalEndpointStrictlyValidatesEnvelopeAndVendorPayload(t *testing.T) {
	server, manager, _ := newTestServer(t)
	tokenPath := filepath.Join(t.TempDir(), "signal.token")
	status, err := manager.Start(context.Background(), session.StartRequest{
		Vendor:  "claude",
		Command: "/bin/sh",
		Args: []string{
			"-c",
			`printf '%s' "$DROVE_SIGNAL_TOKEN" > "$1"; exec /bin/cat`,
			"sh",
			tokenPath,
		},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	token := waitForSignalTokenFile(t, tokenPath)

	tests := []struct {
		name        string
		contentType string
		body        []byte
		wantStatus  int
	}{
		{
			name:        "unknown envelope field",
			contentType: "application/json",
			body:        []byte(`{"version":1,"vendor":"claude","delivery_id":"d1","payload":{},"extra":true}`),
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "unknown version",
			contentType: "application/json",
			body:        []byte(`{"version":2,"vendor":"claude","delivery_id":"d1","payload":{}}`),
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "wrong vendor",
			contentType: "application/json",
			body:        []byte(`{"version":1,"vendor":"codex","delivery_id":"d1","payload":{"hook_event_name":"SessionStart","session_id":"s1"}}`),
			wantStatus:  http.StatusConflict,
		},
		{
			name:        "unknown hook event",
			contentType: "application/json",
			body:        []byte(`{"version":1,"vendor":"claude","delivery_id":"d1","payload":{"hook_event_name":"FutureEvent","session_id":"s1"}}`),
			wantStatus:  http.StatusUnprocessableEntity,
		},
		{
			name:        "invalid UTF-8",
			contentType: "application/json",
			body:        append([]byte(`{"version":1,"vendor":"claude","delivery_id":"`), 0xff, '"', '}'),
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "wrong content type",
			contentType: "text/plain",
			body:        []byte(`{}`),
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "oversized decoded payload",
			contentType: "application/json",
			body: []byte(
				`{"version":1,"vendor":"claude","delivery_id":"d1","payload":{"padding":"` +
					strings.Repeat("x", session.MaxSignalPayloadBytes) +
					`"}}`,
			),
			wantStatus: http.StatusRequestEntityTooLarge,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/agents/"+status.AgentID+"/signal",
				bytes.NewReader(test.body),
			)
			req.RemoteAddr = "127.0.0.1:43210"
			req.Header.Set("Content-Type", test.contentType)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			server.mux.ServeHTTP(rec, req)
			if rec.Code != test.wantStatus {
				t.Fatalf("response = %d %q, want %d", rec.Code, rec.Body.String(), test.wantStatus)
			}
		})
	}
}

func TestServerRequiresBearerAuthentication(t *testing.T) {
	server, _, _ := newTestServer(t)
	tests := []struct {
		name          string
		authorization []string
		wantStatus    int
	}{
		{name: "missing", wantStatus: http.StatusUnauthorized},
		{name: "wrong token", authorization: []string{"Bearer wrong"}, wantStatus: http.StatusUnauthorized},
		{
			name:          "duplicate headers",
			authorization: []string{"Bearer " + testControlToken, "Bearer " + testControlToken},
			wantStatus:    http.StatusUnauthorized,
		},
		{name: "valid", authorization: []string{"Bearer " + testControlToken}, wantStatus: http.StatusOK},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
			for _, value := range test.authorization {
				req.Header.Add("Authorization", value)
			}
			rec := httptest.NewRecorder()
			server.mux.ServeHTTP(rec, req)
			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, test.wantStatus, rec.Body.String())
			}
			if test.wantStatus == http.StatusUnauthorized &&
				rec.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatalf("WWW-Authenticate = %q, want Bearer", rec.Header().Get("WWW-Authenticate"))
			}
		})
	}
}

func TestWebSocketRequiresAllowedOrigin(t *testing.T) {
	server, _, _ := newTestServer(t)
	httpServer := httptest.NewServer(server.mux)
	defer httpServer.Close()
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws"

	tests := []struct {
		name       string
		origin     string
		wantStatus int
	}{
		{name: "remote", origin: "https://example.com", wantStatus: http.StatusForbidden},
		{name: "near match", origin: "http://localhost:5173/", wantStatus: http.StatusForbidden},
		{name: "allowed", origin: "http://localhost:5173"},
		{name: "absent"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header := http.Header{"Authorization": []string{"Bearer " + testControlToken}}
			if test.origin != "" {
				header.Set("Origin", test.origin)
			}
			conn, response, err := websocket.DefaultDialer.Dial(wsURL, header)
			if test.wantStatus != 0 {
				if err == nil {
					conn.Close()
					t.Fatal("WebSocket upgrade succeeded, want rejection")
				}
				if response == nil || response.StatusCode != test.wantStatus {
					t.Fatalf("response = %+v, want status %d", response, test.wantStatus)
				}
				response.Body.Close()
				return
			}
			if err != nil {
				t.Fatalf("WebSocket upgrade: %v", err)
			}
			defer conn.Close()
			_, payload, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("read hello: %v", err)
			}
			if string(payload) != `{"type":"hello"}` {
				t.Fatalf("hello = %s", payload)
			}
		})
	}
}

func newTestServer(t *testing.T) (*Server, *session.Manager, *store.Store) {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "drove.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	hub := event.NewHub(0)
	manager := session.NewManager(
		adapter.NewRegistry(),
		hub,
		st,
		0,
	)
	signalOrigin, err := url.Parse("http://127.0.0.1:7373")
	if err != nil {
		t.Fatalf("parse signal origin: %v", err)
	}
	if err := manager.ConfigureSignalOrigin(signalOrigin); err != nil {
		t.Fatalf("configure signal origin: %v", err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
		hub.Close()
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return NewServer(ServerOptions{
		Manager:        manager,
		Hub:            hub,
		ControlToken:   testControlToken,
		AllowedOrigins: []string{"http://localhost:5173"},
	}), manager, st
}

func waitForSignalTokenFile(t *testing.T, path string) string {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err == nil && len(raw) > 0 {
			return string(raw)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read signal token file: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("signal token file %q missing", path)
		}
		time.Sleep(time.Millisecond)
	}
}

func serveAuthorized(server *Server, rec *httptest.ResponseRecorder, req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+testControlToken)
	server.mux.ServeHTTP(rec, req)
}
