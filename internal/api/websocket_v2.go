package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/recording"
	"github.com/Duang777/drove/internal/session"
)

const (
	webSocketV2Protocol       = "drove.v2"
	webSocketV2Version        = 2
	webSocketV2QueueBytes     = 8 << 20
	webSocketV2CommandBuffer  = 32
	webSocketV2FailureBuffer  = 64
	webSocketV2RawHeaderBytes = 24
	webSocketV2RawOutputKind  = 1
	webSocketV2HistoricalFlag = 1
	webSocketV2TryAgainLater  = 1013
)

type webSocketV2Mode string

const (
	webSocketV2ModeRaw      webSocketV2Mode = "raw"
	webSocketV2ModeEvents   webSocketV2Mode = "events"
	webSocketV2ModeSnapshot webSocketV2Mode = "snapshot"
)

type webSocketV2Command interface {
	requestID() string
}

type webSocketV2Subscribe struct {
	Version   int                     `json:"version"`
	Type      string                  `json:"type"`
	RequestID string                  `json:"request_id"`
	AgentID   string                  `json:"agent_id"`
	Mode      webSocketV2Mode         `json:"mode"`
	Writable  *bool                   `json:"writable,omitempty"`
	Rows      *int                    `json:"rows,omitempty"`
	Columns   *int                    `json:"columns,omitempty"`
	Cursor    *recording.Cursor       `json:"cursor,omitempty"`
	Sequence  *recording.Seq          `json:"seq,omitempty"`
	Offset    *recording.OutputOffset `json:"offset,omitempty"`
}

func (c webSocketV2Subscribe) requestID() string { return c.RequestID }

type webSocketV2Unsubscribe struct {
	Version   int             `json:"version"`
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	AgentID   string          `json:"agent_id"`
	Mode      webSocketV2Mode `json:"mode"`
}

func (c webSocketV2Unsubscribe) requestID() string { return c.RequestID }

type webSocketV2Input struct {
	Version   int    `json:"version"`
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	AgentID   string `json:"agent_id"`
	Data      string `json:"data"`
}

func (c webSocketV2Input) requestID() string { return c.RequestID }

type webSocketV2Resize struct {
	Version   int    `json:"version"`
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	AgentID   string `json:"agent_id"`
	Rows      int    `json:"rows"`
	Columns   int    `json:"columns"`
}

func (c webSocketV2Resize) requestID() string { return c.RequestID }

type webSocketV2Hello struct {
	Version int    `json:"version"`
	Type    string `json:"type"`
}

type webSocketV2Subscribed struct {
	Version   int               `json:"version"`
	Type      string            `json:"type"`
	RequestID string            `json:"request_id"`
	AgentID   string            `json:"agent_id"`
	Mode      webSocketV2Mode   `json:"mode"`
	Cursor    *recording.Cursor `json:"cursor,omitempty"`
}

type webSocketV2Unsubscribed struct {
	Version   int             `json:"version"`
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	AgentID   string          `json:"agent_id"`
	Mode      webSocketV2Mode `json:"mode"`
}

type webSocketV2Ack struct {
	Version   int    `json:"version"`
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Bytes     *int   `json:"bytes,omitempty"`
}

type webSocketV2ResumeCursor struct {
	AgentID string           `json:"agent_id"`
	Mode    webSocketV2Mode  `json:"mode"`
	Cursor  recording.Cursor `json:"cursor"`
}

type webSocketV2Error struct {
	Version       int                       `json:"version"`
	Type          string                    `json:"type"`
	RequestID     string                    `json:"request_id,omitempty"`
	AgentID       string                    `json:"agent_id,omitempty"`
	Mode          webSocketV2Mode           `json:"mode,omitempty"`
	Code          string                    `json:"code"`
	Message       string                    `json:"message"`
	Missing       []recording.OutputRange   `json:"missing,omitempty"`
	Subscriptions []webSocketV2ResumeCursor `json:"subscriptions,omitempty"`
}

