// Package event 定义统一事件模型与扇出 Hub。
//
// 设计原则：事件不可变、全局有序（Seq）、发布者不阻塞。
package event

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
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

// SignalPayloadV1 is the redacted audit payload for TypeAgentSignal.
type SignalPayloadV1 struct {
	Version         int     `json:"version"`
	Source          string  `json:"source"`
	Kind            string  `json:"kind"`
	Vendor          string  `json:"vendor,omitempty"`
	VendorEvent     string  `json:"vendor_event"`
	Scope           string  `json:"scope"`
	VendorSessionID string  `json:"vendor_session_id,omitempty"`
	VendorTurnID    string  `json:"vendor_turn_id,omitempty"`
	Notification    string  `json:"notification,omitempty"`
	Evidence        string  `json:"evidence,omitempty"`
	Confidence      float64 `json:"confidence"`
	OccurredAt      string  `json:"occurred_at,omitempty"`
	ReceivedAt      string  `json:"received_at"`
	DeliveryID      string  `json:"delivery_id,omitempty"`
	Outcome         string  `json:"outcome"`
	ExitCode        *int    `json:"exit_code,omitempty"`
	ExitKind        string  `json:"exit_kind,omitempty"`
}

// Validate rejects malformed or privacy-unsafe signal metadata.
func (p SignalPayloadV1) Validate() error {
	if p.Version != 1 {
		return fmt.Errorf("event: unsupported signal payload version %d", p.Version)
	}
	if !oneOf(p.Source, "process", "hook", "heuristic", "timer") {
		return fmt.Errorf("event: invalid signal source %q", p.Source)
	}
	legacyTransitional := p.Kind == "" && p.Outcome == "" && p.Evidence != ""
	if !legacyTransitional && !oneOf(
		p.Kind,
		"session_started",
		"observed",
		"turn_started",
		"tool_activity",
		"human_input_required",
		"human_input_resolved",
		"permission_requested",
		"permission_resolved",
		"turn_stopped",
		"turn_failed",
		"interrupted",
		"idle_prompt",
		"session_ended",
		"subagent_started",
		"subagent_stopped",
		"task_completed",
		"output_activity",
		"heuristic_blocked",
		"process_started",
		"process_start_failed",
		"process_exited",
		"hook_activation_expired",
		"timer_fired",
	) {
		return fmt.Errorf("event: invalid signal kind %q", p.Kind)
	}
	if p.VendorEvent == "" || len(p.VendorEvent) > 64 || !ascii(p.VendorEvent) {
		return errors.New("event: signal vendor_event must contain 1 to 64 ASCII bytes")
	}
	if !oneOf(p.Scope, "root", "subagent") {
		return fmt.Errorf("event: invalid signal scope %q", p.Scope)
	}
	if len(p.Vendor) > 64 ||
		len(p.VendorSessionID) > 256 ||
		len(p.VendorTurnID) > 256 ||
		len(p.Notification) > 64 ||
		len(p.Evidence) > 128 {
		return errors.New("event: signal metadata exceeds its size limit")
	}
	if math.IsNaN(p.Confidence) || math.IsInf(p.Confidence, 0) ||
		p.Confidence < 0 || p.Confidence > 1 {
		return fmt.Errorf("event: invalid signal confidence %v", p.Confidence)
	}
	if p.ReceivedAt == "" {
		return errors.New("event: signal received_at is required")
	}
	if _, err := time.Parse(time.RFC3339Nano, p.ReceivedAt); err != nil {
		return fmt.Errorf("event: invalid signal received_at: %w", err)
	}
	if p.OccurredAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, p.OccurredAt); err != nil {
			return fmt.Errorf("event: invalid signal occurred_at: %w", err)
		}
	}
	if p.Source == "hook" {
		if p.Vendor == "" {
			return errors.New("event: hook signal vendor is required")
		}
		if !canonicalUUID(p.DeliveryID) {
			return errors.New("event: hook signal delivery_id must be a canonical UUID")
		}
	} else if p.DeliveryID != "" {
		return errors.New("event: only hook signals may contain delivery_id")
	}
	if !legacyTransitional &&
		!oneOf(p.Outcome, "observed", "candidate", "transitioned", "suppressed", "stale", "terminal") {
		return fmt.Errorf("event: invalid signal outcome %q", p.Outcome)
	}
	if p.ExitKind != "" && !oneOf(p.ExitKind, "success", "failure", "stopped", "startup_failed") {
		return fmt.Errorf("event: invalid process exit kind %q", p.ExitKind)
	}
	if p.Source != "process" && (p.ExitCode != nil || p.ExitKind != "") {
		return errors.New("event: only process signals may contain exit metadata")
	}
	return nil
}

// StateEvidencePayloadV1 explains one durable state transition.
type StateEvidencePayloadV1 struct {
	Version    int     `json:"version"`
	Source     string  `json:"source"`
	Event      string  `json:"event"`
	Confidence float64 `json:"confidence"`
	DeliveryID string  `json:"delivery_id,omitempty"`
}

// Validate rejects malformed transition evidence.
func (p StateEvidencePayloadV1) Validate() error {
	if p.Version != 1 {
		return fmt.Errorf("event: unsupported state evidence version %d", p.Version)
	}
	if !oneOf(p.Source, "session", "process", "hook", "heuristic", "timer", "recovery") {
		return fmt.Errorf("event: invalid state evidence source %q", p.Source)
	}
	if p.Event == "" || len(p.Event) > 64 || !ascii(p.Event) {
		return errors.New("event: state evidence event must contain 1 to 64 ASCII bytes")
	}
	if math.IsNaN(p.Confidence) || math.IsInf(p.Confidence, 0) ||
		p.Confidence < 0 || p.Confidence > 1 {
		return fmt.Errorf("event: invalid state evidence confidence %v", p.Confidence)
	}
	if p.Source == "hook" {
		if !canonicalUUID(p.DeliveryID) {
			return errors.New("event: hook state evidence delivery_id must be a canonical UUID")
		}
	} else if p.DeliveryID != "" {
		return errors.New("event: only hook state evidence may contain delivery_id")
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func ascii(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r > 0x7f || r < 0x20 {
			return false
		}
	}
	return strings.TrimSpace(value) == value
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
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
