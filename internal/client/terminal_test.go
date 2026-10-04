package client

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Duang777/drove/internal/auth"
	"github.com/Duang777/drove/internal/localipc"
	"github.com/Duang777/drove/internal/recording"
)

func TestOpenTerminalUsesLocalUnixSocket(t *testing.T) {
	dataDir, err := os.MkdirTemp("/tmp", "drove-terminal-client-")
	if err != nil {
		t.Fatalf("create data directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dataDir); err != nil {
			t.Errorf("remove data directory: %v", err)
		}
	})
	token, err := auth.Ensure(dataDir)
	if err != nil {
		t.Fatalf("ensure token: %v", err)
	}
	listener, err := localipc.Listen(dataDir)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	serverErrors := make(chan error, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		if r.Host != localipc.Authority {
			serverErrors <- fmt.Errorf("host = %q, want %q", r.Host, localipc.Authority)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			serverErrors <- fmt.Errorf("authorization = %q", r.Header.Get("Authorization"))
			return
		}
		upgrader := websocket.Upgrader{Subprotocols: []string{terminalProtocol}}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrors <- err
			return
		}
		defer conn.Close()
		if err := conn.WriteJSON(terminalHelloResponse{
			Version: terminalVersion,
			Type:    "hello",
		}); err != nil {
			serverErrors <- err
			return
		}
		_, _, _ = conn.ReadMessage()
	})}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
	})

	client := NewLocal(dataDir, WithTokenFile(auth.TokenPath(dataDir)))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream, err := client.OpenTerminal(ctx)
	if err != nil {
		t.Fatalf("open terminal over Unix socket: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("close terminal: %v", err)
	}
	select {
	case err := <-serverErrors:
		t.Fatal(err)
	default:
	}
}