type webSocketV2EventEnvelope struct {
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

type webSocketV2Event struct {
	Version    int                      `json:"version"`
	Type       string                   `json:"type"`
	AgentID    string                   `json:"agent_id"`
	Event      webSocketV2EventEnvelope `json:"event"`
	Cursor     recording.Cursor         `json:"cursor"`
	Historical bool                     `json:"historical"`
}

type webSocketV2Resized struct {
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

type webSocketV2Snapshot struct {
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

type webSocketV2CaughtUp struct {
	Version int              `json:"version"`
	Type    string           `json:"type"`
	AgentID string           `json:"agent_id"`
	Mode    webSocketV2Mode  `json:"mode"`
	Cursor  recording.Cursor `json:"cursor"`
}

type webSocketV2Incoming struct {
	command     webSocketV2Command
	protocolErr *webSocketV2Error
}

type webSocketV2SubscriptionKey struct {
	agentID string
	mode    webSocketV2Mode
}

type webSocketV2Subscription struct {
	key        webSocketV2SubscriptionKey
	writable   bool
	cancel     context.CancelFunc
	attachment *session.TerminalAttachment
	raw        *recording.RawTail
	events     *recording.EventTail
	snapshots  <-chan session.LiveSnapshot
	queue      *webSocketV2Queue
	failures   chan<- webSocketV2SubscriptionFailure

	writtenMu   sync.RWMutex
	lastWritten recording.Cursor
	closeOnce   sync.Once
	closed      atomic.Bool
}

type webSocketV2SubscriptionFailure struct {
	subscription *webSocketV2Subscription
	err          error
}

type webSocketV2Frame struct {
	messageType  int
	payload      []byte
	subscription *webSocketV2Subscription
	cursor       *recording.Cursor
}

type webSocketV2Queue struct {
	mu       sync.Mutex
	budget   int
	bytes    int
	frames   []webSocketV2Frame
	disabled map[*webSocketV2Subscription]struct{}
	overflow bool
	reserved *webSocketV2Frame
	ready    chan struct{}
	overrun  chan struct{}
}

func newWebSocketV2Queue(budget int) *webSocketV2Queue {
	return &webSocketV2Queue{
		budget:   budget,
		disabled: make(map[*webSocketV2Subscription]struct{}),
		ready:    make(chan struct{}, 1),
		overrun:  make(chan struct{}, 1),
	}
}

func (q *webSocketV2Queue) Enqueue(frame webSocketV2Frame) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.overflow {
		return false
	}
	if frame.subscription != nil {
		if _, disabled := q.disabled[frame.subscription]; disabled {
			return false
		}
	}
	if len(frame.payload) > q.budget-q.bytes {
		q.overflow = true
		clear(q.frames)
		q.frames = nil
		q.bytes = 0
		select {
		case q.overrun <- struct{}{}:
		default:
		}
		return false
	}
	q.frames = append(q.frames, frame)
	q.bytes += len(frame.payload)
	select {
	case q.ready <- struct{}{}:
	default:
	}
	return true
}

func (q *webSocketV2Queue) Pop() (webSocketV2Frame, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.reserved != nil {
		frame := *q.reserved
		q.reserved = nil
		return frame, true
	}
	if len(q.frames) == 0 {
		return webSocketV2Frame{}, false
	}
	frame := q.frames[0]
	q.frames[0] = webSocketV2Frame{}
	q.frames = q.frames[1:]
	q.bytes -= len(frame.payload)
	if len(q.frames) != 0 {
		select {
		case q.ready <- struct{}{}:
		default:
		}
	}
	return frame, true
}

func (q *webSocketV2Queue) Disable(subscription *webSocketV2Subscription) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.disabled[subscription] = struct{}{}
	kept := q.frames[:0]
	q.bytes = 0
	for _, frame := range q.frames {
		if frame.subscription == subscription {
			continue
		}
		kept = append(kept, frame)
		q.bytes += len(frame.payload)
	}
	clear(q.frames[len(kept):])
	q.frames = kept
}

func (q *webSocketV2Queue) ReserveControl(frame webSocketV2Frame) {
	q.mu.Lock()
	defer q.mu.Unlock()
	clear(q.frames)
	q.frames = nil
	q.bytes = 0
	q.reserved = &frame
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

func (q *webSocketV2Queue) DisableAll(
	subscriptions map[webSocketV2SubscriptionKey]*webSocketV2Subscription,
) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, subscription := range subscriptions {
		q.disabled[subscription] = struct{}{}
	}
	clear(q.frames)
	q.frames = nil
	q.bytes = 0
}

func (q *webSocketV2Queue) Ready() <-chan struct{} {
	return q.ready
}

func (q *webSocketV2Queue) Overrun() <-chan struct{} {
	return q.overrun
}

func (q *webSocketV2Queue) Overflowed() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.overflow
}

func (s *Server) handleWebSocketV2(w http.ResponseWriter, r *http.Request) {
	grant := requestGrant(r)
	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 4096,
		Subprotocols:    []string{webSocketV2Protocol},
		CheckOrigin:     s.checkWebSocketOrigin,
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	connection := newWebSocketV2Connection(s, conn, grant.Done())
	connection.run()
}

type webSocketV2Connection struct {
	server        *Server
	conn          *websocket.Conn
	ctx           context.Context
	cancel        context.CancelFunc
	queue         *webSocketV2Queue
	incoming      chan webSocketV2Incoming
	readerDone    chan struct{}
	failures      chan webSocketV2SubscriptionFailure
	subscriptions map[webSocketV2SubscriptionKey]*webSocketV2Subscription
	slowClosed    bool
	authClosed    bool
	authDone      <-chan struct{}
}

func newWebSocketV2Connection(
	server *Server,
	conn *websocket.Conn,
	authDone <-chan struct{},
) *webSocketV2Connection {
	ctx, cancel := context.WithCancel(context.Background())
	queueBudget := server.webSocketV2QueueBudget
	if queueBudget <= 0 {
		queueBudget = webSocketV2QueueBytes
	}
	return &webSocketV2Connection{
		server:        server,
		conn:          conn,
		ctx:           ctx,
		cancel:        cancel,
		queue:         newWebSocketV2Queue(queueBudget),
		incoming:      make(chan webSocketV2Incoming, webSocketV2CommandBuffer),
		readerDone:    make(chan struct{}),
		failures:      make(chan webSocketV2SubscriptionFailure, webSocketV2FailureBuffer),
		subscriptions: make(map[webSocketV2SubscriptionKey]*webSocketV2Subscription),
		authDone:      authDone,
	}
}

