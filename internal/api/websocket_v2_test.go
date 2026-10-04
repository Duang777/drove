package api

import (
	"bytes"
	"context"
	"encoding/binary"
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
	"github.com/Duang777/drove/internal/recording"
	"github.com/Duang777/drove/internal/session"
	"github.com/Duang777/drove/internal/store"
)

func TestWebSocketV2ClosesWhenAuthorizationExpires(t *testing.T) {
	server, _, _ := newTestServerWithOptions(t, true, auth.Options{
		RotationGrace: 20 * time.Millisecond,
		LoginCodeTTL:  time.Minute,
		SessionTTL:    time.Minute,
	})
	httpServer := newBrowserTestServer(t, server)
	defer httpServer.Close()

	header := http.Header{"Authorization": []string{"Bearer " + testControlToken}}
	dialer := *websocket.DefaultDialer
	dialer.Subprotocols = []string{webSocketV2Protocol}
	conn, _, err := dialer.Dial(
		"ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws",
		header,
	)
	if err != nil {
		t.Fatalf("dial WebSocket v2: %v", err)
	}
	defer conn.Close()
	_ = readWebSocketV2TextType(t, conn, "hello", time.Now().Add(time.Second))

	if err := server.opts.Auth.Rotate(); err != nil {
		t.Fatalf("rotate token: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, _, err = conn.ReadMessage()
	if !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("read after authorization expiry = %v, want policy close", err)
	}
	var closeError *websocket.CloseError
	if !errors.As(err, &closeError) || closeError.Text != "authorization expired" {
		t.Fatalf("close error = %v, want authorization expired", err)
	}
}

func TestWebSocketV2NegotiatesAndStreamsRawHistoryFromOffset(t *testing.T) {
	server, manager, _ := newTestServer(t)
	status, err := manager.Start(context.Background(), session.StartRequest{
		Name:    "websocket-v2-raw",
		Command: "/bin/cat",
		Mode:    agent.RunModeInteractive,
	})
	if err != nil {
		t.Fatalf("start agent: %v", err)
	}
	if _, err := manager.SendInput(agent.ID(status.AgentID), []byte("hello\n")); err != nil {
		t.Fatalf("seed terminal output: %v", err)
	}
	if recorded := waitForRecordedOutput(t, manager, status.AgentID); len(recorded) < 2 {
		t.Fatalf("recorded output length = %d, want at least 2", len(recorded))
	}

	httpServer := httptest.NewServer(server.mux)
	defer httpServer.Close()
	conn := dialTestWebSocketV2(t, httpServer.URL)
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{
		"version":    2,
		"type":       "subscribe",
		"request_id": "subscribe-raw",
		"agent_id":   status.AgentID,
		"mode":       "raw",
		"offset":     "1",
	}); err != nil {
		t.Fatalf("subscribe raw: %v", err)
	}
	subscribed := readWebSocketV2TextType(
		t,
		conn,
		"subscribed",
		time.Now().Add(3*time.Second),
	)
	if subscribed["request_id"] != "subscribe-raw" ||
		subscribed["agent_id"] != status.AgentID ||
		subscribed["mode"] != "raw" {
		t.Fatalf("subscribed = %#v", subscribed)
	}

	var streamed []byte
	var lastCursor map[string]any
	for {
		messageType, payload := readWebSocketV2Message(
			t,
			conn,
			time.Now().Add(3*time.Second),
		)
		if messageType == websocket.BinaryMessage {
			raw := decodeTestRawFrame(t, payload)
			if raw.agentID != status.AgentID || !raw.historical {
				t.Fatalf("raw frame = %+v", raw)
			}
			streamed = append(streamed, raw.data...)
			continue
		}
		message := decodeTestJSONObject(t, payload)
		if message["type"] == "caught_up" {
			lastCursor = message["cursor"].(map[string]any)
			break
		}
	}
	nextOffset, err := recording.ParseOutputOffset(lastCursor["next_offset"].(string))
	if err != nil {
		t.Fatalf("parse caught-up offset: %v", err)
	}
	expected := waitForRecordedOutputLength(
		t,
		manager,
		status.AgentID,
		int(nextOffset),
	)
	if !bytes.Equal(streamed, expected[1:int(nextOffset)]) {
		t.Fatalf("streamed raw = %q, want %q", streamed, expected[1:int(nextOffset)])
	}

	if err := conn.WriteJSON(map[string]any{
		"version":    2,
		"type":       "input",
		"request_id": "read-only-input",
		"agent_id":   status.AgentID,
		"data":       "x",
	}); err != nil {
		t.Fatalf("write read-only input: %v", err)
	}
	response := readWebSocketV2TextType(
		t,
		conn,
		"error",
		time.Now().Add(3*time.Second),
	)
	if response["code"] != "not_writable" {
		t.Fatalf("read-only input response = %#v", response)
	}
	rows, err := manager.Replay(status.AgentID)
	if err != nil {
		t.Fatalf("replay recording raw subscription: %v", err)
	}
	for _, row := range rows {
		if row.Type == string(event.TypeAgentAttachment) {
			t.Fatalf("recording raw subscription wrote attachment audit: %+v", row)
		}
	}
}

