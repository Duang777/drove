// Package event 定义统一事件模型与扇出 Hub。
//
// 设计原则：事件不可变、全局有序（Seq）、发布者不阻塞。
package event

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Type 是事件类型。
type Type string

const (
	// TypeStateChanged 表示 agent 状态迁移。
	TypeStateChanged Type = "state_changed"
	// TypeOutput 表示 agent 输出增量（PTY 字节流按行切分）。
	TypeOutput Type = "output"
	// TypeError 表示 agent 或系统错误。
	TypeError Type = "error"
	// TypeSessionLifecycle 表示会话创建/销毁。
	TypeSessionLifecycle Type = "session_lifecycle"
	// TypeAgentInput 表示已写入 Agent PTY 的脱敏输入审计。
	TypeAgentInput Type = "agent.input"
	// TypeAgentSignal 表示 Detector 已接受的脱敏状态信号。
	TypeAgentSignal Type = "agent.signal"
)

// Event 是不可变事件。所有字段导出供序列化，但外部不得修改。
type Event struct {
	Seq       uint64    `json:"seq"`
	Timestamp time.Time `json:"timestamp"`
	Type      Type      `json:"type"`
	AgentID   string    `json:"agent_id,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	// From / To 仅在 TypeStateChanged 时非空。
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Payload 承载具体事件类型定义的内容。
	Payload string `json:"payload,omitempty"`
}

// NewStateChanged 构造状态迁移事件。
func NewStateChanged(seq uint64, sessionID, agentID, from, to, reason string) Event {
	return Event{
		Seq:       seq,
		Timestamp: time.Now().UTC(),
		Type:      TypeStateChanged,
		SessionID: sessionID,
		AgentID:   agentID,
		From:      from,
		To:        to,
		Reason:    reason,
	}
}

// NewOutput 构造输出增量事件。
func NewOutput(seq uint64, sessionID, agentID, line string) Event {
	return Event{
		Seq:       seq,
		Timestamp: time.Now().UTC(),
		Type:      TypeOutput,
		SessionID: sessionID,
		AgentID:   agentID,
		Payload:   line,
	}
}

// NewError 构造错误事件。
func NewError(seq uint64, sessionID, agentID, message string) Event {
	return Event{
		Seq:       seq,
		Timestamp: time.Now().UTC(),
		Type:      TypeError,
		SessionID: sessionID,
		AgentID:   agentID,
		Payload:   message,
	}
}

// NewSessionLifecycle 构造会话生命周期事件。
func NewSessionLifecycle(seq uint64, sessionID, agentID, reason, payload string) Event {
	return Event{
		Seq:       seq,
		Timestamp: time.Now().UTC(),
		Type:      TypeSessionLifecycle,
		SessionID: sessionID,
		AgentID:   agentID,
		Reason:    reason,
		Payload:   payload,
	}
}

// NewAgentInput 构造已写入 Agent PTY 的脱敏输入审计事件。
func NewAgentInput(seq uint64, sessionID, agentID, payload string) Event {
	return Event{
		Seq:       seq,
		Timestamp: time.Now().UTC(),
		Type:      TypeAgentInput,
		SessionID: sessionID,
		AgentID:   agentID,
		Reason:    "accepted",
		Payload:   payload,
	}
}

// NewAgentSignal 构造 Detector 接受的脱敏状态信号事件。
func NewAgentSignal(seq uint64, sessionID, agentID, reason, payload string) Event {
	return Event{
		Seq:       seq,
		Timestamp: time.Now().UTC(),
		Type:      TypeAgentSignal,
		SessionID: sessionID,
		AgentID:   agentID,
		Reason:    reason,
		Payload:   payload,
	}
}

var (
	// ErrUncommittedEvent 表示调用方尝试发布尚未分配序号的事件草稿。
	ErrUncommittedEvent = errors.New("event: cannot publish uncommitted event")
	// ErrSequenceOrder 表示事件没有按全局序号递增发布。
	ErrSequenceOrder = errors.New("event: publish sequence is not increasing")
	// ErrHubClosed 表示 Hub 已关闭。
	ErrHubClosed = errors.New("event: hub closed")
)

// Hub 将事件广播给订阅者。发布者永不阻塞：
// 慢订阅者的缓冲溢出后事件被丢弃，并计入 Dropped。
type Hub struct {
	mu      sync.RWMutex
	subs    map[uint64]*Subscription
	nextSub uint64
	lastSeq uint64
	closed  bool
}

// NewHub 创建只接受 initialSeq 之后已提交事件的 Hub。
func NewHub(initialSeq uint64) *Hub {
	return &Hub{
		subs:    make(map[uint64]*Subscription),
		nextSub: 1,
		lastSeq: initialSeq,
	}
}

// Subscribe 注册订阅，返回 Subscription。buf 为缓冲容量。
func (h *Hub) Subscribe(buf int) *Subscription {
	h.mu.Lock()
	defer h.mu.Unlock()

	s := &Subscription{
		ch:  make(chan Event, buf),
		hub: h,
	}
	if h.closed {
		close(s.ch)
		return s
	}
	s.id = h.nextSub
	h.nextSub++
	h.subs[s.id] = s
	return s
}

// Unsubscribe 注销订阅并关闭其 channel。
func (h *Hub) Unsubscribe(s *Subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.subs[s.id]; !ok {
		return
	}
	delete(h.subs, s.id)
	close(s.ch)
}

// Publish 广播一个已提交事件。序号分配由持久化提交器负责。
func (h *Hub) Publish(ev Event) error {
	if ev.Seq == 0 {
		return ErrUncommittedEvent
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrHubClosed
	}
	if ev.Seq != h.lastSeq+1 {
		return fmt.Errorf("%w: got %d after %d", ErrSequenceOrder, ev.Seq, h.lastSeq)
	}
	h.lastSeq = ev.Seq

	for _, s := range h.subs {
		select {
		case s.ch <- ev:
		default:
			atomic.AddUint64(&s.dropped, 1)
		}
	}
	return nil
}

// LastSeq 返回 Hub 最后接受的已提交事件序号。
func (h *Hub) LastSeq() uint64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.lastSeq
}

// Close 关闭全部订阅并拒绝后续订阅。
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for id, sub := range h.subs {
		delete(h.subs, id)
		close(sub.ch)
	}
}

// Subscription 是一次事件订阅。
type Subscription struct {
	id      uint64
	ch      chan Event
	hub     *Hub
	dropped uint64
}

// C 返回事件 channel（只读使用）。
func (s *Subscription) C() <-chan Event { return s.ch }

// Dropped 返回该订阅被丢弃的事件数（因缓冲满）。
func (s *Subscription) Dropped() uint64 {
	return atomic.LoadUint64(&s.dropped)
}