func (c *webSocketV2Connection) run() {
	defer func() {
		if c.queue.Overflowed() && !c.slowClosed {
			c.closeSlowConsumer()
		}
		c.cancel()
		c.closeSubscriptions()
		if !c.slowClosed && !c.authClosed {
			_ = c.conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
				time.Now().Add(webSocketWriteTimeout),
			)
		}
	}()

	c.conn.SetReadLimit(maxInputRequestBytes)
	c.conn.SetCloseHandler(func(int, string) error {
		return nil
	})
	c.conn.SetPingHandler(func(payload string) error {
		c.queue.Enqueue(webSocketV2Frame{
			messageType: websocket.PongMessage,
			payload:     []byte(payload),
		})
		return nil
	})
	go c.read()

	hello, err := marshalWebSocketV2(webSocketV2Hello{
		Version: webSocketV2Version,
		Type:    "hello",
	})
	if err != nil || c.writeFrame(webSocketV2Frame{
		messageType: websocket.TextMessage,
		payload:     hello,
	}) != nil {
		return
	}

	ping := time.NewTicker(webSocketPingInterval)
	defer ping.Stop()
	for {
		select {
		case <-c.authDone:
			c.closeAuthorizationExpired()
			return
		case <-c.readerDone:
			return
		case incoming := <-c.incoming:
			if err := c.handleIncoming(incoming); err != nil {
				return
			}
		case <-c.queue.Ready():
			frame, ok := c.queue.Pop()
			if !ok {
				continue
			}
			if err := c.writeFrame(frame); err != nil {
				return
			}
		case <-c.queue.Overrun():
			c.closeSlowConsumer()
			return
		case failure := <-c.failures:
			if err := c.handleSubscriptionFailure(failure); err != nil {
				return
			}
		case <-ping.C:
			if err := c.writeFrame(webSocketV2Frame{
				messageType: websocket.PingMessage,
			}); err != nil {
				return
			}
		}
	}
}

func (c *webSocketV2Connection) closeAuthorizationExpired() {
	_ = c.conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(
			websocket.ClosePolicyViolation,
			"authorization expired",
		),
		time.Now().Add(webSocketWriteTimeout),
	)
	c.authClosed = true
}

func (c *webSocketV2Connection) read() {
	defer close(c.readerDone)
	seenRequestIDs := make(map[string]struct{})
	for {
		messageType, payload, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		if messageType != websocket.TextMessage {
			if !c.sendIncoming(webSocketV2Incoming{
				protocolErr: newWebSocketV2Error(
					"",
					"invalid_message",
					"message must be UTF-8 JSON text",
				),
			}) {
				return
			}
			continue
		}
		command, protocolErr := decodeWebSocketV2Command(payload)
		if protocolErr != nil {
			if !c.sendIncoming(webSocketV2Incoming{protocolErr: protocolErr}) {
				return
			}
			continue
		}
		requestID := command.requestID()
		if _, duplicate := seenRequestIDs[requestID]; duplicate {
			if !c.sendIncoming(webSocketV2Incoming{
				protocolErr: newWebSocketV2Error(
					requestID,
					"duplicate_request_id",
					"request_id was already used on this connection",
				),
			}) {
				return
			}
			continue
		}
		if len(seenRequestIDs) >= maxWebSocketRequestIDs {
			if !c.sendIncoming(webSocketV2Incoming{
				protocolErr: newWebSocketV2Error(
					requestID,
					"request_limit",
					"connection request limit reached; reconnect before sending more commands",
				),
			}) {
				return
			}
			continue
		}
		seenRequestIDs[requestID] = struct{}{}
		if !c.sendIncoming(webSocketV2Incoming{command: command}) {
			return
		}
	}
}

func (c *webSocketV2Connection) sendIncoming(incoming webSocketV2Incoming) bool {
	select {
	case c.incoming <- incoming:
		return true
	case <-c.ctx.Done():
		return false
	}
}

func (c *webSocketV2Connection) handleIncoming(
	incoming webSocketV2Incoming,
) error {
	if incoming.protocolErr != nil {
		return c.writeJSON(incoming.protocolErr, nil, nil)
	}
	switch command := incoming.command.(type) {
	case webSocketV2Subscribe:
		return c.subscribe(command)
	case webSocketV2Unsubscribe:
		return c.unsubscribe(command)
	case webSocketV2Input:
		return c.input(command)
	case webSocketV2Resize:
		return c.resize(command)
	default:
		return c.writeJSON(
			newWebSocketV2Error("", "invalid_message", "unknown command"),
			nil,
			nil,
		)
	}
}

