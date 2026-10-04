package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/Duang777/drove/internal/recording"
	"github.com/Duang777/drove/internal/session"
	"github.com/Duang777/drove/internal/store"
)

const (
	testControlToken     = "test-control-token"
	testSignalDeliveryID = "550e8400-e29b-41d4-a716-446655440000"
)

func TestHandleCreateMapsHookConfigurationErrors(t *testing.T) {
	tests := []struct {
		name            string
		configureOrigin bool
		body            string
		wantStatus      int
	}{
		{
			name:            "invalid policy",
			configureOrigin: true,
			body:            `{"vendor":"generic","command":"/bin/true","hooks":"sometimes"}`,
			wantStatus:      http.StatusBadRequest,
		},
		{
			name:            "required unsupported",
			configureOrigin: true,
			body:            `{"vendor":"generic","command":"/bin/true","hooks":"required"}`,
			wantStatus:      http.StatusBadRequest,
		},
		{
			name:       "signal origin unavailable",
			body:       `{"vendor":"claude","command":"/bin/true","hooks":"auto"}`,
			wantStatus: http.StatusServiceUnavailable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _, _ := newTestServerWithSignalOrigin(t, test.configureOrigin)
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/agents",
				strings.NewReader(test.body),
			)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			serveAuthorized(server, rec, req)
			if rec.Code != test.wantStatus {
				t.Fatalf(
					"response = %d %q, want %d",
					rec.Code,
					rec.Body.String(),
					test.wantStatus,
				)
			}
		})
	}
}

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

func TestHandleExplainReturnsAttachedTypedResponse(t *testing.T) {
	server, manager, _ := newTestServer(t)
	status, err := manager.Start(context.Background(), session.StartRequest{
		Name:    "explain-attached",
		Command: "/bin/cat",
		Mode:    agent.RunModeInteractive,
		Hooks:   agent.HooksOff,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := manager.SendInput(agent.ID(status.AgentID), []byte("screen\n")); err != nil {
		t.Fatalf("send input: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		explanation, explainErr := manager.Explain(
			context.Background(),
			agent.ID(status.AgentID),
			session.ExplainOptions{},
		)
		if explainErr != nil {
			t.Fatalf("wait for screen: %v", explainErr)
		}
		if explanation.Screen != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("attached screen was not sampled")
		}
		time.Sleep(time.Millisecond)
	}

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/agents/"+status.AgentID+"/explain?limit=1",
		nil,
	)
	rec := httptest.NewRecorder()
	serveAuthorized(server, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("response = %d %q, want 200", rec.Code, rec.Body.String())
	}
	var explanation session.Explanation
	if err := json.NewDecoder(rec.Body).Decode(&explanation); err != nil {
		t.Fatalf("decode explanation: %v", err)
	}
	if explanation.AgentID != status.AgentID ||
		!explanation.Attached ||
		explanation.HookStatus != "off" ||
		len(explanation.Events) != 1 ||
		explanation.Screen == nil ||
		len(explanation.Screen.Rows) == 0 {
		t.Fatalf("explanation = %+v", explanation)
	}
}