func TestWebSocketV2ExplicitReadOnlyRawAuditsLifecycle(t *testing.T) {
	server, manager, _ := newTestServer(t)
	status, err := manager.Start(context.Background(), session.StartRequest{
		Name:    "websocket-v2-read-only",
		Command: "/bin/cat",
		Mode:    agent.RunModeInteractive,
	})
	if err != nil {
		t.Fatalf("start agent: %v", err)
	}

	httpServer := httptest.NewServer(server.mux)
	defer httpServer.Close()
	conn := dialTestWebSocketV2(t, httpServer.URL)
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{
		"version":    2,
		"type":       "subscribe",
		"request_id": "subscribe-read-only",
		"agent_id":   status.AgentID,
		"mode":       "raw",
		"writable":   false,
	}); err != nil {
		t.Fatalf("subscribe read-only raw: %v", err)
	}
	_ = readWebSocketV2TextType(t, conn, "subscribed", time.Now().Add(3*time.Second))

	if err := conn.WriteJSON(map[string]any{
		"version":    2,
		"type":       "input",
		"request_id": "read-only-input",
		"agent_id":   status.AgentID,
		"data":       "private input",
	}); err != nil {
		t.Fatalf("write read-only input: %v", err)
	}
	response := readWebSocketV2TextType(
		t,
		conn,
		"error",
		time.Now().Add(3*time.Second),
	)
	if response["code"] != "not_writable" {
		t.Fatalf("read-only input response = %#v", response)
	}

	if err := conn.WriteJSON(map[string]any{
		"version":    2,
		"type":       "unsubscribe",
		"request_id": "unsubscribe-read-only",
		"agent_id":   status.AgentID,
		"mode":       "raw",
	}); err != nil {
		t.Fatalf("unsubscribe read-only raw: %v", err)
	}
	_ = readWebSocketV2TextType(t, conn, "unsubscribed", time.Now().Add(3*time.Second))

	var audits []event.AttachmentAuditPayloadV1
	rows, err := manager.Replay(status.AgentID)
	if err != nil {
		t.Fatalf("replay attachment audit: %v", err)
	}
	for _, row := range rows {
		if row.Type != string(event.TypeAgentAttachment) {
			continue
		}
		if strings.Contains(row.Payload, "private input") ||
			strings.Contains(row.Payload, "subscribe-read-only") {
			t.Fatalf("attachment audit retained private data: %s", row.Payload)
		}
		payload, err := event.DecodeAttachmentAuditPayload(row.Payload)
		if err != nil {
			t.Fatalf("decode attachment audit: %v", err)
		}
		audits = append(audits, payload)
	}
	if len(audits) != 2 ||
		audits[0].Action != event.AttachmentAttached ||
		audits[0].Access != event.AttachmentReadOnly ||
		audits[1].Action != event.AttachmentDetached ||
		audits[1].Access != event.AttachmentReadOnly {
		t.Fatalf("attachment audits = %+v", audits)
	}
}