func (c *webSocketV2Connection) subscribe(command webSocketV2Subscribe) error {
	key := webSocketV2SubscriptionKey{agentID: command.AgentID, mode: command.Mode}
	if _, exists := c.subscriptions[key]; exists {
		return c.writeJSON(newWebSocketV2ErrorForSubscription(
			command.RequestID,
			command.AgentID,
			command.Mode,
			"duplicate_subscription",
			"agent and mode are already subscribed on this connection",
		), nil, nil)
	}

	ctx, cancel := context.WithCancel(c.ctx)
	subscription := &webSocketV2Subscription{
		key:      key,
		writable: command.Writable != nil && *command.Writable,
		cancel:   cancel,
		queue:    c.queue,
		failures: c.failures,
	}
	var startCursor *recording.Cursor
	switch command.Mode {
	case webSocketV2ModeRaw:
		if command.Writable != nil {
			mode := session.AttachmentReadOnly
			if subscription.writable {
				mode = session.AttachmentWritable
			}
			options := session.AttachmentOptions{
				Purpose: session.AttachmentPurposeUser,
				Mode:    mode,
			}
			if command.Rows != nil {
				options.Rows = *command.Rows
				options.Columns = *command.Columns
			}
			attachment, err := c.server.opts.Manager.AttachTerminal(
				ctx,
				agent.ID(command.AgentID),
				options,
			)
			if err != nil {
				cancel()
				return c.writeJSON(webSocketV2OperationError(
					command.RequestID,
					command.AgentID,
					command.Mode,
					err,
				), nil, nil)
			}
			subscription.attachment = attachment
		}
		tail, err := c.server.opts.Manager.TailRaw(
			ctx,
			command.AgentID,
			command.selector(),
		)
		if err != nil {
			if subscription.attachment != nil {
				_ = subscription.attachment.Close()
			}
			cancel()
			return c.writeJSON(webSocketV2OperationError(
				command.RequestID,
				command.AgentID,
				command.Mode,
				err,
			), nil, nil)
		}
		subscription.raw = tail
		cursor := tail.StartCursor()
		startCursor = &cursor
	case webSocketV2ModeEvents:
		tail, err := c.server.opts.Manager.TailEvents(
			ctx,
			command.AgentID,
			command.selector(),
		)
		if err != nil {
			cancel()
			return c.writeJSON(webSocketV2OperationError(
				command.RequestID,
				command.AgentID,
				command.Mode,
				err,
			), nil, nil)
		}
		subscription.events = tail
		cursor := tail.StartCursor()
		startCursor = &cursor
	case webSocketV2ModeSnapshot:
		attachment, err := c.server.opts.Manager.AttachTerminal(
			ctx,
			agent.ID(command.AgentID),
			session.AttachmentOptions{
				Purpose: session.AttachmentPurposeRecording,
				Mode:    session.AttachmentReadOnly,
			},
		)
		if err != nil {
			cancel()
			return c.writeJSON(webSocketV2OperationError(
				command.RequestID,
				command.AgentID,
				command.Mode,
				err,
			), nil, nil)
		}
		subscription.attachment = attachment
		snapshots, err := attachment.WatchSnapshots(ctx)
		if err != nil {
			_ = attachment.Close()
			cancel()
			return c.writeJSON(webSocketV2OperationError(
				command.RequestID,
				command.AgentID,
				command.Mode,
				err,
			), nil, nil)
		}
		subscription.snapshots = snapshots
	default:
		cancel()
		return c.writeJSON(
			newWebSocketV2Error(command.RequestID, "invalid_mode", "unsupported subscription mode"),
			nil,
			nil,
		)
	}

	response := webSocketV2Subscribed{
		Version:   webSocketV2Version,
		Type:      "subscribed",
		RequestID: command.RequestID,
		AgentID:   command.AgentID,
		Mode:      command.Mode,
		Cursor:    startCursor,
	}
	if err := c.writeJSON(response, subscription, startCursor); err != nil {
		subscription.Close()
		return err
	}
	c.subscriptions[key] = subscription
	subscription.Start(ctx)
	return nil
}

func (c *webSocketV2Connection) unsubscribe(command webSocketV2Unsubscribe) error {
	key := webSocketV2SubscriptionKey{agentID: command.AgentID, mode: command.Mode}
	subscription, exists := c.subscriptions[key]
	if !exists {
		return c.writeJSON(newWebSocketV2ErrorForSubscription(
			command.RequestID,
			command.AgentID,
			command.Mode,
			"not_subscribed",
			"agent and mode are not subscribed on this connection",
		), nil, nil)
	}
	delete(c.subscriptions, key)
	subscription.Close()
	c.queue.Disable(subscription)
	return c.writeJSON(webSocketV2Unsubscribed{
		Version:   webSocketV2Version,
		Type:      "unsubscribed",
		RequestID: command.RequestID,
		AgentID:   command.AgentID,
		Mode:      command.Mode,
	}, nil, nil)
}

func (c *webSocketV2Connection) input(command webSocketV2Input) error {
	subscription, ok := c.writableRaw(command.AgentID)
	if !ok {
		return c.writeJSON(newWebSocketV2ErrorForSubscription(
			command.RequestID,
			command.AgentID,
			webSocketV2ModeRaw,
			"not_writable",
			"input requires a writable raw subscription",
		), nil, nil)
	}
	result, err := subscription.attachment.SendInput(c.ctx, []byte(command.Data))
	if err != nil {
		return c.writeJSON(webSocketV2OperationError(
			command.RequestID,
			command.AgentID,
			webSocketV2ModeRaw,
			err,
		), nil, nil)
	}
	return c.writeJSON(webSocketV2Ack{
		Version:   webSocketV2Version,
		Type:      "ack",
		RequestID: command.RequestID,
		Bytes:     &result.BytesWritten,
	}, nil, nil)
}

func (c *webSocketV2Connection) resize(command webSocketV2Resize) error {
	subscription, ok := c.writableRaw(command.AgentID)
	if !ok {
		return c.writeJSON(newWebSocketV2ErrorForSubscription(
			command.RequestID,
			command.AgentID,
			webSocketV2ModeRaw,
			"not_writable",
			"resize requires a writable raw subscription",
		), nil, nil)
	}
	if err := subscription.attachment.Resize(
		c.ctx,
		command.Rows,
		command.Columns,
	); err != nil {
		return c.writeJSON(webSocketV2OperationError(
			command.RequestID,
			command.AgentID,
			webSocketV2ModeRaw,
			err,
		), nil, nil)
	}
	return c.writeJSON(webSocketV2Ack{
		Version:   webSocketV2Version,
		Type:      "ack",
		RequestID: command.RequestID,
	}, nil, nil)
}

