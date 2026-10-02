// Package event 定义统一事件模型与扇出 Hub。
//
// 设计原则：事件不可变、全局有序（Seq）、发布者不阻塞。
package event

import (
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

// Hub 将事件广播给订阅者。发布者永不阻塞：
// 慢订阅者的缓冲溢出后事件被丢弃，并计入 Dropped。
type Hub struct {
	seq uint64

	mu      sync.RWMutex
	subs    map[uint64]*Subscription
	nextSub uint64
}

// NewHub 创建从 initialSeq 之后继续分配序号的事件 Hub。
func NewHub(initialSeq uint64) *Hub {
	return &Hub{
		seq:     initialSeq,
		subs:    make(map[uint64]*Subscription),
		nextSub: 1,
	}
}

// Subscribe 注册订阅，返回 Subscription。buf 为缓冲容量。
func (h *Hub) Subscribe(buf int) *Subscription {
	h.mu.Lock()
	defer h.mu.Unlock()

	s := &Subscription{
		id:  h.nextSub,
		ch:  make(chan Event, buf),
		hub: h,
	}
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

// NextSeq 原子分配一个事件序号（落库用，先于 Publish）。
func (h *Hub) NextSeq() uint64 {
	return atomic.AddUint64(&h.seq, 1)
}

// Publish 广播一个事件。若 ev.Seq 为 0 则自动分配序号；否则使用给定序号。
func (h *Hub) Publish(ev Event) uint64 {
	if ev.Seq == 0 {
		ev.Seq = atomic.AddUint64(&h.seq, 1)
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, s := range h.subs {
		select {
		case s.ch <- ev:
		default:
			atomic.AddUint64(&s.dropped, 1)
		}
	}
	return ev.Seq
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