func TestWebSocketV2EventHistoryRedactsPrivateSessionMetadata(t *testing.T) {
	server, _, st := newTestServer(t)
	base := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	rows := []store.EventRow{
		{
			Seq:       1,
			Timestamp: base,
			Type:      string(event.TypeSessionLifecycle),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Reason:    "created",
			Payload: `{"version":2,"name":"agent","vendor":"claude",` +
				`"working_dir":"/private/project"}`,
		},
		{
			Seq:       2,
			Timestamp: base.Add(time.Second),
			Type:      string(event.TypeAgentSignal),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Reason:    "observed",
			Payload: `{"version":1,"source":"hook","kind":"session_started","vendor":"claude",` +
				`"vendor_event":"SessionStart","scope":"root","vendor_session_ref":"vendor-ref",` +
				`"confidence":1,"received_at":"2026-10-04T12:00:01Z",` +
				`"delivery_id":"550e8400-e29b-41d4-a716-446655440000","outcome":"observed"}`,
		},
		{
			Seq:       3,
			Timestamp: base.Add(2 * time.Second),
			Type:      string(event.TypeAgentResumed),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			Reason:    "requested",
			Payload:   `{"version":1,"vendor_session_ref":"vendor-ref"}`,
		},
	}
	if _, err := st.AppendEvents(context.Background(), 0, rows); err != nil {
		t.Fatalf("seed private event history: %v", err)
	}

	httpServer := httptest.NewServer(server.mux)
	defer httpServer.Close()
	conn := dialTestWebSocketV2(t, httpServer.URL)
	defer conn.Close()
	if err := conn.WriteJSON(map[string]any{
		"version":    2,
		"type":       "subscribe",
		"request_id": "subscribe-private-events",
		"agent_id":   "agent-1",
		"mode":       "events",
	}); err != nil {
		t.Fatalf("subscribe events: %v", err)
	}
	_ = readWebSocketV2TextType(
		t,
		conn,
		"subscribed",
		time.Now().Add(3*time.Second),
	)

	eventCount := 0
	for {
		messageType, payload := readWebSocketV2Message(
			t,
			conn,
			time.Now().Add(3*time.Second),
		)
		if messageType != websocket.TextMessage {
			continue
		}
		message := decodeTestJSONObject(t, payload)
		if message["type"] == "caught_up" {
			break
		}
		if message["type"] != "event" {
			t.Fatalf("unexpected events message = %#v", message)
		}
		eventCount++
		encoded, err := json.Marshal(message)
		if err != nil {
			t.Fatalf("encode event message: %v", err)
		}
		for _, private := range []string{
			"/private/project",
			"vendor-ref",
			"working_dir",
			"vendor_session_ref",
			"vendor_session_id",
		} {
			if bytes.Contains(encoded, []byte(private)) {
				t.Fatalf("WebSocket event exposed %q: %s", private, encoded)
			}
		}
	}
	if eventCount != len(rows) {
		t.Fatalf("event count = %d, want %d", eventCount, len(rows))
	}
}

func TestWebSocketV2WritableRawInputResizeAndEvents(t *testing.T) {
	server, manager, _ := newTestServer(t)
	status, err := manager.Start(context.Background(), session.StartRequest{
		Name:    "websocket-v2-write",
		Command: "/bin/cat",
		Mode:    agent.RunModeInteractive,
	})
	if err != nil {
		t.Fatalf("start agent: %v", err)
	}

	httpServer := httptest.NewServer(server.mux)
	defer httpServer.Close()
	conn := dialTestWebSocketV2(t, httpServer.URL)
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{
		"version":    2,
		"type":       "subscribe",
		"request_id": "subscribe-write",
		"agent_id":   status.AgentID,
		"mode":       "raw",
		"writable":   true,
		"rows":       50,
		"columns":    160,
	}); err != nil {
		t.Fatalf("subscribe writable raw: %v", err)
	}
	_ = readWebSocketV2TextType(t, conn, "subscribed", time.Now().Add(3*time.Second))
	resized := readWebSocketV2TextType(t, conn, "resized", time.Now().Add(3*time.Second))
	if resized["rows"] != float64(50) ||
		resized["columns"] != float64(160) ||
		resized["historical"] != true {
		t.Fatalf("initial resize = %#v", resized)
	}
	_ = readWebSocketV2TextType(t, conn, "caught_up", time.Now().Add(3*time.Second))

	if err := conn.WriteJSON(map[string]any{
		"version":    2,
		"type":       "input",
		"request_id": "input-1",
		"agent_id":   status.AgentID,
		"data":       "continue\n",
	}); err != nil {
		t.Fatalf("write input: %v", err)
	}
	ack := readWebSocketV2TextType(t, conn, "ack", time.Now().Add(3*time.Second))
	if ack["request_id"] != "input-1" || ack["bytes"] != float64(len("continue\n")) {
		t.Fatalf("input ack = %#v", ack)
	}
	for {
		messageType, payload := readWebSocketV2Message(
			t,
			conn,
			time.Now().Add(3*time.Second),
		)
		if messageType != websocket.BinaryMessage {
			continue
		}
		raw := decodeTestRawFrame(t, payload)
		if raw.historical {
			t.Fatalf("live raw frame marked historical: %+v", raw)
		}
		if bytes.Contains(raw.data, []byte("continue")) {
			break
		}
	}

	if err := conn.WriteJSON(map[string]any{
		"version":    2,
		"type":       "resize",
		"request_id": "resize-1",
		"agent_id":   status.AgentID,
		"rows":       60,
		"columns":    180,
	}); err != nil {
		t.Fatalf("write resize: %v", err)
	}
	ack = readWebSocketV2TextType(t, conn, "ack", time.Now().Add(3*time.Second))
	if ack["request_id"] != "resize-1" {
		t.Fatalf("resize ack = %#v", ack)
	}
	resized = readWebSocketV2TextType(t, conn, "resized", time.Now().Add(3*time.Second))
	if resized["rows"] != float64(60) ||
		resized["columns"] != float64(180) ||
		resized["historical"] != false {
		t.Fatalf("live resize = %#v", resized)
	}

	if err := conn.WriteJSON(map[string]any{
		"version":    2,
		"type":       "subscribe",
		"request_id": "subscribe-events",
		"agent_id":   status.AgentID,
		"mode":       "events",
		"seq":        "0",
	}); err != nil {
		t.Fatalf("subscribe events: %v", err)
	}
	_ = readWebSocketV2TextType(t, conn, "subscribed", time.Now().Add(3*time.Second))
	eventMessage := readWebSocketV2TextType(t, conn, "event", time.Now().Add(3*time.Second))
	envelope := eventMessage["event"].(map[string]any)
	if _, ok := envelope["seq"].(string); !ok {
		t.Fatalf("event sequence is not a decimal string: %#v", envelope)
	}
	cursor := eventMessage["cursor"].(map[string]any)
	if _, ok := cursor["seq"].(string); !ok {
		t.Fatalf("event cursor sequence is not a decimal string: %#v", cursor)
	}
}

