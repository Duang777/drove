package client

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/recording"
)

const (
	terminalProtocol       = "drove.v2"
	terminalVersion        = 2
	terminalRawHeaderBytes = 24
	terminalRawOutputKind  = 1
	terminalHistoricalFlag = 1
	terminalMessageBuffer  = 64
	terminalCommandTimeout = 10 * time.Second
	terminalMaxIDBytes     = 128
)

// TerminalMode identifies one v2 terminal subscription.
type TerminalMode string

const (
	// TerminalModeRaw streams retained output bytes and durable resizes.
	TerminalModeRaw TerminalMode = "raw"
	// TerminalModeEvents streams immutable event envelopes.
	TerminalModeEvents TerminalMode = "events"
	// TerminalModeSnapshot streams coalesced live terminal views.
	TerminalModeSnapshot TerminalMode = "snapshot"
)

// TerminalSubscription configures one agent and mode subscription.
type TerminalSubscription struct {
	AgentID  string
	Mode     TerminalMode
	Writable bool
	Rows     int
	Columns  int
	Cursor   *recording.Cursor
	Sequence *recording.Seq
	Offset   *recording.OutputOffset
}

// TerminalMessage is a decoded v2 stream message.
type TerminalMessage interface {
	terminalMessage()
	terminalKey() terminalSubscriptionKey
	appliedCursor() (recording.Cursor, bool)
}

// TerminalOutput carries one raw output chunk or selected suffix.
type TerminalOutput struct {
	AgentID    string
	Sequence   recording.Seq
	Offset     recording.OutputOffset
	Data       []byte
	Historical bool
	Cursor     recording.Cursor
}

func (TerminalOutput) terminalMessage() {}

func (m TerminalOutput) terminalKey() terminalSubscriptionKey {
	return terminalSubscriptionKey{agentID: m.AgentID, mode: TerminalModeRaw}
}

func (m TerminalOutput) appliedCursor() (recording.Cursor, bool) {
	return m.Cursor, true
}

// TerminalResize carries one durable effective terminal resize.
type TerminalResize struct {
	AgentID      string
	Sequence     recording.Seq
	Rows         uint16
	Columns      uint16
	OutputOffset recording.OutputOffset
	Cursor       recording.Cursor
	Historical   bool
}

func (TerminalResize) terminalMessage() {}

func (m TerminalResize) terminalKey() terminalSubscriptionKey {
	return terminalSubscriptionKey{agentID: m.AgentID, mode: TerminalModeRaw}
}

func (m TerminalResize) appliedCursor() (recording.Cursor, bool) {
	return m.Cursor, true
}

// TerminalEventEnvelope is the decimal-safe v2 event representation.
type TerminalEventEnvelope struct {
	Sequence  recording.Seq `json:"seq"`
	Timestamp time.Time     `json:"timestamp"`
	Type      event.Type    `json:"type"`
	AgentID   string        `json:"agent_id,omitempty"`
	SessionID string        `json:"session_id,omitempty"`
	From      string        `json:"from,omitempty"`
	To        string        `json:"to,omitempty"`
	Reason    string        `json:"reason,omitempty"`
	Payload   string        `json:"payload,omitempty"`
}

// TerminalEvent carries one immutable event envelope.
type TerminalEvent struct {
	AgentID    string
	Event      TerminalEventEnvelope
	Cursor     recording.Cursor
	Historical bool
}

func (TerminalEvent) terminalMessage() {}

func (m TerminalEvent) terminalKey() terminalSubscriptionKey {
	return terminalSubscriptionKey{agentID: m.AgentID, mode: TerminalModeEvents}
}

func (m TerminalEvent) appliedCursor() (recording.Cursor, bool) {
	return m.Cursor, true
}

// TerminalSnapshot carries one bounded live-only terminal view.
type TerminalSnapshot struct {
	AgentID    string
	Cursor     recording.Cursor
	Rows       uint16
	Columns    uint16
	Lines      []string
	Truncated  bool
	Restorable bool
	CapturedAt time.Time
}

func (TerminalSnapshot) terminalMessage() {}

func (m TerminalSnapshot) terminalKey() terminalSubscriptionKey {
	return terminalSubscriptionKey{agentID: m.AgentID, mode: TerminalModeSnapshot}
}

func (m TerminalSnapshot) appliedCursor() (recording.Cursor, bool) {
	return m.Cursor, true
}

// TerminalCaughtUp marks the captured history boundary for one subscription.
type TerminalCaughtUp struct {
	AgentID string
	Mode    TerminalMode
	Cursor  recording.Cursor
}