func TestTerminalStreamCommandsAndApplyThenAdvance(t *testing.T) {
	serverErrors := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{
			Subprotocols: []string{terminalProtocol},
			CheckOrigin: func(*http.Request) bool {
				return true
			},
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrors <- err
			return
		}
		defer conn.Close()
		if err := conn.WriteJSON(terminalHelloResponse{
			Version: terminalVersion,
			Type:    "hello",
		}); err != nil {
			serverErrors <- err
			return
		}

		subscribe, err := readTerminalTestCommand(conn)
		if err != nil {
			serverErrors <- err
			return
		}
		if subscribe["type"] != "subscribe" ||
			subscribe["agent_id"] != "agent-1" ||
			subscribe["mode"] != "raw" ||
			subscribe["writable"] != true ||
			subscribe["rows"] != float64(50) ||
			subscribe["columns"] != float64(160) ||
			subscribe["offset"] != "3" {
			serverErrors <- errors.New("unexpected subscribe command")
			return
		}
		if err := conn.WriteJSON(map[string]any{
			"version":    2,
			"type":       "subscribed",
			"request_id": subscribe["request_id"],
			"agent_id":   "agent-1",
			"mode":       "raw",
			"cursor": map[string]string{
				"seq":         "1",
				"next_offset": "3",
			},
		}); err != nil {
			serverErrors <- err
			return
		}
		if err := conn.WriteMessage(
			websocket.BinaryMessage,
			terminalTestRawFrame("agent-1", 2, 3, []byte("abc"), false),
		); err != nil {
			serverErrors <- err
			return
		}

		input, err := readTerminalTestCommand(conn)
		if err != nil {
			serverErrors <- err
			return
		}
		if input["type"] != "input" || input["data"] != "continue\n" {
			serverErrors <- errors.New("unexpected input command")
			return
		}
		if err := conn.WriteJSON(map[string]any{
			"version":    2,
			"type":       "ack",
			"request_id": input["request_id"],
			"bytes":      len("continue\n"),
		}); err != nil {
			serverErrors <- err
			return
		}

		resize, err := readTerminalTestCommand(conn)
		if err != nil {
			serverErrors <- err
			return
		}
		if resize["type"] != "resize" ||
			resize["rows"] != float64(60) ||
			resize["columns"] != float64(180) {
			serverErrors <- errors.New("unexpected resize command")
			return
		}
		if err := conn.WriteJSON(map[string]any{
			"version":    2,
			"type":       "ack",
			"request_id": resize["request_id"],
		}); err != nil {
			serverErrors <- err
			return
		}

		unsubscribe, err := readTerminalTestCommand(conn)
		if err != nil {
			serverErrors <- err
			return
		}
		if unsubscribe["type"] != "unsubscribe" {
			serverErrors <- errors.New("unexpected unsubscribe command")
			return
		}
		if err := conn.WriteJSON(map[string]any{
			"version":    2,
			"type":       "unsubscribed",
			"request_id": unsubscribe["request_id"],
			"agent_id":   "agent-1",
			"mode":       "raw",
		}); err != nil {
			serverErrors <- err
			return
		}
		serverErrors <- nil
	}))
	defer server.Close()

	stream, err := New(strings.TrimPrefix(server.URL, "http://")).OpenTerminal(
		context.Background(),
	)
	if err != nil {
		t.Fatalf("open terminal: %v", err)
	}
	defer stream.Close()

	offset := recording.OutputOffset(3)
	if err := stream.Subscribe(context.Background(), TerminalSubscription{
		AgentID:  "agent-1",
		Mode:     TerminalModeRaw,
		Writable: true,
		Rows:     50,
		Columns:  160,
		Offset:   &offset,
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	cursor, ok := stream.Cursor("agent-1", TerminalModeRaw)
	if !ok || cursor != (recording.Cursor{Seq: 1, NextOffset: 3}) {
		t.Fatalf("initial cursor = %+v, %v", cursor, ok)
	}

	applyErr := errors.New("renderer failed")
	if err := stream.Next(context.Background(), func(message TerminalMessage) error {
		output, ok := message.(TerminalOutput)
		if !ok || !bytes.Equal(output.Data, []byte("abc")) {
			t.Fatalf("message = %+v, want terminal output", message)
		}
		return applyErr
	}); !errors.Is(err, applyErr) {
		t.Fatalf("first apply error = %v", err)
	}
	cursor, _ = stream.Cursor("agent-1", TerminalModeRaw)
	if cursor != (recording.Cursor{Seq: 1, NextOffset: 3}) {
		t.Fatalf("cursor advanced after failed apply: %+v", cursor)
	}

	if err := stream.Next(context.Background(), func(message TerminalMessage) error {
		output, ok := message.(TerminalOutput)
		if !ok || !bytes.Equal(output.Data, []byte("abc")) {
			t.Fatalf("retried message = %+v, want same terminal output", message)
		}
		return nil
	}); err != nil {
		t.Fatalf("retry apply: %v", err)
	}
	cursor, _ = stream.Cursor("agent-1", TerminalModeRaw)
	if cursor != (recording.Cursor{Seq: 2, NextOffset: 6}) {
		t.Fatalf("applied cursor = %+v", cursor)
	}

	written, err := stream.SendInput(context.Background(), "agent-1", "continue\n")
	if err != nil {
		t.Fatalf("send input: %v", err)
	}
	if written != len("continue\n") {
		t.Fatalf("written = %d", written)
	}
	if err := stream.Resize(context.Background(), "agent-1", 60, 180); err != nil {
		t.Fatalf("resize: %v", err)
	}
	if err := stream.Unsubscribe(
		context.Background(),
		"agent-1",
		TerminalModeRaw,
	); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	if _, ok := stream.Cursor("agent-1", TerminalModeRaw); ok {
		t.Fatal("cursor remained after unsubscribe")
	}

	select {
	case err := <-serverErrors:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("test server did not finish")
	}
}

func TestTerminalStreamRejectsMismatchedSubscriptionResponse(t *testing.T) {
	serverErrors := make(chan error, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{
			Subprotocols: []string{terminalProtocol},
			CheckOrigin: func(*http.Request) bool {
				return true
			},
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrors <- err
			return
		}
		defer conn.Close()
		if err := conn.WriteJSON(terminalHelloResponse{
			Version: terminalVersion,
			Type:    "hello",
		}); err != nil {
			serverErrors <- err
			return
		}
		subscribe, err := readTerminalTestCommand(conn)
		if err != nil {
			serverErrors <- err
			return
		}
		if err := conn.WriteJSON(map[string]any{
			"version":    2,
			"type":       "subscribed",
			"request_id": subscribe["request_id"],
			"agent_id":   "wrong-agent",
			"mode":       "raw",
			"cursor": map[string]string{
				"seq":         "0",
				"next_offset": "0",
			},
		}); err != nil {
			serverErrors <- err
			return
		}
		serverErrors <- nil
		<-release
	}))
	defer server.Close()
	defer close(release)

	stream, err := New(strings.TrimPrefix(server.URL, "http://")).OpenTerminal(
		context.Background(),
	)
	if err != nil {
		t.Fatalf("open terminal: %v", err)
	}
	defer stream.Close()

	err = stream.Subscribe(context.Background(), TerminalSubscription{
		AgentID: "agent-1",
		Mode:    TerminalModeRaw,
	})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("subscribe error = %v, want response mismatch", err)
	}
	if _, ok := stream.Cursor("agent-1", TerminalModeRaw); ok {
		t.Fatal("mismatched response advanced the requested subscription cursor")
	}
	if serverErr := <-serverErrors; serverErr != nil {
		t.Fatal(serverErr)
	}
}

