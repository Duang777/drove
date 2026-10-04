package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/session"
)

const (
	webSocketProtocolVersion = 1
	maxWebSocketRequestIDs   = 4096
	maxWebSocketIDBytes      = 128
	webSocketWriteTimeout    = 10 * time.Second
	webSocketPingInterval    = 30 * time.Second
)

type webSocketInput struct {
	Version   int    `json:"version"`
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	AgentID   string `json:"agent_id"`
	Data      string `json:"data"`
}

type webSocketAck struct {
	Version   int    `json:"version"`
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Bytes     int    `json:"bytes"`
}

type webSocketError struct {
	Version   int    `json:"version"`
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

type webSocketControl struct {
	messageType int
	payload     []byte
}

// handleWS negotiates the WebSocket protocol before the HTTP upgrade.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	protocols := websocket.Subprotocols(r)
	switch {
	case len(protocols) == 0:
		s.handleWebSocketV1(w, r)
	case len(protocols) == 1 && protocols[0] == webSocketV2Protocol:
		s.handleWebSocketV2(w, r)
	default:
		writeErr(w, http.StatusBadRequest, "unsupported WebSocket subprotocol")
	}
}

// handleWebSocketV1 serves the original event stream and input protocol.
func (s *Server) handleWebSocketV1(w http.ResponseWriter, r *http.Request) {
	grant := requestGrant(r)
	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 4096,
		CheckOrigin:     s.checkWebSocketOrigin,
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("ws upgrade failed", "err", err)
		return
	}
	defer conn.Close()
	closeCode := websocket.CloseNormalClosure
	closeReason := ""
	defer func() {
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(closeCode, closeReason),
			time.Now().Add(webSocketWriteTimeout),
		)
	}()
	conn.SetReadLimit(maxInputRequestBytes)

	buffer := s.opts.EventBuffer
	if buffer <= 0 {
		buffer = 1024
	}
	subscription := s.opts.Hub.Subscribe(buffer)
	defer s.opts.Hub.Unsubscribe(subscription)

	responses := make(chan any, 16)
	readerDone := make(chan struct{})
	writerDone := make(chan struct{})
	defer close(writerDone)
	conn.SetCloseHandler(func(int, string) error {
		return nil
	})
	conn.SetPingHandler(func(payload string) error {
		if queueWebSocketResponse(
			writerDone,
			responses,
			webSocketControl{messageType: websocket.PongMessage, payload: []byte(payload)},
		) {
			return nil
		}
		return errors.New("websocket writer closed")
	})
	go s.readWebSocket(conn, responses, readerDone, writerDone)

	if err := writeWebSocketText(conn, []byte(`{"type":"hello"}`)); err != nil {
		return
	}

	ping := time.NewTicker(webSocketPingInterval)
	defer ping.Stop()
	for {
		select {
		case <-grant.Done():
			closeCode = websocket.ClosePolicyViolation
			closeReason = "authorization expired"
			return
		case <-readerDone:
			return
		case response := <-responses:
			if err := writeWebSocketResponse(conn, response); err != nil {
				return
			}
		case ev, ok := <-subscription.C():
			if !ok {
				return
			}
			if err := writeWebSocketJSON(conn, ev); err != nil {
				return
			}
		case <-ping.C:
			if err := conn.WriteControl(
				websocket.PingMessage,
				nil,
				time.Now().Add(webSocketWriteTimeout),
			); err != nil {
				return
			}
		}
	}
}

func (s *Server) readWebSocket(
	conn *websocket.Conn,
	responses chan<- any,
	readerDone chan<- struct{},
	writerDone <-chan struct{},
) {
	defer close(readerDone)
	seenRequestIDs := make(map[string]struct{})
	for {
		messageType, payload, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if messageType != websocket.TextMessage {
			if !queueWebSocketResponse(
				writerDone,
				responses,
				newWebSocketError("", "invalid_message", "message must be UTF-8 JSON text"),
			) {
				return
			}
			continue
		}

		request, protocolErr := decodeWebSocketInput(payload)
		if protocolErr != nil {
			if !queueWebSocketResponse(writerDone, responses, *protocolErr) {
				return
			}
			continue
		}
		if _, duplicate := seenRequestIDs[request.RequestID]; duplicate {
			if !queueWebSocketResponse(
				writerDone,
				responses,
				newWebSocketError(
					request.RequestID,
					"duplicate_request_id",
					"request_id was already used on this connection",
				),
			) {
				return
			}
			continue
		}
		if len(seenRequestIDs) >= maxWebSocketRequestIDs {
			if !queueWebSocketResponse(
				writerDone,
				responses,
				newWebSocketError(
					request.RequestID,
					"request_limit",
					"connection request limit reached; reconnect before sending more input",
				),
			) {
				return
			}
			continue
		}
		seenRequestIDs[request.RequestID] = struct{}{}

		result, err := s.opts.Manager.SendInput(agent.ID(request.AgentID), []byte(request.Data))
		if err != nil {
			if !queueWebSocketResponse(
				writerDone,
				responses,
				webSocketInputError(request.RequestID, err),
			) {
				return
			}
			continue
		}
		if !queueWebSocketResponse(writerDone, responses, webSocketAck{
			Version:   webSocketProtocolVersion,
			Type:      "ack",
			RequestID: request.RequestID,
			Bytes:     result.BytesWritten,
		}) {
			return
		}
	}
}

