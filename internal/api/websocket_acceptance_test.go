package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/recording"
	"github.com/Duang777/drove/internal/session"
)

func TestWebSocketV2AcceptanceRacingSubscribeReconnectsWithoutLoss(t *testing.T) {
	server, manager, _ := newTestServer(t)
	status := startExactEchoAgent(t, manager, "websocket-v2-acceptance")
	initial := waitForRecordedOutputLength(t, manager, status.AgentID, len("READY"))
	sendExactEchoInput(t, manager, status.AgentID, []byte("seed\n"), len(initial))
	beforeRace := waitForRecordedOutputLength(
		t,
		manager,
		status.AgentID,
		len(initial)+len("seed\n"),
	)

	httpServer := httptestServer(t, server)
	writer := dialTestWebSocketV2(t, httpServer.URL)
	subscribeWritableRaw(t, writer, status.AgentID, "writer")
	_ = readWebSocketV2TextType(t, writer, "caught_up", time.Now().Add(3*time.Second))

	observer := dialTestWebSocketV2(t, httpServer.URL)
	start := make(chan struct{})
	results := make(chan error, 3)
	var workers sync.WaitGroup
	workers.Add(3)
	go func() {
		defer workers.Done()
		<-start
		results <- observer.WriteJSON(map[string]any{
			"version":    2,
			"type":       "subscribe",
			"request_id": "racing-observer",
			"agent_id":   status.AgentID,
			"mode":       "raw",
		})
	}()
	go func() {
		defer workers.Done()
		<-start
		results <- writer.WriteJSON(map[string]any{
			"version":    2,
			"type":       "resize",
			"request_id": "racing-resize",
			"agent_id":   status.AgentID,
			"rows":       33,
			"columns":    101,
		})
	}()
	go func() {
		defer workers.Done()
		<-start
		_, err := manager.SendInput(agent.ID(status.AgentID), []byte("race\n"))
		results <- err
	}()
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("racing operation: %v", err)
		}
	}

	subscribed := readWebSocketV2TextType(
		t,
		observer,
		"subscribed",
		time.Now().Add(3*time.Second),
	)
	transcript := readRawTranscriptToCaughtUp(
		t,
		observer,
		time.Now().Add(3*time.Second),
	)
	transcript.cursors = append(
		[]recording.Cursor{decodeTestCursor(t, subscribed["cursor"])},
		transcript.cursors...,
	)

	_ = readWebSocketV2TextType(t, writer, "ack", time.Now().Add(3*time.Second))
	_ = readWebSocketV2TextType(t, writer, "resized", time.Now().Add(3*time.Second))
	waitForRecordedOutputLength(
		t,
		manager,
		status.AgentID,
		len(beforeRace)+len("race\n"),
	)

	if _, err := manager.SendInput(agent.ID(status.AgentID), []byte("live\n")); err != nil {
		t.Fatalf("send live input: %v", err)
	}
	wantLength := len(beforeRace) + len("race\n") + len("live\n")
	fullOutput := waitForRecordedOutputLength(
		t,
		manager,
		status.AgentID,
		wantLength,
	)
	live := readRawTranscriptThroughOffset(
		t,
		observer,
		recording.OutputOffset(wantLength),
		time.Now().Add(3*time.Second),
	)
	transcript.output = append(transcript.output, live.output...)
	transcript.cursors = append(transcript.cursors, live.cursors...)
	transcript.live = transcript.live || live.live
	transcript.resizeCount += live.resizeCount

	if !bytes.Equal(transcript.output, fullOutput) {
		t.Fatalf(
			"history plus live output = %q, want Store output %q",
			transcript.output,
			fullOutput,
		)
	}
	if !transcript.historical || !transcript.live {
		t.Fatalf(
			"stream phases = historical %v live %v, want both",
			transcript.historical,
			transcript.live,
		)
	}
	if transcript.resizeCount != 1 {
		t.Fatalf("resize messages = %d, want 1", transcript.resizeCount)
	}

	for _, cursor := range uniqueTestCursors(transcript.cursors) {
		conn := dialTestWebSocketV2(t, httpServer.URL)
		if err := conn.WriteJSON(map[string]any{
			"version":    2,
			"type":       "subscribe",
			"request_id": "reconnect-" + cursor.Seq.String() + "-" + cursor.NextOffset.String(),
			"agent_id":   status.AgentID,
			"mode":       "raw",
			"cursor":     cursor,
		}); err != nil {
			t.Fatalf("reconnect from %+v: %v", cursor, err)
		}
		_ = readWebSocketV2TextType(t, conn, "subscribed", time.Now().Add(3*time.Second))
		replayed := readRawTranscriptToCaughtUp(
			t,
			conn,
			time.Now().Add(3*time.Second),
		)
		if !bytes.Equal(replayed.output, fullOutput[int(cursor.NextOffset):]) {
			t.Fatalf(
				"reconnect from %+v = %q, want %q",
				cursor,
				replayed.output,
				fullOutput[int(cursor.NextOffset):],
			)
		}
		if replayed.live {
			t.Fatalf("reconnect from %+v marked history as live", cursor)
		}
		_ = conn.Close()
	}
}