func TestTerminalStreamCloseUnblocksPendingCommandAndNext(t *testing.T) {
	commandSeen := make(chan struct{})
	release := make(chan struct{})
	releaseServer := func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}
	serverErrors := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{
			Subprotocols: []string{terminalProtocol},
			CheckOrigin: func(*http.Request) bool {
				return true
			},
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrors <- err
			return
		}
		defer conn.Close()
		if err := conn.WriteJSON(terminalHelloResponse{
			Version: terminalVersion,
			Type:    "hello",
		}); err != nil {
			serverErrors <- err
			return
		}
		if _, err := readTerminalTestCommand(conn); err != nil {
			serverErrors <- err
			return
		}
		close(commandSeen)
		<-release
		serverErrors <- nil
	}))
	defer server.Close()
	defer releaseServer()

	stream, err := New(strings.TrimPrefix(server.URL, "http://")).OpenTerminal(
		context.Background(),
	)
	if err != nil {
		t.Fatalf("open terminal: %v", err)
	}
	subscribeResult := make(chan error, 1)
	go func() {
		subscribeResult <- stream.Subscribe(context.Background(), TerminalSubscription{
			AgentID: "agent-1",
			Mode:    TerminalModeRaw,
		})
	}()
	select {
	case <-commandSeen:
	case <-time.After(time.Second):
		t.Fatal("server did not receive terminal command")
	}

	if err := stream.Close(); err != nil {
		t.Fatalf("close terminal stream: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("close terminal stream again: %v", err)
	}
	select {
	case err := <-subscribeResult:
		if err == nil {
			t.Fatal("pending subscribe succeeded after close")
		}
	case <-time.After(time.Second):
		t.Fatal("close did not unblock pending subscribe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := stream.Next(ctx, func(TerminalMessage) error {
		return nil
	}); err == nil {
		t.Fatal("next succeeded after close")
	}

	releaseServer()
	if serverErr := <-serverErrors; serverErr != nil {
		t.Fatal(serverErr)
	}
}