func TestHandleExplainReturnsDetachedHistoryAndUnknownVersion(t *testing.T) {
	server, manager, st := newTestServer(t)
	status, err := manager.Start(context.Background(), session.StartRequest{
		Name:    "explain-detached",
		Command: "/bin/sh",
		Args:    []string{"-c", "exit 0"},
		Mode:    agent.RunModeOneshot,
		Hooks:   agent.HooksOff,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForDetachedAPIStatus(t, manager, agent.ID(status.AgentID))
	lastSeq, err := st.LastSeq()
	if err != nil {
		t.Fatalf("last seq: %v", err)
	}
	if err := st.AppendEvent(store.EventRow{
		Seq:       lastSeq + 1,
		Timestamp: time.Now().UTC(),
		Type:      string(event.TypeAgentSignal),
		SessionID: status.AgentID,
		AgentID:   status.AgentID,
		Payload:   `{"version":91,"secret":"not-forwarded"}`,
	}); err != nil {
		t.Fatalf("append unknown signal: %v", err)
	}

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/agents/"+status.AgentID+"/explain",
		nil,
	)
	rec := httptest.NewRecorder()
	serveAuthorized(server, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("response = %d %q, want 200", rec.Code, rec.Body.String())
	}
	var explanation session.Explanation
	if err := json.NewDecoder(rec.Body).Decode(&explanation); err != nil {
		t.Fatalf("decode explanation: %v", err)
	}
	last := explanation.Events[len(explanation.Events)-1]
	if explanation.Attached ||
		explanation.Screen != nil ||
		explanation.HookStatus != "detached" ||
		last.UnsupportedVersion == nil ||
		*last.UnsupportedVersion != 91 ||
		strings.Contains(rec.Body.String(), "not-forwarded") {
		t.Fatalf("detached explanation = %+v; body=%s", explanation, rec.Body.String())
	}
}

func TestHandleTimelineBlockedAndFrameContracts(t *testing.T) {
	server, _, st := newTestServer(t)
	base := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	rows := []store.EventRow{
		{
			Seq: 1, Timestamp: base, Type: string(event.TypeSessionLifecycle),
			SessionID: "agent-1", AgentID: "agent-1", Reason: "created",
		},
		{
			Seq: 2, Timestamp: base.Add(time.Second), Type: string(event.TypeStateChanged),
			SessionID: "agent-1", AgentID: "agent-1",
			From: string(agent.StatePending), To: string(agent.StateStarting),
			Reason:  "starting",
			Payload: `{"version":1,"source":"session","event":"session_start","confidence":1}`,
		},
		{
			Seq: 3, Timestamp: base.Add(2 * time.Second), Type: string(event.TypeStateChanged),
			SessionID: "agent-1", AgentID: "agent-1",
			From: string(agent.StateStarting), To: string(agent.StateWorking),
			Reason:  "working",
			Payload: `{"version":1,"source":"process","event":"process_started","confidence":1}`,
		},
		{
			Seq: 4, Timestamp: base.Add(5 * time.Second), Type: string(event.TypeOutputChunk),
			SessionID: "agent-1", AgentID: "agent-1",
			Payload:          `{"version":1,"offset":0,"len":5}`,
			OutputAttachment: []byte("hello"),
		},
		{
			Seq: 5, Timestamp: base.Add(40 * time.Second), Type: string(event.TypeStateChanged),
			SessionID: "agent-1", AgentID: "agent-1",
			From: string(agent.StateWorking), To: string(agent.StateBlocked),
			Reason:  "waiting",
			Payload: `{"version":1,"source":"timer","event":"blocked_timeout","confidence":1}`,
		},
	}
	if _, err := st.AppendEvents(context.Background(), 0, rows); err != nil {
		t.Fatalf("append timeline fixture: %v", err)
	}

	timelineResponse := httptest.NewRecorder()
	serveAuthorized(
		server,
		timelineResponse,
		httptest.NewRequest(http.MethodGet, "/api/v1/agents/agent-1/timeline", nil),
	)
	if timelineResponse.Code != http.StatusOK {
		t.Fatalf(
			"timeline response = %d %q",
			timelineResponse.Code,
			timelineResponse.Body.String(),
		)
	}
	var timeline recording.Timeline
	if err := json.NewDecoder(timelineResponse.Body).Decode(&timeline); err != nil {
		t.Fatalf("decode timeline: %v", err)
	}
	if timeline.Captured != (recording.Cursor{Seq: 5, NextOffset: 5}) ||
		len(timeline.Blocked) != 1 {
		t.Fatalf("timeline = %+v", timeline)
	}

	blockedResponse := httptest.NewRecorder()
	serveAuthorized(
		server,
		blockedResponse,
		httptest.NewRequest(
			http.MethodGet,
			"/api/v1/agents/agent-1/timeline/blocked/1",
			nil,
		),
	)
	if blockedResponse.Code != http.StatusOK {
		t.Fatalf(
			"blocked response = %d %q",
			blockedResponse.Code,
			blockedResponse.Body.String(),
		)
	}
	var blocked recording.BlockedOccurrence
	if err := json.NewDecoder(blockedResponse.Body).Decode(&blocked); err != nil {
		t.Fatalf("decode blocked occurrence: %v", err)
	}
	if blocked.Number != 1 ||
		blocked.Jump != (recording.Cursor{Seq: 4, NextOffset: 5}) {
		t.Fatalf("blocked occurrence = %+v", blocked)
	}

	frameResponse := httptest.NewRecorder()
	serveAuthorized(
		server,
		frameResponse,
		httptest.NewRequest(http.MethodGet, "/api/v1/agents/agent-1/frame?seq=4", nil),
	)
	if frameResponse.Code != http.StatusOK {
		t.Fatalf(
			"frame response = %d %q",
			frameResponse.Code,
			frameResponse.Body.String(),
		)
	}
	var frame recording.Frame
	if err := json.NewDecoder(frameResponse.Body).Decode(&frame); err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	if frame.Cursor != (recording.Cursor{Seq: 4, NextOffset: 5}) ||
		len(frame.Lines) != 1 ||
		frame.Lines[0] != "hello" {
		t.Fatalf("frame = %+v", frame)
	}

	if _, err := st.PruneOutputAttachments(
		context.Background(),
		base.Add(10*time.Second),
	); err != nil {
		t.Fatalf("prune output: %v", err)
	}
	expiredResponse := httptest.NewRecorder()
	serveAuthorized(
		server,
		expiredResponse,
		httptest.NewRequest(http.MethodGet, "/api/v1/agents/agent-1/frame?seq=5", nil),
	)
	if expiredResponse.Code != http.StatusGone {
		t.Fatalf(
			"expired response = %d %q",
			expiredResponse.Code,
			expiredResponse.Body.String(),
		)
	}
	var expired struct {
		Code      string                  `json:"code"`
		SessionID string                  `json:"session_id"`
		Missing   []recording.OutputRange `json:"missing"`
	}
	if err := json.NewDecoder(expiredResponse.Body).Decode(&expired); err != nil {
		t.Fatalf("decode expiry response: %v", err)
	}
	if expired.Code != "output_expired" ||
		expired.SessionID != "agent-1" ||
		len(expired.Missing) != 1 ||
		expired.Missing[0] != (recording.OutputRange{Start: 0, End: 5}) {
		t.Fatalf("expiry response = %+v", expired)
	}
}

func TestHandleTimelineAndFrameMapLookupAndSelectorErrors(t *testing.T) {
	server, _, st := newTestServer(t)
	base := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	if err := st.AppendEvent(store.EventRow{
		Seq: 1, Timestamp: base, Type: string(event.TypeSessionLifecycle),
		SessionID: "agent-1", AgentID: "agent-1", Reason: "created",
	}); err != nil {
		t.Fatalf("append session: %v", err)
	}

	for _, path := range []string{
		"/api/v1/agents/missing/timeline",
		"/api/v1/agents/missing/frame?seq=0",
		"/api/v1/agents/agent-1/timeline/blocked/1",
	} {
		response := httptest.NewRecorder()
		serveAuthorized(
			server,
			response,
			httptest.NewRequest(http.MethodGet, path, nil),
		)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s response = %d %q, want 404", path, response.Code, response.Body.String())
		}
	}

	for _, path := range []string{
		"/api/v1/agents/agent-1/frame",
		"/api/v1/agents/agent-1/frame?seq=0&offset=0",
		"/api/v1/agents/agent-1/frame?seq=01",
		"/api/v1/agents/agent-1/frame?at=not-a-time",
		"/api/v1/agents/agent-1/frame?offset=0&extra=1",
		"/api/v1/agents/agent-1/timeline/blocked/0",
	} {
		response := httptest.NewRecorder()
		serveAuthorized(
			server,
			response,
			httptest.NewRequest(http.MethodGet, path, nil),
		)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s response = %d %q, want 400", path, response.Code, response.Body.String())
		}
	}

	if _, err := st.AppendEvents(context.Background(), 1, []store.EventRow{
		{
			Seq: 2, Timestamp: base, Type: string(event.TypeSessionLifecycle),
			SessionID: "corrupt", AgentID: "corrupt", Reason: "created",
		},
		{
			Seq: 3, Timestamp: base.Add(time.Second), Type: string(event.TypeOutputChunk),
			SessionID: "corrupt", AgentID: "corrupt",
			Payload:          `{"version":1,"offset":0,"len":2}`,
			OutputAttachment: []byte("bad"),
		},
	}); err != nil {
		t.Fatalf("append corrupt recording: %v", err)
	}
	corruptResponse := httptest.NewRecorder()
	serveAuthorized(
		server,
		corruptResponse,
		httptest.NewRequest(http.MethodGet, "/api/v1/agents/corrupt/frame?seq=3", nil),
	)
	if corruptResponse.Code != http.StatusInternalServerError {
		t.Fatalf(
			"corrupt frame response = %d %q, want 500",
			corruptResponse.Code,
			corruptResponse.Body.String(),
		)
	}
}