func TestWebSocketV2InputReturnsBackpressureForBlockedPTY(t *testing.T) {
	server, manager, _ := newTestServer(t)
	status, err := manager.Start(context.Background(), session.StartRequest{
		Name:    "websocket-v2-blocked-input",
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
	conn := dialTestWebSocketV2(t, httpServer.URL)
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{
		"version":    2,
		"type":       "subscribe",
		"request_id": "subscribe-blocked",
		"agent_id":   status.AgentID,
		"mode":       "raw",
		"writable":   true,
		"rows":       40,
		"columns":    120,
	}); err != nil {
		t.Fatalf("subscribe writable raw: %v", err)
	}
	_ = readWebSocketV2TextType(
		t,
		conn,
		"subscribed",
		time.Now().Add(2*time.Second),
	)
	_ = readWebSocketV2TextType(
		t,
		conn,
		"caught_up",
		time.Now().Add(2*time.Second),
	)

	if err := conn.WriteJSON(map[string]any{
		"version":    2,
		"type":       "input",
		"request_id": "blocked-1",
		"agent_id":   status.AgentID,
		"data":       strings.Repeat("x", session.MaxInputBytes),
	}); err != nil {
		t.Fatalf("write input: %v", err)
	}
	response := readWebSocketV2TextType(
		t,
		conn,
		"error",
		time.Now().Add(2*time.Second),
	)
	if response["request_id"] != "blocked-1" ||
		response["code"] != "input_backpressure" ||
		!strings.Contains(response["message"].(string), "do not retry") {
		t.Fatalf("response = %#v, want partial input_backpressure", response)
	}
}

func TestWebSocketV2RejectsUnsupportedNegotiationBeforeUpgrade(t *testing.T) {
	server, _, _ := newTestServer(t)
	httpServer := httptest.NewServer(server.mux)
	defer httpServer.Close()
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws"
	header := http.Header{"Authorization": []string{"Bearer " + testControlToken}}

	dialer := *websocket.DefaultDialer
	dialer.Subprotocols = []string{"unknown.v9"}
	conn, response, err := dialer.Dial(wsURL, header)
	if err == nil {
		conn.Close()
		t.Fatal("unsupported WebSocket protocol was upgraded")
	}
	if response == nil || response.StatusCode != http.StatusBadRequest {
		t.Fatalf("response = %+v, want 400", response)
	}
	response.Body.Close()

	dialer.Subprotocols = []string{webSocketV2Protocol, "unknown.v9"}
	conn, response, err = dialer.Dial(wsURL, header)
	if err == nil {
		conn.Close()
		t.Fatal("mixed WebSocket protocols were upgraded")
	}
	if response == nil || response.StatusCode != http.StatusBadRequest {
		t.Fatalf("mixed response = %+v, want 400", response)
	}
	response.Body.Close()
}

func TestDecodeWebSocketV2CommandIsStrict(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		wantCode string
	}{
		{
			name:     "unknown field",
			payload:  `{"version":2,"type":"input","request_id":"r1","agent_id":"a1","data":"x","extra":true}`,
			wantCode: "invalid_message",
		},
		{
			name:     "numeric cursor",
			payload:  `{"version":2,"type":"subscribe","request_id":"r1","agent_id":"a1","mode":"raw","seq":1}`,
			wantCode: "invalid_message",
		},
		{
			name:     "mixed selector",
			payload:  `{"version":2,"type":"subscribe","request_id":"r1","agent_id":"a1","mode":"raw","seq":"1","offset":"1"}`,
			wantCode: "invalid_selector",
		},
		{
			name:     "snapshot cursor",
			payload:  `{"version":2,"type":"subscribe","request_id":"r1","agent_id":"a1","mode":"snapshot","seq":"1"}`,
			wantCode: "invalid_subscription",
		},
		{
			name:     "partial viewport",
			payload:  `{"version":2,"type":"subscribe","request_id":"r1","agent_id":"a1","mode":"raw","writable":true,"rows":20}`,
			wantCode: "invalid_size",
		},
		{
			name:     "bad size",
			payload:  `{"version":2,"type":"resize","request_id":"r1","agent_id":"a1","rows":0,"columns":80}`,
			wantCode: "invalid_size",
		},
		{
			name:     "unsupported type",
			payload:  `{"version":2,"type":"other","request_id":"r1"}`,
			wantCode: "unsupported_type",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, protocolErr := decodeWebSocketV2Command([]byte(test.payload))
			if protocolErr == nil || protocolErr.Code != test.wantCode {
				t.Fatalf("protocol error = %+v, want %q", protocolErr, test.wantCode)
			}
		})
	}
}