func (TerminalCaughtUp) terminalMessage() {}

func (m TerminalCaughtUp) terminalKey() terminalSubscriptionKey {
	return terminalSubscriptionKey{agentID: m.AgentID, mode: m.Mode}
}

func (m TerminalCaughtUp) appliedCursor() (recording.Cursor, bool) {
	return m.Cursor, true
}

// TerminalStreamError is a structured v2 protocol or subscription error.
type TerminalStreamError struct {
	RequestID     string
	AgentID       string
	Mode          TerminalMode
	Code          string
	Message       string
	Missing       []recording.OutputRange
	Subscriptions []TerminalResumeCursor
}

func (e *TerminalStreamError) Error() string {
	if e == nil {
		return "client: terminal stream error"
	}
	return fmt.Sprintf("client: terminal stream %s: %s", e.Code, e.Message)
}

// TerminalResumeCursor is the last server-written cursor for one subscription.
type TerminalResumeCursor struct {
	AgentID string
	Mode    TerminalMode
	Cursor  recording.Cursor
}

type terminalErrorMessage struct {
	err *TerminalStreamError
}

func (terminalErrorMessage) terminalMessage() {}

func (m terminalErrorMessage) terminalKey() terminalSubscriptionKey {
	return terminalSubscriptionKey{agentID: m.err.AgentID, mode: m.err.Mode}
}

func (terminalErrorMessage) appliedCursor() (recording.Cursor, bool) {
	return recording.Cursor{}, false
}

type terminalSubscriptionKey struct {
	agentID string
	mode    TerminalMode
}

type terminalPendingResult struct {
	messageType string
	agentID     string
	mode        TerminalMode
	cursor      *recording.Cursor
	bytes       *int
	err         error
}

// TerminalStream is one negotiated v2 WebSocket connection.
type TerminalStream struct {
	conn *websocket.Conn

	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[string]chan terminalPendingResult

	cursorMu sync.RWMutex
	cursors  map[terminalSubscriptionKey]recording.Cursor

	consumeMu sync.Mutex
	unapplied TerminalMessage
	messages  chan TerminalMessage
	done      chan struct{}

	errMu          sync.RWMutex
	err            error
	failOnce       sync.Once
	connectionOnce sync.Once
}

// OpenTerminal negotiates a drove.v2 WebSocket connection.
func (c *Client) OpenTerminal(ctx context.Context) (*TerminalStream, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint, err := terminalWebSocketURL(c.baseURL)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("client: create terminal request: %w", err)
	}
	if err := c.authorize(request, false); err != nil {
		return nil, err
	}
	dialer := *websocket.DefaultDialer
	dialer.Subprotocols = []string{terminalProtocol}
	if c.netDialContext != nil {
		dialer.Proxy = nil
		dialer.NetDialContext = c.netDialContext
	}
	conn, response, err := dialer.DialContext(ctx, endpoint, request.Header)
	if err != nil {
		if response != nil {
			defer response.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
			return nil, fmt.Errorf(
				"client: terminal upgrade failed with %s: %s",
				response.Status,
				strings.TrimSpace(string(body)),
			)
		}
		return nil, fmt.Errorf("%w: terminal WebSocket: %v", ErrDaemonUnreachable, err)
	}
	if conn.Subprotocol() != terminalProtocol {
		_ = conn.Close()
		return nil, fmt.Errorf(
			"client: terminal subprotocol %q, want %q",
			conn.Subprotocol(),
			terminalProtocol,
		)
	}
	if err := readTerminalHello(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}

	stream := &TerminalStream{
		conn:     conn,
		pending:  make(map[string]chan terminalPendingResult),
		cursors:  make(map[terminalSubscriptionKey]recording.Cursor),
		messages: make(chan TerminalMessage, terminalMessageBuffer),
		done:     make(chan struct{}),
	}
	go stream.read()
	return stream, nil
}

// Subscribe registers one agent and stream mode.
func (s *TerminalStream) Subscribe(
	ctx context.Context,
	subscription TerminalSubscription,
) error {
	request, err := newTerminalSubscribeRequest(subscription)
	if err != nil {
		return err
	}
	result, err := s.command(ctx, request)
	if err != nil {
		return err
	}
	if result.messageType != "subscribed" {
		return fmt.Errorf(
			"client: terminal subscribe received %q",
			result.messageType,
		)
	}
	if result.agentID != subscription.AgentID || result.mode != subscription.Mode {
		return errors.New("client: terminal subscribe response does not match the request")
	}
	if result.cursor != nil {
		s.cursorMu.Lock()
		s.cursors[terminalSubscriptionKey{
			agentID: subscription.AgentID,
			mode:    subscription.Mode,
		}] = *result.cursor
		s.cursorMu.Unlock()
	}
	return nil
}