func TestHandleExplainValidatesLimitAgentAndAuthentication(t *testing.T) {
	server, _, _ := newTestServer(t)
	for _, rawQuery := range []string{
		"limit=0",
		"limit=-1",
		"limit=word",
		"limit=",
		"limit=1&limit=2",
		"other=1",
		fmt.Sprintf("limit=%d", session.MaxExplainLimit+1),
	} {
		req := httptest.NewRequest(
			http.MethodGet,
			"/api/v1/agents/missing/explain?"+rawQuery,
			nil,
		)
		rec := httptest.NewRecorder()
		serveAuthorized(server, rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf(
				"query %q response = %d %q, want 400",
				rawQuery,
				rec.Code,
				rec.Body.String(),
			)
		}
	}

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/agents/missing/explain?limit=1",
		nil,
	)
	rec := httptest.NewRecorder()
	serveAuthorized(server, rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown agent response = %d %q, want 404", rec.Code, rec.Body.String())
	}

	unauthorized := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/agents/missing/explain",
		nil,
	)
	unauthorizedResponse := httptest.NewRecorder()
	server.mux.ServeHTTP(unauthorizedResponse, unauthorized)
	if unauthorizedResponse.Code != http.StatusUnauthorized {
		t.Fatalf(
			"unauthorized response = %d %q, want 401",
			unauthorizedResponse.Code,
			unauthorizedResponse.Body.String(),
		)
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
			if row.Type == string(event.TypeOutputChunk) &&
				outputPayloadContains(t, row.Payload, "secret") {
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

func outputPayloadContains(t *testing.T, payload, text string) bool {
	t.Helper()

	decoded, err := event.DecodeOutputChunkPayload(payload)
	if err != nil {
		t.Fatalf("decode output chunk: %v", err)
	}
	data, err := decoded.DecodeData()
	if err != nil {
		t.Fatalf("decode output data: %v", err)
	}
	return strings.Contains(string(data), text)
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
	duplicate := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/agents/"+status.AgentID+"/signal",
		strings.NewReader(body),
	)
	duplicate.RemoteAddr = "127.0.0.1:43210"
	duplicate.Header.Set("Content-Type", "application/json")
	duplicate.Header.Set("Authorization", "Bearer "+token)
	duplicateResponse := httptest.NewRecorder()
	server.mux.ServeHTTP(duplicateResponse, duplicate)
	if duplicateResponse.Code != http.StatusNoContent {
		t.Fatalf(
			"duplicate response = %d %q, want 204",
			duplicateResponse.Code,
			duplicateResponse.Body.String(),
		)
	}

	for _, test := range []struct {
		name       string
		remoteAddr string
		token      string
		wantStatus int
	}{
		{
			name:       "missing token",
			remoteAddr: "127.0.0.1:43210",
			wantStatus: http.StatusUnauthorized,
		},
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
			body: []byte(
				`{"version":1,"vendor":"codex","delivery_id":"` +
					testSignalDeliveryID +
					`","payload":{"hook_event_name":"SessionStart","session_id":"s1"}}`,
			),
			wantStatus: http.StatusConflict,
		},
		{
			name:        "unknown hook event",
			contentType: "application/json",
			body: []byte(
				`{"version":1,"vendor":"claude","delivery_id":"` +
					testSignalDeliveryID +
					`","payload":{"hook_event_name":"FutureEvent","session_id":"s1"}}`,
			),
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:        "invalid delivery ID",
			contentType: "application/json",
			body:        []byte(`{"version":1,"vendor":"claude","delivery_id":"d1","payload":{}}`),
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "non-object payload",
			contentType: "application/json",
			body: []byte(
				`{"version":1,"vendor":"claude","delivery_id":"` +
					testSignalDeliveryID +
					`","payload":[]}`,
			),
			wantStatus: http.StatusBadRequest,
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
			wantStatus:  http.StatusBadRequest,
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
		{
			name:        "oversized envelope",
			contentType: "application/json",
			body:        bytes.Repeat([]byte(" "), maxSignalRequestBytes+1),
			wantStatus:  http.StatusRequestEntityTooLarge,
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

func TestWriteSignalErrorMapsSessionSentinels(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantHeader string
	}{
		{
			name:       "unauthorized",
			err:        session.ErrHookUnauthorized,
			wantStatus: http.StatusUnauthorized,
			wantHeader: "Bearer",
		},
		{name: "unknown agent", err: session.ErrUnknownAgent, wantStatus: http.StatusNotFound},
		{name: "hooks disabled", err: session.ErrHookDisabled, wantStatus: http.StatusConflict},
		{name: "hooks unsupported", err: session.ErrHookUnsupported, wantStatus: http.StatusConflict},
		{name: "vendor mismatch", err: session.ErrHookVendorMismatch, wantStatus: http.StatusConflict},
		{name: "detached", err: session.ErrHookDetached, wantStatus: http.StatusGone},
		{
			name:       "backpressure",
			err:        session.ErrHookBackpressure,
			wantStatus: http.StatusTooManyRequests,
			wantHeader: "1",
		},
		{name: "manager closed", err: session.ErrManagerClosed, wantStatus: http.StatusServiceUnavailable},
		{
			name:       "committer unavailable",
			err:        session.ErrEventCommitterUnavailable,
			wantStatus: http.StatusServiceUnavailable,
		},
		{name: "invalid hook", err: session.ErrHookInvalid, wantStatus: http.StatusUnprocessableEntity},
		{name: "internal", err: errors.New("internal"), wantStatus: http.StatusInternalServerError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeSignalError(rec, fmt.Errorf("wrapped: %w", test.err))
			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, test.wantStatus)
			}
			switch test.wantStatus {
			case http.StatusUnauthorized:
				if got := rec.Header().Get("WWW-Authenticate"); got != test.wantHeader {
					t.Fatalf("WWW-Authenticate = %q, want %q", got, test.wantHeader)
				}
				if strings.Contains(rec.Body.String(), "wrapped") {
					t.Fatalf("unauthorized response exposed error: %q", rec.Body.String())
				}
			case http.StatusTooManyRequests:
				if got := rec.Header().Get("Retry-After"); got != test.wantHeader {
					t.Fatalf("Retry-After = %q, want %q", got, test.wantHeader)
				}
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
	return newTestServerWithSignalOrigin(t, true)
}

func newTestServerWithSignalOrigin(
	t *testing.T,
	configureOrigin bool,
) (*Server, *session.Manager, *store.Store) {
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
	if configureOrigin {
		signalOrigin, err := url.Parse("http://127.0.0.1:7373")
		if err != nil {
			t.Fatalf("parse signal origin: %v", err)
		}
		if err := manager.ConfigureSignalOrigin(signalOrigin); err != nil {
			t.Fatalf("configure signal origin: %v", err)
		}
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

func waitForDetachedAPIStatus(
	t *testing.T,
	manager *session.Manager,
	id agent.ID,
) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		status, err := manager.Status(id)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if status.PID == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent remained attached: %+v", status)
		}
		time.Sleep(time.Millisecond)
	}
}

func serveAuthorized(server *Server, rec *httptest.ResponseRecorder, req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+testControlToken)
	server.mux.ServeHTTP(rec, req)
}