func TestWebSocketV2RawCodec(t *testing.T) {
	payload, err := encodeWebSocketV2Raw(recording.RawOutput{
		AgentID:    "agent-1",
		Sequence:   42,
		Offset:     9007199254740993,
		Data:       []byte{0x00, 0xff, 'x'},
		Historical: true,
	})
	if err != nil {
		t.Fatalf("encode raw: %v", err)
	}
	frame := decodeTestRawFrame(t, payload)
	if frame.agentID != "agent-1" ||
		frame.sequence != 42 ||
		frame.offset != 9007199254740993 ||
		!frame.historical ||
		!bytes.Equal(frame.data, []byte{0x00, 0xff, 'x'}) {
		t.Fatalf("decoded raw frame = %+v", frame)
	}
	if _, err := encodeWebSocketV2Raw(recording.RawOutput{
		AgentID: "agent-1",
		Data:    nil,
	}); err == nil {
		t.Fatal("raw codec accepted empty output")
	}
}

func TestWebSocketV2QueueDropsDataAndKeepsReservedControlOnOverflow(t *testing.T) {
	queue := newWebSocketV2Queue(4)
	subscription := &webSocketV2Subscription{}
	if !queue.Enqueue(webSocketV2Frame{
		payload:      []byte("1234"),
		subscription: subscription,
	}) {
		t.Fatal("queue rejected frame at exact budget")
	}
	if queue.Enqueue(webSocketV2Frame{
		payload:      []byte("x"),
		subscription: subscription,
	}) {
		t.Fatal("queue accepted frame over budget")
	}
	if !queue.Overflowed() {
		t.Fatal("queue did not retain overflow state")
	}
	if frame, ok := queue.Pop(); ok {
		t.Fatalf("queue retained data after overflow: %+v", frame)
	}

	control := webSocketV2Frame{
		messageType: websocket.TextMessage,
		payload:     []byte("slow_consumer"),
	}
	queue.ReserveControl(control)
	frame, ok := queue.Pop()
	if !ok || !bytes.Equal(frame.payload, control.payload) {
		t.Fatalf("reserved control = %+v, %v", frame, ok)
	}
}