func TestWebSocketV2AcceptanceSlowConnectionIsIsolated(t *testing.T) {
	server, manager, _ := newTestServer(t)
	status := startExactEchoAgent(t, manager, "websocket-v2-slow")
	initial := waitForRecordedOutputLength(t, manager, status.AgentID, len("READY"))

	httpServer := httptestServer(t, server)
	server.webSocketV2QueueBudget = 512
	slow := dialTestWebSocketV2(t, httpServer.URL)
	subscribeReadOnlyRaw(t, slow, status.AgentID, "slow")
	slowHead := readRawTranscriptToCaughtUp(t, slow, time.Now().Add(3*time.Second)).caughtUp

	server.webSocketV2QueueBudget = webSocketV2QueueBytes
	fast := dialTestWebSocketV2(t, httpServer.URL)
	subscribeReadOnlyRaw(t, fast, status.AgentID, "fast")
	fastHistory := readRawTranscriptToCaughtUp(t, fast, time.Now().Add(3*time.Second))

	large := []byte(strings.Repeat("x", 512) + "\n")
	if _, err := manager.SendInput(agent.ID(status.AgentID), large); err != nil {
		t.Fatalf("send overflow input: %v", err)
	}
	slowError := readWebSocketV2TextType(
		t,
		slow,
		"error",
		time.Now().Add(3*time.Second),
	)
	if slowError["code"] != "slow_consumer" {
		t.Fatalf("slow connection error = %#v", slowError)
	}
	subscriptions, ok := slowError["subscriptions"].([]any)
	if !ok || len(subscriptions) != 1 {
		t.Fatalf("slow connection cursors = %#v", slowError["subscriptions"])
	}
	resume := subscriptions[0].(map[string]any)
	if got := decodeTestCursor(t, resume["cursor"]); got != slowHead {
		t.Fatalf("slow resume cursor = %+v, want %+v", got, slowHead)
	}

	_, _, err := slow.ReadMessage()
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != webSocketV2TryAgainLater {
		t.Fatalf("slow close = %v, want code %d", err, webSocketV2TryAgainLater)
	}

	startedAt := time.Now()
	if _, err := manager.SendInput(
		agent.ID(status.AgentID),
		[]byte("after\n"),
	); err != nil {
		t.Fatalf("send after slow close: %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("committer was delayed for %s after slow-client overflow", elapsed)
	}
	wantLength := len(initial) + len(large) + len("after\n")
	fullOutput := waitForRecordedOutputLength(
		t,
		manager,
		status.AgentID,
		wantLength,
	)
	fastLive := readRawTranscriptThroughOffset(
		t,
		fast,
		recording.OutputOffset(wantLength),
		time.Now().Add(3*time.Second),
	)
	got := append(fastHistory.output, fastLive.output...)
	if !bytes.Equal(got, fullOutput) {
		t.Fatalf("fast connection output length = %d, want %d", len(got), len(fullOutput))
	}
}

func TestWebSocketV1GoldenTranscript(t *testing.T) {
	server, _, _ := newTestServer(t)
	httpServer := httptestServer(t, server)
	header := http.Header{"Authorization": []string{"Bearer " + testControlToken}}
	url := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws"
	conn, response, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		t.Fatalf("dial WebSocket v1: %v", err)
	}
	defer conn.Close()

	transcript := make([]string, 0, 3)
	transcript = append(transcript, strconv.Quote(readWebSocketText(t, conn)))
	if err := server.opts.Hub.Publish(event.Event{
		Seq:       1,
		Timestamp: time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC),
		Type:      event.TypeError,
		AgentID:   "agent-1",
		SessionID: "session-1",
		Reason:    "boom",
		Payload:   "details",
	}); err != nil {
		t.Fatalf("publish v1 golden event: %v", err)
	}
	transcript = append(transcript, strconv.Quote(readWebSocketText(t, conn)))
	if err := conn.WriteJSON(webSocketInput{
		Version:   1,
		Type:      "input",
		RequestID: "request-1",
		AgentID:   "missing",
		Data:      "continue\n",
	}); err != nil {
		t.Fatalf("write v1 golden input: %v", err)
	}
	transcript = append(transcript, strconv.Quote(readWebSocketText(t, conn)))

	golden, err := os.ReadFile(filepath.Join("testdata", "websocket_v1.golden"))
	if err != nil {
		t.Fatalf("read v1 golden transcript: %v", err)
	}
	got := strings.Join(transcript, "\n") + "\n"
	if got != string(golden) {
		t.Fatalf("v1 transcript changed:\ngot:\n%s\nwant:\n%s", got, golden)
	}
}