func TestDecodeTerminalTextAndRawAreStrict(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{
			name:    "unknown field",
			payload: `{"version":2,"type":"caught_up","agent_id":"a1","mode":"raw","cursor":{"seq":"1","next_offset":"0"},"extra":true}`,
		},
		{
			name:    "numeric sequence",
			payload: `{"version":2,"type":"caught_up","agent_id":"a1","mode":"raw","cursor":{"seq":1,"next_offset":"0"}}`,
		},
		{
			name:    "restorable snapshot",
			payload: `{"version":2,"type":"snapshot","agent_id":"a1","cursor":{"seq":"1","next_offset":"0"},"rows":40,"columns":120,"lines":[],"truncated":false,"restorable":true,"captured_at":"2026-10-04T12:00:00Z"}`,
		},
		{
			name:    "raw subscription without cursor",
			payload: `{"version":2,"type":"subscribed","request_id":"r1","agent_id":"a1","mode":"raw"}`,
		},
		{
			name:    "event cursor mismatch",
			payload: `{"version":2,"type":"event","agent_id":"a1","event":{"seq":"2","timestamp":"2026-10-04T12:00:00Z","type":"output.chunk"},"cursor":{"seq":"1","next_offset":"0"},"historical":true}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := decodeTerminalText([]byte(test.payload)); err == nil {
				t.Fatal("decode succeeded")
			}
		})
	}

	frame := terminalTestRawFrame("agent-1", 2, 3, []byte("abc"), true)
	message, err := decodeTerminalRaw(frame)
	if err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	output, ok := message.(TerminalOutput)
	if !ok ||
		output.Cursor != (recording.Cursor{Seq: 2, NextOffset: 6}) ||
		!output.Historical ||
		!bytes.Equal(output.Data, []byte("abc")) {
		t.Fatalf("output = %+v", output)
	}
	frame[5] = 0x80
	if _, err := decodeTerminalRaw(frame); err == nil {
		t.Fatal("raw decoder accepted unknown flags")
	}
}

func TestTerminalStreamReturnsStructuredSubscriptionError(t *testing.T) {
	payload := []byte(
		`{"version":2,"type":"error","agent_id":"agent-1","mode":"raw",` +
			`"code":"output_expired","message":"expired",` +
			`"missing":[{"start":"3","end":"8"}]}`,
	)
	message, command, err := decodeTerminalText(payload)
	if err != nil {
		t.Fatalf("decode terminal error: %v", err)
	}
	if command != nil {
		t.Fatalf("command response = %+v", command)
	}
	typed, ok := message.(terminalErrorMessage)
	if !ok {
		t.Fatalf("message = %T, want terminalErrorMessage", message)
	}
	if typed.err.Code != "output_expired" ||
		len(typed.err.Missing) != 1 ||
		typed.err.Missing[0] != (recording.OutputRange{Start: 3, End: 8}) {
		t.Fatalf("terminal error = %+v", typed.err)
	}
}

func readTerminalTestCommand(conn *websocket.Conn) (map[string]any, error) {
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return nil, fmt.Errorf("set command read deadline: %w", err)
	}
	messageType, payload, err := conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("read terminal command: %w", err)
	}
	if messageType != websocket.TextMessage {
		return nil, fmt.Errorf("terminal command type = %d, want text", messageType)
	}
	var command map[string]any
	if err := json.Unmarshal(payload, &command); err != nil {
		return nil, fmt.Errorf("decode terminal command: %w", err)
	}
	return command, nil
}

func terminalTestRawFrame(
	agentID string,
	sequence uint64,
	offset uint64,
	data []byte,
	historical bool,
) []byte {
	frame := make([]byte, terminalRawHeaderBytes+len(agentID)+len(data))
	copy(frame[:4], "DRV2")
	frame[4] = terminalRawOutputKind
	if historical {
		frame[5] = terminalHistoricalFlag
	}
	binary.BigEndian.PutUint16(frame[6:8], uint16(len(agentID)))
	binary.BigEndian.PutUint64(frame[8:16], sequence)
	binary.BigEndian.PutUint64(frame[16:24], offset)
	copy(frame[24:], agentID)
	copy(frame[24+len(agentID):], data)
	return frame
}