func (c *webSocketV2Connection) writableRaw(
	agentID string,
) (*webSocketV2Subscription, bool) {
	subscription, ok := c.subscriptions[webSocketV2SubscriptionKey{
		agentID: agentID,
		mode:    webSocketV2ModeRaw,
	}]
	return subscription, ok && subscription.writable && subscription.attachment != nil
}

func (c *webSocketV2Connection) handleSubscriptionFailure(
	failure webSocketV2SubscriptionFailure,
) error {
	subscription := failure.subscription
	current, exists := c.subscriptions[subscription.key]
	if !exists || current != subscription {
		return nil
	}
	delete(c.subscriptions, subscription.key)
	subscription.Close()
	c.queue.Disable(subscription)
	response := webSocketV2OperationError(
		"",
		subscription.key.agentID,
		subscription.key.mode,
		failure.err,
	)
	return c.writeJSON(response, nil, nil)
}

func (c *webSocketV2Connection) closeSlowConsumer() {
	resume := make([]webSocketV2ResumeCursor, 0, len(c.subscriptions))
	for _, subscription := range c.subscriptions {
		resume = append(resume, webSocketV2ResumeCursor{
			AgentID: subscription.key.agentID,
			Mode:    subscription.key.mode,
			Cursor:  subscription.LastWritten(),
		})
		subscription.Close()
	}
	sort.Slice(resume, func(i, j int) bool {
		if resume[i].AgentID == resume[j].AgentID {
			return resume[i].Mode < resume[j].Mode
		}
		return resume[i].AgentID < resume[j].AgentID
	})
	c.queue.DisableAll(c.subscriptions)
	clear(c.subscriptions)

	payload, err := marshalWebSocketV2(webSocketV2Error{
		Version:       webSocketV2Version,
		Type:          "error",
		Code:          "slow_consumer",
		Message:       "connection could not keep up with terminal streams",
		Subscriptions: resume,
	})
	if err == nil {
		c.queue.ReserveControl(webSocketV2Frame{
			messageType: websocket.TextMessage,
			payload:     payload,
		})
		if frame, ok := c.queue.Pop(); ok {
			_ = c.writeFrame(frame)
		}
	}
	_ = c.conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(
			webSocketV2TryAgainLater,
			"slow_consumer",
		),
		time.Now().Add(webSocketWriteTimeout),
	)
	c.slowClosed = true
}

func (c *webSocketV2Connection) closeSubscriptions() {
	for _, subscription := range c.subscriptions {
		subscription.Close()
	}
	clear(c.subscriptions)
}

func (c *webSocketV2Connection) writeJSON(
	message any,
	subscription *webSocketV2Subscription,
	cursor *recording.Cursor,
) error {
	payload, err := marshalWebSocketV2(message)
	if err != nil {
		return err
	}
	return c.writeFrame(webSocketV2Frame{
		messageType:  websocket.TextMessage,
		payload:      payload,
		subscription: subscription,
		cursor:       cloneCursor(cursor),
	})
}

func (c *webSocketV2Connection) writeFrame(frame webSocketV2Frame) error {
	c.conn.SetWriteDeadline(time.Now().Add(webSocketWriteTimeout))
	var err error
	if frame.messageType == websocket.PingMessage ||
		frame.messageType == websocket.PongMessage ||
		frame.messageType == websocket.CloseMessage {
		err = c.conn.WriteControl(
			frame.messageType,
			frame.payload,
			time.Now().Add(webSocketWriteTimeout),
		)
	} else {
		err = c.conn.WriteMessage(frame.messageType, frame.payload)
	}
	if err != nil {
		return err
	}
	if frame.subscription != nil && frame.cursor != nil {
		frame.subscription.SetLastWritten(*frame.cursor)
	}
	return nil
}

func (s *webSocketV2Subscription) Start(ctx context.Context) {
	switch s.key.mode {
	case webSocketV2ModeRaw:
		go s.produceRaw(ctx)
	case webSocketV2ModeEvents:
		go s.produceEvents(ctx)
	case webSocketV2ModeSnapshot:
		go s.produceSnapshots(ctx)
	}
}

