package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/session"
	"github.com/Duang777/drove/internal/store"
)

func TestHandleCreateRejectsInvalidModeWithoutHistory(t *testing.T) {
	server, manager, st := newTestServer(t)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/agents",
		strings.NewReader(`{"vendor":"generic","command":"/bin/true","mode":"batch"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, req)

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
	server.mux.ServeHTTP(rec, req)

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
	server.mux.ServeHTTP(rec, req)

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
			server.mux.ServeHTTP(rec, req)
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
		server.mux.ServeHTTP(rec, req)
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
		server.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("response = %d %q, want 503", rec.Code, rec.Body.String())
		}
	})
}

func newTestServer(t *testing.T) (*Server, *session.Manager, *store.Store) {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "drove.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	hub := event.NewHub(0)
	manager := session.NewManager(adapter.NewRegistry(), hub, st)
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
		hub.Close()
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return NewServer(ServerOptions{Manager: manager, Hub: hub}), manager, st
}