func decodeWebSocketInput(payload []byte) (webSocketInput, *webSocketError) {
	if !utf8.Valid(payload) {
		response := newWebSocketError("", "invalid_utf8", session.ErrInputNotUTF8.Error())
		return webSocketInput{}, &response
	}

	var request *webSocketInput
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		response := newWebSocketError("", "invalid_message", "invalid JSON message: "+err.Error())
		return webSocketInput{}, &response
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		response := newWebSocketError("", "invalid_message", "message must contain one JSON object")
		return webSocketInput{}, &response
	}
	if request == nil {
		response := newWebSocketError("", "invalid_message", "message must be a JSON object")
		return webSocketInput{}, &response
	}
	if request.Version != webSocketProtocolVersion {
		response := newWebSocketError(
			validResponseRequestID(request.RequestID),
			"unsupported_version",
			"version must be 1",
		)
		return webSocketInput{}, &response
	}
	if request.Type != "input" {
		response := newWebSocketError(
			validResponseRequestID(request.RequestID),
			"unsupported_type",
			"type must be input",
		)
		return webSocketInput{}, &response
	}
	if !validWebSocketID(request.RequestID) {
		response := newWebSocketError("", "invalid_request_id", "request_id is invalid")
		return webSocketInput{}, &response
	}
	if !validWebSocketID(request.AgentID) {
		response := newWebSocketError(
			request.RequestID,
			"invalid_agent_id",
			"agent_id is invalid",
		)
		return webSocketInput{}, &response
	}
	return *request, nil
}

func webSocketInputError(requestID string, err error) webSocketError {
	code := "internal_error"
	switch {
	case errors.Is(err, session.ErrInputEmpty):
		code = "empty_input"
	case errors.Is(err, session.ErrInputTooLarge):
		code = "input_too_large"
	case errors.Is(err, session.ErrInputNotUTF8):
		code = "invalid_utf8"
	case errors.Is(err, session.ErrUnknownAgent):
		code = "unknown_agent"
	case errors.Is(err, session.ErrNotAttached):
		code = "not_attached"
	case errors.Is(err, session.ErrManagerClosed):
		code = "manager_closed"
	case errors.Is(err, session.ErrInputBackpressure):
		code = "input_backpressure"
	case errors.Is(err, session.ErrInputWrite):
		code = "write_failed"
	case errors.Is(err, session.ErrInputAudit):
		code = "audit_failed"
	}
	return newWebSocketError(requestID, code, err.Error())
}

func newWebSocketError(requestID, code, message string) webSocketError {
	return webSocketError{
		Version:   webSocketProtocolVersion,
		Type:      "error",
		RequestID: requestID,
		Code:      code,
		Message:   message,
	}
}

func queueWebSocketResponse(done <-chan struct{}, responses chan<- any, response any) bool {
	select {
	case responses <- response:
		return true
	case <-done:
		return false
	}
}

func writeWebSocketJSON(conn *websocket.Conn, message any) error {
	conn.SetWriteDeadline(time.Now().Add(webSocketWriteTimeout))
	return conn.WriteJSON(message)
}

func writeWebSocketResponse(conn *websocket.Conn, response any) error {
	if control, ok := response.(webSocketControl); ok {
		return conn.WriteControl(
			control.messageType,
			control.payload,
			time.Now().Add(webSocketWriteTimeout),
		)
	}
	return writeWebSocketJSON(conn, response)
}

func writeWebSocketText(conn *websocket.Conn, payload []byte) error {
	conn.SetWriteDeadline(time.Now().Add(webSocketWriteTimeout))
	return conn.WriteMessage(websocket.TextMessage, payload)
}

func (s *Server) checkWebSocketOrigin(r *http.Request) bool {
	return s.originAllowed(r)
}

func validResponseRequestID(requestID string) string {
	if validWebSocketID(requestID) {
		return requestID
	}
	return ""
}

func validWebSocketID(value string) bool {
	if len(value) == 0 || len(value) > maxWebSocketIDBytes {
		return false
	}
	for i := range len(value) {
		char := value[i]
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '-' ||
			char == '_' ||
			char == '.' ||
			char == ':' {
			continue
		}
		return false
	}
	return true
}