func dialTestWebSocketV2(t *testing.T, serverURL string) *websocket.Conn {
	t.Helper()
	dialer := *websocket.DefaultDialer
	dialer.Subprotocols = []string{webSocketV2Protocol}
	header := http.Header{"Authorization": []string{"Bearer " + testControlToken}}
	url := "ws" + strings.TrimPrefix(serverURL, "http") + "/ws"
	conn, response, err := dialer.Dial(url, header)
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		t.Fatalf("dial WebSocket v2: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})
	if conn.Subprotocol() != webSocketV2Protocol {
		t.Fatalf("subprotocol = %q, want %q", conn.Subprotocol(), webSocketV2Protocol)
	}
	hello := readWebSocketV2TextType(t, conn, "hello", time.Now().Add(2*time.Second))
	if hello["version"] != float64(2) {
		t.Fatalf("hello = %#v", hello)
	}
	return conn
}

func readWebSocketV2TextType(
	t *testing.T,
	conn *websocket.Conn,
	want string,
	deadline time.Time,
) map[string]any {
	t.Helper()
	for {
		messageType, payload := readWebSocketV2Message(t, conn, deadline)
		if messageType != websocket.TextMessage {
			continue
		}
		message := decodeTestJSONObject(t, payload)
		if message["type"] == want {
			return message
		}
	}
}

func readWebSocketV2Message(
	t *testing.T,
	conn *websocket.Conn,
	deadline time.Time,
) (int, []byte) {
	t.Helper()
	if err := conn.SetReadDeadline(deadline); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	messageType, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read WebSocket v2 message: %v", err)
	}
	return messageType, payload
}

func decodeTestJSONObject(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	var message map[string]any
	if err := json.Unmarshal(payload, &message); err != nil {
		t.Fatalf("decode WebSocket v2 text %q: %v", payload, err)
	}
	return message
}

type testRawFrame struct {
	agentID    string
	sequence   uint64
	offset     uint64
	historical bool
	data       []byte
}

func decodeTestRawFrame(t *testing.T, payload []byte) testRawFrame {
	t.Helper()
	if len(payload) < webSocketV2RawHeaderBytes ||
		string(payload[:4]) != "DRV2" ||
		payload[4] != webSocketV2RawOutputKind {
		t.Fatalf("invalid raw frame header: %x", payload)
	}
	agentLength := int(binary.BigEndian.Uint16(payload[6:8]))
	if agentLength == 0 || len(payload) <= webSocketV2RawHeaderBytes+agentLength {
		t.Fatalf("invalid raw frame length: %d", len(payload))
	}
	return testRawFrame{
		agentID:    string(payload[24 : 24+agentLength]),
		sequence:   binary.BigEndian.Uint64(payload[8:16]),
		offset:     binary.BigEndian.Uint64(payload[16:24]),
		historical: payload[5]&webSocketV2HistoricalFlag != 0,
		data:       append([]byte(nil), payload[24+agentLength:]...),
	}
}

func waitForRecordedOutput(
	t *testing.T,
	manager *session.Manager,
	agentID string,
) []byte {
	return waitForRecordedOutputLength(t, manager, agentID, 1)
}

func waitForRecordedOutputLength(
	t *testing.T,
	manager *session.Manager,
	agentID string,
	minimum int,
) []byte {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		rows, err := manager.Replay(agentID)
		if err != nil {
			t.Fatalf("replay output: %v", err)
		}
		var output []byte
		for _, row := range rows {
			if row.Type != string(event.TypeOutputChunk) {
				continue
			}
			payload, err := event.DecodeOutputChunkPayload(row.Payload)
			if err != nil {
				t.Fatalf("decode output: %v", err)
			}
			data, err := payload.DecodeData()
			if err != nil {
				t.Fatalf("decode output data: %v", err)
			}
			output = append(output, data...)
		}
		if len(output) >= minimum {
			return output
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal output was not recorded")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWebSocketV2OperationErrorIncludesExpiredRanges(t *testing.T) {
	source := &recording.OutputExpiredError{
		SessionID: "s1",
		Missing: []recording.OutputRange{{
			Start: 3,
			End:   8,
		}},
	}
	response := webSocketV2OperationError(
		"",
		"s1",
		webSocketV2ModeRaw,
		source,
	)
	if response.Code != "output_expired" ||
		len(response.Missing) != 1 ||
		response.Missing[0] != source.Missing[0] {
		t.Fatalf("response = %+v", response)
	}
}

func TestWebSocketV2OperationErrorMapsInputBackpressure(t *testing.T) {
	response := webSocketV2OperationError(
		"request-1",
		"agent-1",
		webSocketV2ModeRaw,
		session.ErrInputBackpressure,
	)
	if response.Code != "input_backpressure" ||
		response.RequestID != "request-1" {
		t.Fatalf("response = %+v", response)
	}
}