func (s *webSocketV2Subscription) produceRaw(ctx context.Context) {
	for {
		item, err := s.raw.Next()
		if err != nil {
			s.fail(ctx, err)
			return
		}
		var frame webSocketV2Frame
		switch typed := item.(type) {
		case recording.RawOutput:
			payload, encodeErr := encodeWebSocketV2Raw(typed)
			if encodeErr != nil {
				s.fail(ctx, encodeErr)
				return
			}
			cursor := typed.Cursor
			frame = webSocketV2Frame{
				messageType:  websocket.BinaryMessage,
				payload:      payload,
				subscription: s,
				cursor:       &cursor,
			}
		case recording.Resize:
			payload, encodeErr := marshalWebSocketV2(webSocketV2Resized{
				Version:      webSocketV2Version,
				Type:         "resized",
				AgentID:      typed.AgentID,
				Sequence:     typed.Sequence,
				Rows:         typed.Rows,
				Columns:      typed.Columns,
				OutputOffset: typed.OutputOffset,
				Cursor:       typed.Cursor,
				Historical:   typed.Historical,
			})
			if encodeErr != nil {
				s.fail(ctx, encodeErr)
				return
			}
			cursor := typed.Cursor
			frame = webSocketV2Frame{
				messageType:  websocket.TextMessage,
				payload:      payload,
				subscription: s,
				cursor:       &cursor,
			}
		case recording.CaughtUp:
			payload, encodeErr := marshalWebSocketV2(webSocketV2CaughtUp{
				Version: webSocketV2Version,
				Type:    "caught_up",
				AgentID: s.key.agentID,
				Mode:    s.key.mode,
				Cursor:  typed.Cursor,
			})
			if encodeErr != nil {
				s.fail(ctx, encodeErr)
				return
			}
			cursor := typed.Cursor
			frame = webSocketV2Frame{
				messageType:  websocket.TextMessage,
				payload:      payload,
				subscription: s,
				cursor:       &cursor,
			}
		default:
			s.fail(ctx, fmt.Errorf("api: unknown raw tail item %T", item))
			return
		}
		if !s.enqueue(frame) {
			return
		}
	}
}

func (s *webSocketV2Subscription) produceEvents(ctx context.Context) {
	for {
		item, err := s.events.Next()
		if err != nil {
			s.fail(ctx, err)
			return
		}
		var frame webSocketV2Frame
		switch typed := item.(type) {
		case recording.EventRecord:
			payload, encodeErr := marshalWebSocketV2(webSocketV2Event{
				Version: webSocketV2Version,
				Type:    "event",
				AgentID: s.key.agentID,
				Event: webSocketV2EventEnvelope{
					Sequence:  recording.Seq(typed.Event.Seq),
					Timestamp: typed.Event.Timestamp,
					Type:      typed.Event.Type,
					AgentID:   typed.Event.AgentID,
					SessionID: typed.Event.SessionID,
					From:      typed.Event.From,
					To:        typed.Event.To,
					Reason:    typed.Event.Reason,
					Payload:   typed.Event.Payload,
				},
				Cursor:     typed.Cursor,
				Historical: typed.Historical,
			})
			if encodeErr != nil {
				s.fail(ctx, encodeErr)
				return
			}
			cursor := typed.Cursor
			frame = webSocketV2Frame{
				messageType:  websocket.TextMessage,
				payload:      payload,
				subscription: s,
				cursor:       &cursor,
			}
		case recording.CaughtUp:
			payload, encodeErr := marshalWebSocketV2(webSocketV2CaughtUp{
				Version: webSocketV2Version,
				Type:    "caught_up",
				AgentID: s.key.agentID,
				Mode:    s.key.mode,
				Cursor:  typed.Cursor,
			})
			if encodeErr != nil {
				s.fail(ctx, encodeErr)
				return
			}
			cursor := typed.Cursor
			frame = webSocketV2Frame{
				messageType:  websocket.TextMessage,
				payload:      payload,
				subscription: s,
				cursor:       &cursor,
			}
		default:
			s.fail(ctx, fmt.Errorf("api: unknown event tail item %T", item))
			return
		}
		if !s.enqueue(frame) {
			return
		}
	}
}

func (s *webSocketV2Subscription) produceSnapshots(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case snapshot, ok := <-s.snapshots:
			if !ok {
				return
			}
			payload, err := marshalWebSocketV2(webSocketV2Snapshot{
				Version:    webSocketV2Version,
				Type:       "snapshot",
				AgentID:    s.key.agentID,
				Cursor:     snapshot.Cursor,
				Rows:       snapshot.Rows,
				Columns:    snapshot.Columns,
				Lines:      snapshot.Lines,
				Truncated:  snapshot.Truncated,
				Restorable: snapshot.Restorable,
				CapturedAt: snapshot.CapturedAt,
			})
			if err != nil {
				s.fail(ctx, err)
				return
			}
			cursor := snapshot.Cursor
			if !s.enqueue(webSocketV2Frame{
				messageType:  websocket.TextMessage,
				payload:      payload,
				subscription: s,
				cursor:       &cursor,
			}) {
				return
			}
		}
	}
}

func (s *webSocketV2Subscription) enqueue(frame webSocketV2Frame) bool {
	if s.closed.Load() {
		return false
	}
	return s.queue.Enqueue(frame)
}

func (s *webSocketV2Subscription) fail(ctx context.Context, err error) {
	if errors.Is(err, io.EOF) ||
		errors.Is(err, context.Canceled) ||
		s.closed.Load() {
		return
	}
	select {
	case s.failures <- webSocketV2SubscriptionFailure{
		subscription: s,
		err:          err,
	}:
	case <-ctx.Done():
	}
}

func (s *webSocketV2Subscription) SetLastWritten(cursor recording.Cursor) {
	s.writtenMu.Lock()
	s.lastWritten = cursor
	s.writtenMu.Unlock()
}

func (s *webSocketV2Subscription) LastWritten() recording.Cursor {
	s.writtenMu.RLock()
	defer s.writtenMu.RUnlock()
	return s.lastWritten
}