// Unsubscribe removes one agent and stream mode.
func (s *TerminalStream) Unsubscribe(
	ctx context.Context,
	agentID string,
	mode TerminalMode,
) error {
	if !validTerminalID(agentID) {
		return errors.New("client: terminal agent ID is invalid")
	}
	if !validTerminalMode(mode) {
		return errors.New("client: terminal mode is invalid")
	}
	result, err := s.command(ctx, terminalUnsubscribeRequest{
		Version:   terminalVersion,
		Type:      "unsubscribe",
		RequestID: uuid.NewString(),
		AgentID:   agentID,
		Mode:      mode,
	})
	if err != nil {
		return err
	}
	if result.messageType != "unsubscribed" {
		return fmt.Errorf(
			"client: terminal unsubscribe received %q",
			result.messageType,
		)
	}
	if result.agentID != agentID || result.mode != mode {
		return errors.New("client: terminal unsubscribe response does not match the request")
	}
	s.cursorMu.Lock()
	delete(s.cursors, terminalSubscriptionKey{agentID: agentID, mode: mode})
	s.cursorMu.Unlock()
	return nil
}

// SendInput writes UTF-8 input through a writable raw subscription.
func (s *TerminalStream) SendInput(
	ctx context.Context,
	agentID string,
	data string,
) (int, error) {
	if !validTerminalID(agentID) {
		return 0, errors.New("client: terminal agent ID is invalid")
	}
	if !utf8.ValidString(data) {
		return 0, errors.New("client: terminal input is not valid UTF-8")
	}
	result, err := s.command(ctx, terminalInputRequest{
		Version:   terminalVersion,
		Type:      "input",
		RequestID: uuid.NewString(),
		AgentID:   agentID,
		Data:      data,
	})
	if err != nil {
		return 0, err
	}
	if result.messageType != "ack" || result.bytes == nil {
		return 0, errors.New("client: terminal input acknowledgement omitted bytes")
	}
	return *result.bytes, nil
}

// Resize updates the viewport of a writable raw subscription.
func (s *TerminalStream) Resize(
	ctx context.Context,
	agentID string,
	rows int,
	columns int,
) error {
	if !validTerminalID(agentID) {
		return errors.New("client: terminal agent ID is invalid")
	}
	if rows <= 0 || columns <= 0 || rows > math.MaxUint16 || columns > math.MaxUint16 {
		return errors.New("client: terminal size must be in 1..65535")
	}
	result, err := s.command(ctx, terminalResizeRequest{
		Version:   terminalVersion,
		Type:      "resize",
		RequestID: uuid.NewString(),
		AgentID:   agentID,
		Rows:      rows,
		Columns:   columns,
	})
	if err != nil {
		return err
	}
	if result.messageType != "ack" || result.bytes != nil {
		return errors.New("client: invalid terminal resize acknowledgement")
	}
	return nil
}

