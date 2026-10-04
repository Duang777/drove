package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/auth"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/session"
)

func TestWebSocketInputAcknowledgesAndPreservesEventEnvelope(t *testing.T) {
	server, manager, _ := newTestServer(t)
	status, err := manager.Start(context.Background(), session.StartRequest{
		Name:    "websocket-input",
		Command: "/bin/cat",
		Mode:    agent.RunModeInteractive,
	})
	if err != nil {
		t.Fatalf("start agent: %v", err)
	}

	httpServer := httptest.NewServer(server.mux)
	defer httpServer.Close()
	conn := dialTestWebSocket(t, httpServer.URL)
	defer conn.Close()
	pong := make(chan string, 1)
	conn.SetPongHandler(func(payload string) error {
		pong <- payload
		return nil
	})
	if err := conn.WriteControl(
		websocket.PingMessage,
		[]byte("probe"),
		time.Now().Add(time.Second),
	); err != nil {
		t.Fatalf("write ping: %v", err)
	}

	request := webSocketInput{
		Version:   webSocketProtocolVersion,
		Type:      "input",
		RequestID: "request-1",
		AgentID:   status.AgentID,
		Data:      "continue\n",
	}
	if err := conn.WriteJSON(request); err != nil {
		t.Fatalf("write input: %v", err)
	}

	var ack map[string]any
	var output map[string]any
	deadline := time.Now().Add(3 * time.Second)
	for ack == nil || output == nil {
		message := readWebSocketObject(t, conn, deadline)
		switch message["type"] {
		case "ack":
			if message["request_id"] == request.RequestID {
				ack = message
			}
		case string(event.TypeOutputChunk):
			if message["agent_id"] == status.AgentID &&
				outputPayloadContains(t, message["payload"].(string), "continue") {
				output = message
			}
		}
	}

	if ack["version"] != float64(1) ||
		ack["bytes"] != float64(len(request.Data)) ||
		ack["request_id"] != request.RequestID {
		t.Fatalf("ack = %#v", ack)
	}
	if _, exists := output["version"]; exists {
		t.Fatalf("event envelope gained protocol version: %#v", output)
	}
	if output["seq"].(float64) <= 0 {
		t.Fatalf("event sequence = %#v, want positive", output["seq"])
	}
	select {
	case payload := <-pong:
		if payload != "probe" {
			t.Fatalf("pong payload = %q, want probe", payload)
		}
	default:
		t.Fatal("server did not return queued pong")
	}

	deadline = time.Now().Add(2 * time.Second)
	for {
		rows, replayErr := manager.Replay(status.AgentID)
		if replayErr != nil {
			t.Fatalf("replay: %v", replayErr)
		}
		found := false
		for _, row := range rows {
			if row.Type == string(event.TypeAgentInput) {
				found = true
				if row.Payload != `{"version":1,"bytes":9}` ||
					strings.Contains(row.Payload, "continue") {
					t.Fatalf("input audit = %+v", row)
				}
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("input audit did not become replayable")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWebSocketInputReturnsStableErrorsAndRejectsDuplicateIDs(t *testing.T) {
	server, _, _ := newTestServer(t)
	httpServer := httptest.NewServer(server.mux)
	defer httpServer.Close()
	conn := dialTestWebSocket(t, httpServer.URL)
	defer conn.Close()

	requests := []struct {
		message   any
		requestID string
		wantCode  string
	}{
		{
			message: webSocketInput{
				Version:   1,
				Type:      "input",
				RequestID: "empty-1",
				AgentID:   "missing",
			},
			requestID: "empty-1",
			wantCode:  "empty_input",
		},
		{
			message: webSocketInput{
				Version:   1,
				Type:      "input",
				RequestID: "unknown-1",
				AgentID:   "missing",
				Data:      "continue\n",
			},
			requestID: "unknown-1",
			wantCode:  "unknown_agent",
		},
		{
			message: webSocketInput{
				Version:   1,
				Type:      "input",
				RequestID: "large-1",
				AgentID:   "missing",
				Data:      strings.Repeat("x", session.MaxInputBytes+1),
			},
			requestID: "large-1",
			wantCode:  "input_too_large",
		},
		{
			message: webSocketInput{
				Version:   1,
				Type:      "input",
				RequestID: "unknown-1",
				AgentID:   "missing",
				Data:      "retry\n",
			},
			requestID: "unknown-1",
			wantCode:  "duplicate_request_id",
		},
	}

	for _, request := range requests {
		if err := conn.WriteJSON(request.message); err != nil {
			t.Fatalf("write %s: %v", request.requestID, err)
		}
		response := readWebSocketType(t, conn, "error", time.Now().Add(3*time.Second))
		if response["version"] != float64(1) ||
			response["request_id"] != request.requestID ||
			response["code"] != request.wantCode {
			t.Fatalf("response = %#v, want request %q code %q", response, request.requestID, request.wantCode)
		}
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte("binary")); err != nil {
		t.Fatalf("write binary message: %v", err)
	}
	response := readWebSocketType(t, conn, "error", time.Now().Add(3*time.Second))
	if response["code"] != "invalid_message" {
		t.Fatalf("binary response = %#v", response)
	}
}

func TestWebSocketInputReturnsBackpressureForBlockedPTY(t *testing.T) {
	server, manager, _ := newTestServer(t)
	status, err := manager.Start(context.Background(), session.StartRequest{
		Name:    "websocket-blocked-input",
		Command: "/bin/sh",
		Args: []string{
			"-c",
			"stty raw -echo; printf READY; kill -STOP $$",
		},
		Mode: agent.RunModeInteractive,
	})
	if err != nil {
		t.Fatalf("start agent: %v", err)
	}
	waitForOutputText(t, manager, status.AgentID, "READY")
	t.Cleanup(func() {
		resumeStoppedAgent(t, manager, status.AgentID)
	})

	httpServer := httptest.NewServer(server.mux)
	defer httpServer.Close()
	conn := dialTestWebSocket(t, httpServer.URL)
	defer conn.Close()

	request := webSocketInput{
		Version:   webSocketProtocolVersion,
		Type:      "input",
		RequestID: "blocked-1",
		AgentID:   status.AgentID,
		Data:      strings.Repeat("x", session.MaxInputBytes),
	}
	if err := conn.WriteJSON(request); err != nil {
		t.Fatalf("write input: %v", err)
	}
	response := readWebSocketType(
		t,
		conn,
		"error",
		time.Now().Add(2*time.Second),
	)
	if response["request_id"] != request.RequestID ||
		response["code"] != "input_backpressure" ||
		!strings.Contains(response["message"].(string), "do not retry") {
		t.Fatalf("response = %#v, want partial input_backpressure", response)
	}
}

func TestWebSocketClosesWhenBearerGenerationExpires(t *testing.T) {
	server, _, _ := newTestServerWithOptions(t, true, auth.Options{
		RotationGrace: 40 * time.Millisecond,
		LoginCodeTTL:  time.Minute,
		SessionTTL:    time.Minute,
	})
	httpServer := newBrowserTestServer(t, server)
	defer httpServer.Close()
	conn := dialTestWebSocket(t, httpServer.URL)

	rotate := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/token/rotate",
		nil,
	)
	rotate.Host = "drove.local"
	rotate.Header.Set("Authorization", "Bearer "+testControlToken)
	response := httptest.NewRecorder()
	server.Handler(LocalAccess, "drove.local").ServeHTTP(response, rotate)
	if response.Code != http.StatusNoContent {
		t.Fatalf(
			"rotate response = %d %q, want 204",
			response.Code,
			response.Body.String(),
		)
	}

	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, _, err := conn.ReadMessage()
	if !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("read after rotation error = %v, want policy close", err)
	}
}

func TestDecodeWebSocketInputValidation(t *testing.T) {
	tests := []struct {
		name     string
		payload  []byte
		wantCode string
	}{
		{name: "invalid UTF-8", payload: []byte{0xff}, wantCode: "invalid_utf8"},
		{name: "malformed JSON", payload: []byte(`{"version":`), wantCode: "invalid_message"},
		{name: "null", payload: []byte(`null`), wantCode: "invalid_message"},
		{
			name:     "unknown field",
			payload:  []byte(`{"version":1,"type":"input","request_id":"r1","agent_id":"a1","data":"x","extra":true}`),
			wantCode: "invalid_message",
		},
		{
			name:     "trailing JSON",
			payload:  []byte(`{"version":1,"type":"input","request_id":"r1","agent_id":"a1","data":"x"} {}`),
			wantCode: "invalid_message",
		},
		{
			name:     "unsupported version",
			payload:  []byte(`{"version":2,"type":"input","request_id":"r1","agent_id":"a1","data":"x"}`),
			wantCode: "unsupported_version",
		},
		{
			name:     "unsupported type",
			payload:  []byte(`{"version":1,"type":"other","request_id":"r1","agent_id":"a1","data":"x"}`),
			wantCode: "unsupported_type",
		},
		{
			name:     "invalid request ID",
			payload:  []byte(`{"version":1,"type":"input","request_id":"bad id","agent_id":"a1","data":"x"}`),
			wantCode: "invalid_request_id",
		},
		{
			name:     "invalid agent ID",
			payload:  []byte(`{"version":1,"type":"input","request_id":"r1","agent_id":"","data":"x"}`),
			wantCode: "invalid_agent_id",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, protocolErr := decodeWebSocketInput(test.payload)
			if protocolErr == nil || protocolErr.Code != test.wantCode {
				t.Fatalf("protocol error = %+v, want code %q", protocolErr, test.wantCode)
			}
		})
	}
}

func TestWebSocketInputErrorCodes(t *testing.T) {
	tests := []struct {
		err  error
		code string
	}{
		{err: session.ErrInputEmpty, code: "empty_input"},
		{err: session.ErrInputTooLarge, code: "input_too_large"},
		{err: session.ErrInputNotUTF8, code: "invalid_utf8"},
		{err: session.ErrUnknownAgent, code: "unknown_agent"},
		{err: session.ErrNotAttached, code: "not_attached"},
		{err: session.ErrManagerClosed, code: "manager_closed"},
		{err: session.ErrInputBackpressure, code: "input_backpressure"},
		{err: session.ErrInputWrite, code: "write_failed"},
		{err: session.ErrInputAudit, code: "audit_failed"},
		{err: errors.New("unexpected"), code: "internal_error"},
	}
	for _, test := range tests {
		response := webSocketInputError("r1", test.err)
		if response.Code != test.code || response.RequestID != "r1" {
			t.Fatalf("response = %+v, want code %q", response, test.code)
		}
	}
}

func dialTestWebSocket(t *testing.T, serverURL string) *websocket.Conn {
	t.Helper()
	header := http.Header{"Authorization": []string{"Bearer " + testControlToken}}
	url := "ws" + strings.TrimPrefix(serverURL, "http") + "/ws"
	conn, response, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		t.Fatalf("dial WebSocket: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})
	hello := readWebSocketObject(t, conn, time.Now().Add(2*time.Second))
	if hello["type"] != "hello" {
		t.Fatalf("first message = %#v, want hello", hello)
	}
	return conn
}

func readWebSocketType(
	t *testing.T,
	conn *websocket.Conn,
	messageType string,
	deadline time.Time,
) map[string]any {
	t.Helper()
	for {
		message := readWebSocketObject(t, conn, deadline)
		if message["type"] == messageType {
			return message
		}
	}
}

func readWebSocketObject(t *testing.T, conn *websocket.Conn, deadline time.Time) map[string]any {
	t.Helper()
	if err := conn.SetReadDeadline(deadline); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read WebSocket message: %v", err)
	}
	var message map[string]any
	if err := json.Unmarshal(payload, &message); err != nil {
		t.Fatalf("decode WebSocket message %q: %v", payload, err)
	}
	return message
}