func (s *webSocketV2Subscription) Close() {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		s.cancel()
		if s.raw != nil {
			_ = s.raw.Close()
		}
		if s.events != nil {
			_ = s.events.Close()
		}
		if s.attachment != nil {
			_ = s.attachment.Close()
		}
	})
}

func decodeWebSocketV2Command(
	payload []byte,
) (webSocketV2Command, *webSocketV2Error) {
	if !utf8.Valid(payload) {
		return nil, newWebSocketV2Error(
			"",
			"invalid_utf8",
			"message must be valid UTF-8",
		)
	}
	var header *struct {
		Version   int    `json:"version"`
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(payload, &header); err != nil || header == nil {
		return nil, newWebSocketV2Error("", "invalid_message", "message must be one JSON object")
	}
	if header.Version != webSocketV2Version {
		return nil, newWebSocketV2Error(
			validResponseRequestID(header.RequestID),
			"unsupported_version",
			"version must be 2",
		)
	}
	if !validWebSocketID(header.RequestID) {
		return nil, newWebSocketV2Error("", "invalid_request_id", "request_id is invalid")
	}

	var command webSocketV2Command
	switch header.Type {
	case "subscribe":
		var subscribe webSocketV2Subscribe
		if err := decodeStrictWebSocketV2(payload, &subscribe); err != nil {
			return nil, invalidWebSocketV2Message(header.RequestID, err)
		}
		if protocolErr := subscribe.validate(); protocolErr != nil {
			return nil, protocolErr
		}
		command = subscribe
	case "unsubscribe":
		var unsubscribe webSocketV2Unsubscribe
		if err := decodeStrictWebSocketV2(payload, &unsubscribe); err != nil {
			return nil, invalidWebSocketV2Message(header.RequestID, err)
		}
		if protocolErr := unsubscribe.validate(); protocolErr != nil {
			return nil, protocolErr
		}
		command = unsubscribe
	case "input":
		var input webSocketV2Input
		if err := decodeStrictWebSocketV2(payload, &input); err != nil {
			return nil, invalidWebSocketV2Message(header.RequestID, err)
		}
		if !validWebSocketID(input.AgentID) {
			return nil, newWebSocketV2Error(
				header.RequestID,
				"invalid_agent_id",
				"agent_id is invalid",
			)
		}
		command = input
	case "resize":
		var resize webSocketV2Resize
		if err := decodeStrictWebSocketV2(payload, &resize); err != nil {
			return nil, invalidWebSocketV2Message(header.RequestID, err)
		}
		if !validWebSocketID(resize.AgentID) {
			return nil, newWebSocketV2Error(
				header.RequestID,
				"invalid_agent_id",
				"agent_id is invalid",
			)
		}
		if resize.Rows <= 0 || resize.Columns <= 0 ||
			resize.Rows > 1<<16-1 || resize.Columns > 1<<16-1 {
			return nil, newWebSocketV2Error(
				header.RequestID,
				"invalid_size",
				"rows and columns must be in 1..65535",
			)
		}
		command = resize
	default:
		return nil, newWebSocketV2Error(
			header.RequestID,
			"unsupported_type",
			"type must be subscribe, unsubscribe, input, or resize",
		)
	}
	return command, nil
}

func (c webSocketV2Subscribe) validate() *webSocketV2Error {
	if !validWebSocketID(c.AgentID) {
		return newWebSocketV2Error(c.RequestID, "invalid_agent_id", "agent_id is invalid")
	}
	selectorCount := 0
	if c.Cursor != nil {
		selectorCount++
	}
	if c.Sequence != nil {
		selectorCount++
	}
	if c.Offset != nil {
		selectorCount++
	}
	if selectorCount > 1 {
		return newWebSocketV2Error(
			c.RequestID,
			"invalid_selector",
			"subscribe accepts at most one cursor, seq, or offset",
		)
	}
	if c.Cursor != nil {
		if err := c.Cursor.Validate(); err != nil {
			return newWebSocketV2Error(c.RequestID, "invalid_selector", err.Error())
		}
	}

	writable := c.Writable != nil && *c.Writable
	switch c.Mode {
	case webSocketV2ModeRaw:
		if (c.Rows == nil) != (c.Columns == nil) {
			return newWebSocketV2Error(
				c.RequestID,
				"invalid_size",
				"rows and columns must be provided together",
			)
		}
		if !writable && (c.Rows != nil || c.Columns != nil) {
			return newWebSocketV2Error(
				c.RequestID,
				"invalid_size",
				"a viewport requires a writable raw subscription",
			)
		}
		if c.Rows != nil &&
			(*c.Rows <= 0 || *c.Columns <= 0 ||
				*c.Rows > 1<<16-1 || *c.Columns > 1<<16-1) {
			return newWebSocketV2Error(
				c.RequestID,
				"invalid_size",
				"rows and columns must be in 1..65535",
			)
		}
	case webSocketV2ModeEvents:
		if c.Writable != nil || c.Rows != nil || c.Columns != nil {
			return newWebSocketV2Error(
				c.RequestID,
				"invalid_subscription",
				"event subscriptions do not accept writable or viewport fields",
			)
		}
	case webSocketV2ModeSnapshot:
		if selectorCount != 0 ||
			c.Writable != nil ||
			c.Rows != nil ||
			c.Columns != nil {
			return newWebSocketV2Error(
				c.RequestID,
				"invalid_subscription",
				"snapshot subscriptions are live-only and read-only",
			)
		}
	default:
		return newWebSocketV2Error(
			c.RequestID,
			"invalid_mode",
			"mode must be raw, events, or snapshot",
		)
	}
	return nil
}

func (c webSocketV2Subscribe) selector() *recording.Selector {
	if c.Cursor == nil && c.Sequence == nil && c.Offset == nil {
		return nil
	}
	selector, err := recording.NewSelector(recording.SelectorInput{
		Cursor: c.Cursor,
		Seq:    c.Sequence,
		Offset: c.Offset,
	})
	if err != nil {
		return nil
	}
	return &selector
}

func (c webSocketV2Unsubscribe) validate() *webSocketV2Error {
	if !validWebSocketID(c.AgentID) {
		return newWebSocketV2Error(c.RequestID, "invalid_agent_id", "agent_id is invalid")
	}
	switch c.Mode {
	case webSocketV2ModeRaw, webSocketV2ModeEvents, webSocketV2ModeSnapshot:
		return nil
	default:
		return newWebSocketV2Error(
			c.RequestID,
			"invalid_mode",
			"mode must be raw, events, or snapshot",
		)
	}
}

func decodeStrictWebSocketV2(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("message must contain one JSON object")
	}
	return nil
}