// Next decodes one stream message, applies it, then advances its resume cursor.
func (s *TerminalStream) Next(
	ctx context.Context,
	apply func(TerminalMessage) error,
) error {
	if apply == nil {
		return errors.New("client: terminal apply callback is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.consumeMu.Lock()
	defer s.consumeMu.Unlock()
	if s.unapplied == nil {
		message, err := s.nextMessage(ctx)
		if err != nil {
			return err
		}
		s.unapplied = message
	}
	message := s.unapplied
	if protocolError, ok := message.(terminalErrorMessage); ok {
		s.unapplied = nil
		return protocolError.err
	}
	if err := apply(message); err != nil {
		return err
	}
	if cursor, ok := message.appliedCursor(); ok {
		s.cursorMu.Lock()
		s.cursors[message.terminalKey()] = cursor
		s.cursorMu.Unlock()
	}
	s.unapplied = nil
	return nil
}

// Cursor returns the last successfully applied cursor for one subscription.
func (s *TerminalStream) Cursor(
	agentID string,
	mode TerminalMode,
) (recording.Cursor, bool) {
	s.cursorMu.RLock()
	defer s.cursorMu.RUnlock()
	cursor, ok := s.cursors[terminalSubscriptionKey{agentID: agentID, mode: mode}]
	return cursor, ok
}

// Close closes the v2 connection and fails pending commands.
func (s *TerminalStream) Close() error {
	if s == nil {
		return nil
	}
	var closeErr error
	s.connectionOnce.Do(func() {
		closeErr = s.conn.Close()
	})
	s.fail(io.EOF)
	return closeErr
}

type terminalSubscribeRequest struct {
	Version   int                     `json:"version"`
	Type      string                  `json:"type"`
	RequestID string                  `json:"request_id"`
	AgentID   string                  `json:"agent_id"`
	Mode      TerminalMode            `json:"mode"`
	Writable  *bool                   `json:"writable,omitempty"`
	Rows      *int                    `json:"rows,omitempty"`
	Columns   *int                    `json:"columns,omitempty"`
	Cursor    *recording.Cursor       `json:"cursor,omitempty"`
	Sequence  *recording.Seq          `json:"seq,omitempty"`
	Offset    *recording.OutputOffset `json:"offset,omitempty"`
}

type terminalUnsubscribeRequest struct {
	Version   int          `json:"version"`
	Type      string       `json:"type"`
	RequestID string       `json:"request_id"`
	AgentID   string       `json:"agent_id"`
	Mode      TerminalMode `json:"mode"`
}

type terminalInputRequest struct {
	Version   int    `json:"version"`
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	AgentID   string `json:"agent_id"`
	Data      string `json:"data"`
}

type terminalResizeRequest struct {
	Version   int    `json:"version"`
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	AgentID   string `json:"agent_id"`
	Rows      int    `json:"rows"`
	Columns   int    `json:"columns"`
}

type terminalResponseHeader struct {
	Version   int    `json:"version"`
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
}

type terminalHelloResponse struct {
	Version int    `json:"version"`
	Type    string `json:"type"`
}

type terminalSubscribedResponse struct {
	Version   int               `json:"version"`
	Type      string            `json:"type"`
	RequestID string            `json:"request_id"`
	AgentID   string            `json:"agent_id"`
	Mode      TerminalMode      `json:"mode"`
	Cursor    *recording.Cursor `json:"cursor,omitempty"`
}

type terminalUnsubscribedResponse struct {
	Version   int          `json:"version"`
	Type      string       `json:"type"`
	RequestID string       `json:"request_id"`
	AgentID   string       `json:"agent_id"`
	Mode      TerminalMode `json:"mode"`
}

type terminalAckResponse struct {
	Version   int    `json:"version"`
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Bytes     *int   `json:"bytes,omitempty"`
}

type terminalErrorResponse struct {
	Version       int                     `json:"version"`
	Type          string                  `json:"type"`
	RequestID     string                  `json:"request_id,omitempty"`
	AgentID       string                  `json:"agent_id,omitempty"`
	Mode          TerminalMode            `json:"mode,omitempty"`
	Code          string                  `json:"code"`
	Message       string                  `json:"message"`
	Missing       []recording.OutputRange `json:"missing,omitempty"`
	Subscriptions []TerminalResumeCursor  `json:"subscriptions,omitempty"`
}

type terminalEventResponse struct {
	Version    int                   `json:"version"`
	Type       string                `json:"type"`
	AgentID    string                `json:"agent_id"`
	Event      TerminalEventEnvelope `json:"event"`
	Cursor     recording.Cursor      `json:"cursor"`
	Historical bool                  `json:"historical"`
}

type terminalResizeResponse struct {
	Version      int                    `json:"version"`
	Type         string                 `json:"type"`
	AgentID      string                 `json:"agent_id"`
	Sequence     recording.Seq          `json:"seq"`
	Rows         uint16                 `json:"rows"`
	Columns      uint16                 `json:"columns"`
	OutputOffset recording.OutputOffset `json:"output_offset"`
	Cursor       recording.Cursor       `json:"cursor"`
	Historical   bool                   `json:"historical"`
}

type terminalSnapshotResponse struct {
	Version    int              `json:"version"`
	Type       string           `json:"type"`
	AgentID    string           `json:"agent_id"`
	Cursor     recording.Cursor `json:"cursor"`
	Rows       uint16           `json:"rows"`
	Columns    uint16           `json:"columns"`
	Lines      []string         `json:"lines"`
	Truncated  bool             `json:"truncated"`
	Restorable bool             `json:"restorable"`
	CapturedAt time.Time        `json:"captured_at"`
}

type terminalCaughtUpResponse struct {
	Version int              `json:"version"`
	Type    string           `json:"type"`
	AgentID string           `json:"agent_id"`
	Mode    TerminalMode     `json:"mode"`
	Cursor  recording.Cursor `json:"cursor"`
}

func newTerminalSubscribeRequest(
	subscription TerminalSubscription,
) (terminalSubscribeRequest, error) {
	if !validTerminalID(subscription.AgentID) {
		return terminalSubscribeRequest{}, errors.New(
			"client: terminal agent ID is invalid",
		)
	}
	if !validTerminalMode(subscription.Mode) {
		return terminalSubscribeRequest{}, errors.New(
			"client: terminal mode is invalid",
		)
	}
	selectorCount := 0
	if subscription.Cursor != nil {
		selectorCount++
	}
	if subscription.Sequence != nil {
		selectorCount++
	}
	if subscription.Offset != nil {
		selectorCount++
	}
	if selectorCount > 1 {
		return terminalSubscribeRequest{}, errors.New(
			"client: terminal subscription accepts at most one selector",
		)
	}
	if subscription.Cursor != nil {
		if err := subscription.Cursor.Validate(); err != nil {
			return terminalSubscribeRequest{}, err
		}
	}
	if subscription.Mode == TerminalModeSnapshot &&
		(selectorCount != 0 ||
			subscription.Writable ||
			subscription.Rows != 0 ||
			subscription.Columns != 0) {
		return terminalSubscribeRequest{}, errors.New(
			"client: snapshot subscription is live-only and read-only",
		)
	}
	if subscription.Mode == TerminalModeEvents &&
		(subscription.Writable ||
			subscription.Rows != 0 ||
			subscription.Columns != 0) {
		return terminalSubscribeRequest{}, errors.New(
			"client: event subscription is read-only",
		)
	}
	if subscription.Mode == TerminalModeRaw {
		if (subscription.Rows == 0) != (subscription.Columns == 0) {
			return terminalSubscribeRequest{}, errors.New(
				"client: terminal rows and columns must be provided together",
			)
		}
		if !subscription.Writable &&
			(subscription.Rows != 0 || subscription.Columns != 0) {
			return terminalSubscribeRequest{}, errors.New(
				"client: terminal viewport requires a writable subscription",
			)
		}
		if subscription.Rows < 0 ||
			subscription.Columns < 0 ||
			subscription.Rows > math.MaxUint16 ||
			subscription.Columns > math.MaxUint16 {
			return terminalSubscribeRequest{}, errors.New(
				"client: terminal size must be in 1..65535",
			)
		}
	}

	request := terminalSubscribeRequest{
		Version:   terminalVersion,
		Type:      "subscribe",
		RequestID: uuid.NewString(),
		AgentID:   subscription.AgentID,
		Mode:      subscription.Mode,
		Cursor:    subscription.Cursor,
		Sequence:  subscription.Sequence,
		Offset:    subscription.Offset,
	}
	if subscription.Mode == TerminalModeRaw && subscription.Writable {
		writable := true
		request.Writable = &writable
		if subscription.Rows != 0 {
			rows := subscription.Rows
			columns := subscription.Columns
			request.Rows = &rows
			request.Columns = &columns
		}
	}
	return request, nil
}

func (s *TerminalStream) command(
	ctx context.Context,
	request any,
) (terminalPendingResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	requestID, err := terminalRequestID(request)
	if err != nil {
		return terminalPendingResult{}, err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return terminalPendingResult{}, fmt.Errorf(
			"client: encode terminal command: %w",
			err,
		)
	}
	result := make(chan terminalPendingResult, 1)
	s.pendingMu.Lock()
	select {
	case <-s.done:
		s.pendingMu.Unlock()
		return terminalPendingResult{}, s.failure()
	default:
	}
	s.pending[requestID] = result
	s.pendingMu.Unlock()

	s.writeMu.Lock()
	err = s.conn.SetWriteDeadline(time.Now().Add(terminalCommandTimeout))
	if err == nil {
		err = s.conn.WriteMessage(websocket.TextMessage, payload)
	}
	s.writeMu.Unlock()
	if err != nil {
		s.removePending(requestID)
		s.fail(err)
		return terminalPendingResult{}, fmt.Errorf(
			"client: write terminal command: %w",
			err,
		)
	}

	select {
	case response := <-result:
		if response.err != nil {
			return terminalPendingResult{}, response.err
		}
		return response, nil
	case <-ctx.Done():
		s.removePending(requestID)
		return terminalPendingResult{}, fmt.Errorf(
			"client: wait for terminal command %q: %w",
			requestID,
			ctx.Err(),
		)
	case <-s.done:
		s.removePending(requestID)
		return terminalPendingResult{}, s.failure()
	}
}

func terminalRequestID(request any) (string, error) {
	switch typed := request.(type) {
	case terminalSubscribeRequest:
		return typed.RequestID, nil
	case terminalUnsubscribeRequest:
		return typed.RequestID, nil
	case terminalInputRequest:
		return typed.RequestID, nil
	case terminalResizeRequest:
		return typed.RequestID, nil
	default:
		return "", fmt.Errorf("client: unsupported terminal command %T", request)
	}
}

func (s *TerminalStream) read() {
	for {
		messageType, payload, err := s.conn.ReadMessage()
		if err != nil {
			s.fail(err)
			return
		}
		var message TerminalMessage
		switch messageType {
		case websocket.TextMessage:
			response, commandResult, decodeErr := decodeTerminalText(payload)
			if decodeErr != nil {
				s.fail(decodeErr)
				return
			}
			if commandResult != nil {
				if !s.resolvePending(commandResult.requestID, commandResult.result) {
					s.fail(fmt.Errorf(
						"client: unexpected terminal response %q",
						commandResult.requestID,
					))
					return
				}
				continue
			}
			message = response
		case websocket.BinaryMessage:
			message, err = decodeTerminalRaw(payload)
			if err != nil {
				s.fail(err)
				return
			}
		default:
			s.fail(fmt.Errorf(
				"client: unexpected terminal WebSocket message type %d",
				messageType,
			))
			return
		}
		select {
		case s.messages <- message:
		case <-s.done:
			return
		}
	}
}

type terminalCommandResult struct {
	requestID string
	result    terminalPendingResult
}

func decodeTerminalText(
	payload []byte,
) (TerminalMessage, *terminalCommandResult, error) {
	if !utf8.Valid(payload) {
		return nil, nil, errors.New("client: terminal text frame is not valid UTF-8")
	}
	var header *terminalResponseHeader
	if err := json.Unmarshal(payload, &header); err != nil || header == nil {
		return nil, nil, errors.New("client: terminal text frame is not one JSON object")
	}
	if header.Version != terminalVersion {
		return nil, nil, fmt.Errorf(
			"client: terminal response version %d is unsupported",
			header.Version,
		)
	}
	switch header.Type {
	case "subscribed":
		var response terminalSubscribedResponse
		if err := decodeStrictTerminalJSON(payload, &response); err != nil {
			return nil, nil, err
		}
		if !validTerminalID(response.AgentID) ||
			!validTerminalMode(response.Mode) ||
			response.RequestID == "" {
			return nil, nil, errors.New("client: invalid subscribed response")
		}
		if (response.Mode == TerminalModeSnapshot) != (response.Cursor == nil) {
			return nil, nil, errors.New("client: invalid subscribed cursor")
		}
		return nil, &terminalCommandResult{
			requestID: response.RequestID,
			result: terminalPendingResult{
				messageType: response.Type,
				agentID:     response.AgentID,
				mode:        response.Mode,
				cursor:      response.Cursor,
			},
		}, nil
	case "unsubscribed":
		var response terminalUnsubscribedResponse
		if err := decodeStrictTerminalJSON(payload, &response); err != nil {
			return nil, nil, err
		}
		if !validTerminalID(response.AgentID) ||
			!validTerminalMode(response.Mode) ||
			response.RequestID == "" {
			return nil, nil, errors.New("client: invalid unsubscribed response")
		}
		return nil, &terminalCommandResult{
			requestID: response.RequestID,
			result: terminalPendingResult{
				messageType: response.Type,
				agentID:     response.AgentID,
				mode:        response.Mode,
			},
		}, nil
	case "ack":
		var response terminalAckResponse
		if err := decodeStrictTerminalJSON(payload, &response); err != nil {
			return nil, nil, err
		}
		if response.RequestID == "" {
			return nil, nil, errors.New("client: terminal ack omitted request_id")
		}
		return nil, &terminalCommandResult{
			requestID: response.RequestID,
			result: terminalPendingResult{
				messageType: response.Type,
				bytes:       response.Bytes,
			},
		}, nil
	case "error":
		var response terminalErrorResponse
		if err := decodeStrictTerminalJSON(payload, &response); err != nil {
			return nil, nil, err
		}
		if response.Code == "" || response.Message == "" {
			return nil, nil, errors.New("client: terminal error omitted code or message")
		}
		protocolError := &TerminalStreamError{
			RequestID:     response.RequestID,
			AgentID:       response.AgentID,
			Mode:          response.Mode,
			Code:          response.Code,
			Message:       response.Message,
			Missing:       append([]recording.OutputRange(nil), response.Missing...),
			Subscriptions: append([]TerminalResumeCursor(nil), response.Subscriptions...),
		}
		if response.RequestID != "" {
			return nil, &terminalCommandResult{
				requestID: response.RequestID,
				result: terminalPendingResult{
					messageType: response.Type,
					err:         protocolError,
				},
			}, nil
		}
		return terminalErrorMessage{err: protocolError}, nil, nil
	case "event":
		var response terminalEventResponse
		if err := decodeStrictTerminalJSON(payload, &response); err != nil {
			return nil, nil, err
		}
		if !validTerminalID(response.AgentID) ||
			response.Event.Sequence == 0 ||
			response.Event.Timestamp.IsZero() ||
			response.Event.Type == "" ||
			response.Cursor.Seq != response.Event.Sequence {
			return nil, nil, errors.New("client: invalid terminal event response")
		}
		return TerminalEvent{
			AgentID:    response.AgentID,
			Event:      response.Event,
			Cursor:     response.Cursor,
			Historical: response.Historical,
		}, nil, nil
	case "resized":
		var response terminalResizeResponse
		if err := decodeStrictTerminalJSON(payload, &response); err != nil {
			return nil, nil, err
		}
		if !validTerminalID(response.AgentID) ||
			response.Rows == 0 ||
			response.Columns == 0 ||
			response.Cursor.Seq != response.Sequence ||
			response.Cursor.NextOffset != response.OutputOffset {
			return nil, nil, errors.New("client: invalid terminal resize response")
		}
		return TerminalResize{
			AgentID:      response.AgentID,
			Sequence:     response.Sequence,
			Rows:         response.Rows,
			Columns:      response.Columns,
			OutputOffset: response.OutputOffset,
			Cursor:       response.Cursor,
			Historical:   response.Historical,
		}, nil, nil
	case "snapshot":
		var response terminalSnapshotResponse
		if err := decodeStrictTerminalJSON(payload, &response); err != nil {
			return nil, nil, err
		}
		if !validTerminalID(response.AgentID) ||
			response.Rows == 0 ||
			response.Columns == 0 ||
			response.Restorable {
			return nil, nil, errors.New("client: invalid terminal snapshot response")
		}
		return TerminalSnapshot{
			AgentID:    response.AgentID,
			Cursor:     response.Cursor,
			Rows:       response.Rows,
			Columns:    response.Columns,
			Lines:      append([]string(nil), response.Lines...),
			Truncated:  response.Truncated,
			Restorable: response.Restorable,
			CapturedAt: response.CapturedAt,
		}, nil, nil
	case "caught_up":
		var response terminalCaughtUpResponse
		if err := decodeStrictTerminalJSON(payload, &response); err != nil {
			return nil, nil, err
		}
		if !validTerminalID(response.AgentID) ||
			(response.Mode != TerminalModeRaw && response.Mode != TerminalModeEvents) {
			return nil, nil, errors.New("client: invalid terminal caught-up response")
		}
		return TerminalCaughtUp{
			AgentID: response.AgentID,
			Mode:    response.Mode,
			Cursor:  response.Cursor,
		}, nil, nil
	default:
		return nil, nil, fmt.Errorf(
			"client: unsupported terminal response type %q",
			header.Type,
		)
	}
}

func decodeTerminalRaw(payload []byte) (TerminalMessage, error) {
	if len(payload) <= terminalRawHeaderBytes {
		return nil, errors.New("client: terminal raw frame is truncated")
	}
	if string(payload[:4]) != "DRV2" {
		return nil, errors.New("client: terminal raw frame has invalid magic")
	}
	if payload[4] != terminalRawOutputKind {
		return nil, fmt.Errorf(
			"client: terminal raw frame kind %d is unsupported",
			payload[4],
		)
	}
	if payload[5]&^byte(terminalHistoricalFlag) != 0 {
		return nil, fmt.Errorf(
			"client: terminal raw frame flags %#x are unsupported",
			payload[5],
		)
	}
	agentLength := int(binary.BigEndian.Uint16(payload[6:8]))
	if agentLength == 0 ||
		agentLength > terminalMaxIDBytes ||
		len(payload) <= terminalRawHeaderBytes+agentLength {
		return nil, errors.New("client: terminal raw frame has invalid agent length")
	}
	agentID := string(payload[24 : 24+agentLength])
	if !validTerminalID(agentID) {
		return nil, errors.New("client: terminal raw frame has invalid agent_id")
	}
	sequence := binary.BigEndian.Uint64(payload[8:16])
	offset := binary.BigEndian.Uint64(payload[16:24])
	data := payload[24+agentLength:]
	if len(data) > event.MaxOutputChunkBytes {
		return nil, errors.New("client: terminal raw frame exceeds output chunk limit")
	}
	if offset > math.MaxUint64-uint64(len(data)) {
		return nil, errors.New("client: terminal raw frame cursor overflows")
	}
	cursor, err := recording.NewCursor(
		recording.Seq(sequence),
		recording.OutputOffset(offset+uint64(len(data))),
	)
	if err != nil {
		return nil, fmt.Errorf("client: terminal raw frame cursor: %w", err)
	}
	return TerminalOutput{
		AgentID:    agentID,
		Sequence:   recording.Seq(sequence),
		Offset:     recording.OutputOffset(offset),
		Data:       append([]byte(nil), data...),
		Historical: payload[5]&terminalHistoricalFlag != 0,
		Cursor:     cursor,
	}, nil
}

func readTerminalHello(conn *websocket.Conn) error {
	if err := conn.SetReadDeadline(time.Now().Add(terminalCommandTimeout)); err != nil {
		return fmt.Errorf("client: set terminal hello deadline: %w", err)
	}
	messageType, payload, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("client: read terminal hello: %w", err)
	}
	if messageType != websocket.TextMessage {
		return errors.New("client: terminal hello must be a text frame")
	}
	var hello terminalHelloResponse
	if err := decodeStrictTerminalJSON(payload, &hello); err != nil {
		return err
	}
	if hello.Version != terminalVersion || hello.Type != "hello" {
		return fmt.Errorf("client: invalid terminal hello %q", payload)
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return fmt.Errorf("client: clear terminal hello deadline: %w", err)
	}
	return nil
}

func decodeStrictTerminalJSON(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("client: decode terminal message: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("client: terminal message must contain one JSON object")
	}
	return nil
}

func (s *TerminalStream) nextMessage(ctx context.Context) (TerminalMessage, error) {
	select {
	case message := <-s.messages:
		return message, nil
	default:
	}
	select {
	case message := <-s.messages:
		return message, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("client: wait for terminal message: %w", ctx.Err())
	case <-s.done:
		return nil, s.failure()
	}
}

func (s *TerminalStream) resolvePending(
	requestID string,
	result terminalPendingResult,
) bool {
	s.pendingMu.Lock()
	pending, ok := s.pending[requestID]
	if ok {
		delete(s.pending, requestID)
	}
	s.pendingMu.Unlock()
	if !ok {
		return false
	}
	pending <- result
	return true
}

func (s *TerminalStream) removePending(requestID string) {
	s.pendingMu.Lock()
	delete(s.pending, requestID)
	s.pendingMu.Unlock()
}

func (s *TerminalStream) fail(err error) {
	s.failOnce.Do(func() {
		if err == nil {
			err = io.EOF
		}
		s.connectionOnce.Do(func() {
			_ = s.conn.Close()
		})
		s.errMu.Lock()
		s.err = err
		s.errMu.Unlock()
		close(s.done)

		s.pendingMu.Lock()
		pending := s.pending
		s.pending = make(map[string]chan terminalPendingResult)
		s.pendingMu.Unlock()
		for _, result := range pending {
			result <- terminalPendingResult{err: err}
		}
	})
}

func (s *TerminalStream) failure() error {
	s.errMu.RLock()
	defer s.errMu.RUnlock()
	if s.err == nil {
		return io.EOF
	}
	return s.err
}

func terminalWebSocketURL(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("client: parse daemon URL: %w", err)
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	default:
		return "", fmt.Errorf("client: unsupported daemon URL scheme %q", parsed.Scheme)
	}
	parsed.Path = "/ws"
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func validTerminalID(value string) bool {
	if len(value) == 0 || len(value) > terminalMaxIDBytes {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' ||
			character == '_' ||
			character == '.' ||
			character == ':' {
			continue
		}
		return false
	}
	return true
}

func validTerminalMode(mode TerminalMode) bool {
	switch mode {
	case TerminalModeRaw, TerminalModeEvents, TerminalModeSnapshot:
		return true
	default:
		return false
	}
}