type rawTestTranscript struct {
	output      []byte
	cursors     []recording.Cursor
	caughtUp    recording.Cursor
	historical  bool
	live        bool
	resizeCount int
}

func readRawTranscriptToCaughtUp(
	t *testing.T,
	conn *websocket.Conn,
	deadline time.Time,
) rawTestTranscript {
	t.Helper()
	var transcript rawTestTranscript
	for {
		messageType, payload := readWebSocketV2Message(t, conn, deadline)
		if messageType == websocket.BinaryMessage {
			transcript.addRaw(t, payload)
			continue
		}
		message := decodeTestJSONObject(t, payload)
		switch message["type"] {
		case "resized":
			transcript.addResize(t, message)
		case "caught_up":
			transcript.caughtUp = decodeTestCursor(t, message["cursor"])
			transcript.cursors = append(transcript.cursors, transcript.caughtUp)
			return transcript
		case "error":
			t.Fatalf("raw stream error = %#v", message)
		}
	}
}

func readRawTranscriptThroughOffset(
	t *testing.T,
	conn *websocket.Conn,
	target recording.OutputOffset,
	deadline time.Time,
) rawTestTranscript {
	t.Helper()
	var transcript rawTestTranscript
	for {
		messageType, payload := readWebSocketV2Message(t, conn, deadline)
		if messageType == websocket.BinaryMessage {
			cursor := transcript.addRaw(t, payload)
			if cursor.NextOffset >= target {
				return transcript
			}
			continue
		}
		message := decodeTestJSONObject(t, payload)
		switch message["type"] {
		case "resized":
			transcript.addResize(t, message)
		case "error":
			t.Fatalf("raw stream error = %#v", message)
		}
	}
}

func (t *rawTestTranscript) addRaw(testingT *testing.T, payload []byte) recording.Cursor {
	testingT.Helper()
	raw := decodeTestRawFrame(testingT, payload)
	t.output = append(t.output, raw.data...)
	t.historical = t.historical || raw.historical
	t.live = t.live || !raw.historical
	cursor, err := recording.NewCursor(
		recording.Seq(raw.sequence),
		recording.OutputOffset(raw.offset+uint64(len(raw.data))),
	)
	if err != nil {
		testingT.Fatalf("create raw cursor: %v", err)
	}
	t.cursors = append(t.cursors, cursor)
	return cursor
}