func invalidWebSocketV2Message(
	requestID string,
	err error,
) *webSocketV2Error {
	return newWebSocketV2Error(
		requestID,
		"invalid_message",
		"invalid JSON message: "+err.Error(),
	)
}

func newWebSocketV2Error(
	requestID string,
	code string,
	message string,
) *webSocketV2Error {
	return &webSocketV2Error{
		Version:   webSocketV2Version,
		Type:      "error",
		RequestID: requestID,
		Code:      code,
		Message:   message,
	}
}

func newWebSocketV2ErrorForSubscription(
	requestID string,
	agentID string,
	mode webSocketV2Mode,
	code string,
	message string,
) *webSocketV2Error {
	response := newWebSocketV2Error(requestID, code, message)
	response.AgentID = agentID
	response.Mode = mode
	return response
}

func webSocketV2OperationError(
	requestID string,
	agentID string,
	mode webSocketV2Mode,
	err error,
) *webSocketV2Error {
	code := "internal_error"
	var expired *recording.OutputExpiredError
	switch {
	case errors.Is(err, recording.ErrUnknownSession),
		errors.Is(err, session.ErrUnknownAgent):
		code = "unknown_agent"
	case errors.Is(err, recording.ErrInvalidCursor):
		code = "invalid_cursor"
	case errors.As(err, &expired):
		code = "output_expired"
	case errors.Is(err, session.ErrNotAttached):
		code = "not_attached"
	case errors.Is(err, session.ErrManagerClosed):
		code = "manager_closed"
	case errors.Is(err, session.ErrAttachmentClosed):
		code = "attachment_closed"
	case errors.Is(err, session.ErrAttachmentReadOnly):
		code = "not_writable"
	case errors.Is(err, session.ErrInputEmpty):
		code = "empty_input"
	case errors.Is(err, session.ErrInputTooLarge):
		code = "input_too_large"
	case errors.Is(err, session.ErrInputNotUTF8):
		code = "invalid_utf8"
	case errors.Is(err, session.ErrInputWrite):
		code = "write_failed"
	case errors.Is(err, session.ErrInputAudit):
		code = "audit_failed"
	}
	response := newWebSocketV2ErrorForSubscription(
		requestID,
		agentID,
		mode,
		code,
		err.Error(),
	)
	if expired != nil {
		response.Missing = append([]recording.OutputRange(nil), expired.Missing...)
	}
	return response
}

func marshalWebSocketV2(message any) ([]byte, error) {
	payload, err := json.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("api: encode WebSocket v2 message: %w", err)
	}
	return payload, nil
}

func encodeWebSocketV2Raw(output recording.RawOutput) ([]byte, error) {
	if len(output.AgentID) == 0 || len(output.AgentID) > maxWebSocketIDBytes {
		return nil, errors.New("api: invalid raw output agent ID")
	}
	if len(output.Data) == 0 || len(output.Data) > event.MaxOutputChunkBytes {
		return nil, fmt.Errorf(
			"api: raw output length %d is outside 1..%d",
			len(output.Data),
			event.MaxOutputChunkBytes,
		)
	}
	for index := range len(output.AgentID) {
		if output.AgentID[index] > 0x7f {
			return nil, errors.New("api: raw output agent ID must be ASCII")
		}
	}
	payload := make(
		[]byte,
		webSocketV2RawHeaderBytes+len(output.AgentID)+len(output.Data),
	)
	copy(payload[0:4], "DRV2")
	payload[4] = webSocketV2RawOutputKind
	if output.Historical {
		payload[5] = webSocketV2HistoricalFlag
	}
	binary.BigEndian.PutUint16(payload[6:8], uint16(len(output.AgentID)))
	binary.BigEndian.PutUint64(payload[8:16], uint64(output.Sequence))
	binary.BigEndian.PutUint64(payload[16:24], uint64(output.Offset))
	copy(payload[24:], output.AgentID)
	copy(payload[24+len(output.AgentID):], output.Data)
	return payload, nil
}

func cloneCursor(cursor *recording.Cursor) *recording.Cursor {
	if cursor == nil {
		return nil
	}
	cloned := *cursor
	return &cloned
}