func (t *rawTestTranscript) addResize(testingT *testing.T, message map[string]any) {
	testingT.Helper()
	t.cursors = append(t.cursors, decodeTestCursor(testingT, message["cursor"]))
	t.resizeCount++
	historical, ok := message["historical"].(bool)
	if !ok {
		testingT.Fatalf("resize historical flag = %#v", message["historical"])
	}
	t.historical = t.historical || historical
	t.live = t.live || !historical
}

func decodeTestCursor(t *testing.T, value any) recording.Cursor {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("cursor = %#v, want object", value)
	}
	sequence, ok := object["seq"].(string)
	if !ok {
		t.Fatalf("cursor sequence = %#v, want string", object["seq"])
	}
	offset, ok := object["next_offset"].(string)
	if !ok {
		t.Fatalf("cursor offset = %#v, want string", object["next_offset"])
	}
	parsedSequence, err := recording.ParseSeq(sequence)
	if err != nil {
		t.Fatalf("parse cursor sequence: %v", err)
	}
	parsedOffset, err := recording.ParseOutputOffset(offset)
	if err != nil {
		t.Fatalf("parse cursor offset: %v", err)
	}
	cursor, err := recording.NewCursor(parsedSequence, parsedOffset)
	if err != nil {
		t.Fatalf("create cursor: %v", err)
	}
	return cursor
}

func uniqueTestCursors(cursors []recording.Cursor) []recording.Cursor {
	seen := make(map[recording.Cursor]struct{}, len(cursors))
	unique := make([]recording.Cursor, 0, len(cursors))
	for _, cursor := range cursors {
		if _, exists := seen[cursor]; exists {
			continue
		}
		seen[cursor] = struct{}{}
		unique = append(unique, cursor)
	}
	return unique
}

func startExactEchoAgent(
	t *testing.T,
	manager *session.Manager,
	name string,
) *session.Status {
	t.Helper()
	status, err := manager.Start(context.Background(), session.StartRequest{
		Name:    name,
		Command: "/bin/sh",
		Args:    []string{"-c", "stty -echo -onlcr; printf READY; exec cat"},
		Mode:    agent.RunModeInteractive,
		Hooks:   agent.HooksOff,
	})
	if err != nil {
		t.Fatalf("start exact echo agent: %v", err)
	}
	return status
}

func sendExactEchoInput(
	t *testing.T,
	manager *session.Manager,
	agentID string,
	data []byte,
	previousLength int,
) {
	t.Helper()
	if _, err := manager.SendInput(agent.ID(agentID), data); err != nil {
		t.Fatalf("send exact echo input: %v", err)
	}
	waitForRecordedOutputLength(t, manager, agentID, previousLength+len(data))
}

func subscribeWritableRaw(
	t *testing.T,
	conn *websocket.Conn,
	agentID string,
	requestID string,
) {
	t.Helper()
	if err := conn.WriteJSON(map[string]any{
		"version":    2,
		"type":       "subscribe",
		"request_id": requestID,
		"agent_id":   agentID,
		"mode":       "raw",
		"writable":   true,
	}); err != nil {
		t.Fatalf("subscribe writable raw: %v", err)
	}
	_ = readWebSocketV2TextType(t, conn, "subscribed", time.Now().Add(3*time.Second))
}

func subscribeReadOnlyRaw(
	t *testing.T,
	conn *websocket.Conn,
	agentID string,
	requestID string,
) {
	t.Helper()
	if err := conn.WriteJSON(map[string]any{
		"version":    2,
		"type":       "subscribe",
		"request_id": requestID,
		"agent_id":   agentID,
		"mode":       "raw",
	}); err != nil {
		t.Fatalf("subscribe read-only raw: %v", err)
	}
	_ = readWebSocketV2TextType(t, conn, "subscribed", time.Now().Add(3*time.Second))
}

func readWebSocketText(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set WebSocket read deadline: %v", err)
	}
	messageType, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read WebSocket text: %v", err)
	}
	if messageType != websocket.TextMessage {
		t.Fatalf("WebSocket message type = %d, want text", messageType)
	}
	return string(payload)
}

func httptestServer(t *testing.T, server *Server) *httptest.Server {
	t.Helper()
	httpServer := httptest.NewServer(server.mux)
	t.Cleanup(httpServer.Close)
	return httpServer
}
