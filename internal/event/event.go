// Package event 定义统一事件模型与扇出 Hub。
//
// 设计原则：事件不可变、全局有序（Seq）、发布者不阻塞。
package event

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// TypeOutputChunk 表示带会话内字节偏移的终端输出块。
	TypeOutputChunk Type = "output.chunk"
	// TypeError 表示 agent 或系统错误。
	TypeError Type = "error"
	// TypeSessionLifecycle 表示会话创建/销毁。
	TypeSessionLifecycle Type = "session_lifecycle"
	// TypeAgentInput 表示已写入 Agent PTY 的脱敏输入审计。
	TypeAgentInput Type = "agent.input"
	// TypeAgentSignal 表示 Detector 已接受的脱敏状态信号。
	TypeAgentSignal Type = "agent.signal"
	// TypeAgentResized 表示已成功应用到 PTY 和终端模型的尺寸。
	TypeAgentResized Type = "agent.resized"
	// TypeAgentAttachment 表示用户终端 attachment 的脱敏生命周期审计。
	TypeAgentAttachment Type = "agent.attachment"
	// TypeAgentResumed marks an explicit native resume attempt.
	TypeAgentResumed Type = "agent.resumed"
)

// Event 是不可变事件。公开字段供序列化，私有字段保存持久化附件。
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

	storedPayload    string
	outputAttachment []byte
}

// Draft is an unsequenced event value. Its fields stay private so callers
// cannot forge a committed event.
type Draft struct {
	typ       Type
	agentID   string
	sessionID string
	from      string
	to        string
	reason    string
	payload   string

	storedPayload    string
	outputAttachment []byte
}

// NewStateChangedDraft constructs an uncommitted state transition.
func NewStateChangedDraft(sessionID, agentID, from, to, reason, payload string) Draft {
	return Draft{
		typ:       TypeStateChanged,
		sessionID: sessionID,
		agentID:   agentID,
		from:      from,
		to:        to,
		reason:    reason,
		payload:   payload,
	}
}

// NewOutputDraft constructs an uncommitted output event.
func NewOutputDraft(sessionID, agentID, line string) Draft {
	return Draft{
		typ:       TypeOutput,
		sessionID: sessionID,
		agentID:   agentID,
		payload:   line,
	}
}

const (
	// OutputChunkPayloadVersion 是当前 output.chunk payload 版本。
	OutputChunkPayloadVersion = 1
	// MaxOutputChunkBytes 是单个 output.chunk 可携带的最大解码字节数。
	MaxOutputChunkBytes       = 32 * 1024
	agentResumedPublicPayload = `{"version":1}`
)

// OutputChunkPayloadV1 是 output.chunk 的版本 1 传输载荷。
type OutputChunkPayloadV1 struct {
	Version int    `json:"version"`
	Offset  uint64 `json:"offset"`
	Len     int    `json:"len"`
	DataB64 string `json:"data_b64,omitempty"`
}

// ValidateMetadata 校验可持久化的 output.chunk 元数据。
func (p OutputChunkPayloadV1) ValidateMetadata() error {
	if p.Version != OutputChunkPayloadVersion {
		return fmt.Errorf("event: output chunk version %d is unsupported", p.Version)
	}
	if p.Len < 1 || p.Len > MaxOutputChunkBytes {
		return fmt.Errorf(
			"event: output chunk length %d is outside 1..%d",
			p.Len,
			MaxOutputChunkBytes,
		)
	}
	return nil
}

// ValidateHydrated 校验带 Base64 正文的 output.chunk 载荷。
func (p OutputChunkPayloadV1) ValidateHydrated() error {
	if err := p.ValidateMetadata(); err != nil {
		return err
	}
	data, err := base64.StdEncoding.DecodeString(p.DataB64)
	if err != nil {
		return fmt.Errorf("event: decode output chunk data: %w", err)
	}
	if p.DataB64 == "" || base64.StdEncoding.EncodeToString(data) != p.DataB64 {
		return errors.New("event: output chunk data is not canonical padded Base64")
	}
	if len(data) != p.Len {
		return fmt.Errorf(
			"event: output chunk decoded length %d does not match %d",
			len(data),
			p.Len,
		)
	}
	return nil
}

// DecodeData 返回校验后的 output.chunk 原始字节副本。
func (p OutputChunkPayloadV1) DecodeData() ([]byte, error) {
	if err := p.ValidateHydrated(); err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(p.DataB64)
	if err != nil {
		return nil, fmt.Errorf("event: decode validated output chunk data: %w", err)
	}
	return append([]byte(nil), data...), nil
}

// DecodeOutputChunkPayload 解码并校验 output.chunk 元数据。
func DecodeOutputChunkPayload(payload string) (OutputChunkPayloadV1, error) {
	var decoded OutputChunkPayloadV1
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		return OutputChunkPayloadV1{}, fmt.Errorf("event: decode output chunk payload: %w", err)
	}
	if err := decoded.ValidateMetadata(); err != nil {
		return OutputChunkPayloadV1{}, err
	}
	return decoded, nil
}

// HydrateOutputChunkPayload 将持久化元数据和附件编码为传输载荷。
func HydrateOutputChunkPayload(payload string, data []byte) (string, error) {
	decoded, err := DecodeOutputChunkPayload(payload)
	if err != nil {
		return "", err
	}
	if len(data) != decoded.Len {
		return "", fmt.Errorf(
			"event: output chunk attachment length %d does not match %d",
			len(data),
			decoded.Len,
		)
	}
	decoded.DataB64 = base64.StdEncoding.EncodeToString(data)
	if validateErr := decoded.ValidateHydrated(); validateErr != nil {
		return "", validateErr
	}
	hydrated, err := json.Marshal(decoded)
	if err != nil {
		return "", fmt.Errorf("event: encode hydrated output chunk payload: %w", err)
	}
	return string(hydrated), nil
}

// NewOutputChunkDraft constructs an uncommitted output chunk event.
func NewOutputChunkDraft(
	sessionID string,
	agentID string,
	offset uint64,
	data []byte,
) (Draft, error) {
	payload := OutputChunkPayloadV1{
		Version: OutputChunkPayloadVersion,
		Offset:  offset,
		Len:     len(data),
		DataB64: base64.StdEncoding.EncodeToString(data),
	}
	if err := payload.ValidateHydrated(); err != nil {
		return Draft{}, err
	}
	hydrated, err := json.Marshal(payload)
	if err != nil {
		return Draft{}, fmt.Errorf("event: encode output chunk payload: %w", err)
	}
	payload.DataB64 = ""
	stored, err := json.Marshal(payload)
	if err != nil {
		return Draft{}, fmt.Errorf("event: encode output chunk metadata: %w", err)
	}
	return Draft{
		typ:              TypeOutputChunk,
		sessionID:        sessionID,
		agentID:          agentID,
		payload:          string(hydrated),
		storedPayload:    string(stored),
		outputAttachment: append([]byte(nil), data...),
	}, nil
}

// NewErrorDraft constructs an uncommitted error event.
func NewErrorDraft(sessionID, agentID, message string) Draft {
	return Draft{
		typ:       TypeError,
		sessionID: sessionID,
		agentID:   agentID,
		payload:   message,
	}
}

// NewSessionLifecycleDraft constructs an uncommitted lifecycle event.
func NewSessionLifecycleDraft(sessionID, agentID, reason, payload string) Draft {
	return Draft{
		typ:       TypeSessionLifecycle,
		sessionID: sessionID,
		agentID:   agentID,
		reason:    reason,
		payload:   payload,
	}
}

// NewPrivateSessionLifecycleDraft stores private lifecycle metadata separately
// from the payload published to subscribers.
func NewPrivateSessionLifecycleDraft(
	sessionID,
	agentID,
	reason,
	publicPayload,
	storedPayload string,
) Draft {
	return Draft{
		typ:           TypeSessionLifecycle,
		sessionID:     sessionID,
		agentID:       agentID,
		reason:        reason,
		payload:       publicPayload,
		storedPayload: storedPayload,
	}
}

// NewAgentInputDraft constructs an uncommitted input audit event.
func NewAgentInputDraft(sessionID, agentID, payload string) Draft {
	return Draft{
		typ:       TypeAgentInput,
		sessionID: sessionID,
		agentID:   agentID,
		reason:    "accepted",
		payload:   payload,
	}
}

// NewAgentSignalDraft constructs an uncommitted signal audit event.
func NewAgentSignalDraft(sessionID, agentID, payload string) Draft {
	publicPayload, _ := PublicPayload(TypeAgentSignal, "observed", payload)
	return Draft{
		typ:           TypeAgentSignal,
		sessionID:     sessionID,
		agentID:       agentID,
		reason:        "observed",
		payload:       publicPayload,
		storedPayload: payload,
	}
}

const (
	// AgentResizedPayloadVersion is the current agent.resized payload version.
	AgentResizedPayloadVersion = 1
)

// AgentResizedPayloadV1 records one effective terminal size at an output boundary.
type AgentResizedPayloadV1 struct {
	Version      int    `json:"version"`
	Rows         uint16 `json:"rows"`
	Columns      uint16 `json:"columns"`
	OutputOffset uint64 `json:"output_offset"`
}

// Validate rejects malformed agent.resized payloads.
func (p AgentResizedPayloadV1) Validate() error {
	if p.Version != AgentResizedPayloadVersion {
		return fmt.Errorf("event: agent resized version %d is unsupported", p.Version)
	}
	if p.Rows == 0 {
		return errors.New("event: agent resized rows must be positive")
	}
	if p.Columns == 0 {
		return errors.New("event: agent resized columns must be positive")
	}
	return nil
}

// DecodeAgentResizedPayload decodes and validates an agent.resized v1 payload.
func DecodeAgentResizedPayload(payload string) (AgentResizedPayloadV1, error) {
	var decoded AgentResizedPayloadV1
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		return AgentResizedPayloadV1{}, fmt.Errorf(
			"event: decode agent resized payload: %w",
			err,
		)
	}
	if err := decoded.Validate(); err != nil {
		return AgentResizedPayloadV1{}, err
	}
	return decoded, nil
}

// NewAgentResizedDraft constructs an uncommitted effective terminal resize.
func NewAgentResizedDraft(
	sessionID string,
	agentID string,
	rows uint16,
	columns uint16,
	outputOffset uint64,
) (Draft, error) {
	payload := AgentResizedPayloadV1{
		Version:      AgentResizedPayloadVersion,
		Rows:         rows,
		Columns:      columns,
		OutputOffset: outputOffset,
	}
	if err := payload.Validate(); err != nil {
		return Draft{}, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Draft{}, fmt.Errorf("event: encode agent resized payload: %w", err)
	}
	return Draft{
		typ:       TypeAgentResized,
		sessionID: sessionID,
		agentID:   agentID,
		reason:    "applied",
		payload:   string(encoded),
	}, nil
}

// AttachmentAction identifies one user attachment lifecycle transition.
type AttachmentAction string

const (
	// AttachmentAttached records successful user attachment setup.
	AttachmentAttached AttachmentAction = "attached"
	// AttachmentDetached records completed local attachment cleanup.
	AttachmentDetached AttachmentAction = "detached"
)

// AttachmentAccess identifies the capabilities granted to a user attachment.
type AttachmentAccess string

const (
	// AttachmentReadOnly permits terminal output reads only.
	AttachmentReadOnly AttachmentAccess = "read_only"
	// AttachmentReadWrite permits terminal output, input, and resize.
	AttachmentReadWrite AttachmentAccess = "read_write"
)

const (
	// AttachmentAuditPayloadVersion is the current agent.attachment payload version.
	AttachmentAuditPayloadVersion = 1
)

// AttachmentAuditPayloadV1 records one privacy-bounded attachment transition.
type AttachmentAuditPayloadV1 struct {
	Version int              `json:"version"`
	Action  AttachmentAction `json:"action"`
	Access  AttachmentAccess `json:"access"`
}

// Validate rejects malformed attachment audit metadata.
func (p AttachmentAuditPayloadV1) Validate() error {
	if p.Version != AttachmentAuditPayloadVersion {
		return fmt.Errorf("event: attachment audit version %d is unsupported", p.Version)
	}
	switch p.Action {
	case AttachmentAttached, AttachmentDetached:
	default:
		return fmt.Errorf("event: attachment audit action %q is invalid", p.Action)
	}
	switch p.Access {
	case AttachmentReadOnly, AttachmentReadWrite:
	default:
		return fmt.Errorf("event: attachment audit access %q is invalid", p.Access)
	}
	return nil
}

// DecodeAttachmentAuditPayload decodes and validates an agent.attachment v1 payload.
func DecodeAttachmentAuditPayload(payload string) (AttachmentAuditPayloadV1, error) {
	var decoded AttachmentAuditPayloadV1
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return AttachmentAuditPayloadV1{}, fmt.Errorf(
			"event: decode attachment audit payload: %w",
			err,
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return AttachmentAuditPayloadV1{}, fmt.Errorf(
			"event: decode attachment audit payload: %w",
			err,
		)
	}
	if err := decoded.Validate(); err != nil {
		return AttachmentAuditPayloadV1{}, err
	}
	return decoded, nil
}

// NewAgentAttachmentDraft constructs an uncommitted user attachment audit.
func NewAgentAttachmentDraft(
	sessionID string,
	agentID string,
	action AttachmentAction,
	access AttachmentAccess,
) (Draft, error) {
	payload := AttachmentAuditPayloadV1{
		Version: AttachmentAuditPayloadVersion,
		Action:  action,
		Access:  access,
	}
	if err := payload.Validate(); err != nil {
		return Draft{}, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Draft{}, fmt.Errorf("event: encode attachment audit payload: %w", err)
	}
	return Draft{
		typ:       TypeAgentAttachment,
		sessionID: sessionID,
		agentID:   agentID,
		payload:   string(encoded),
	}, nil
}

// NewAgentResumedDraft stores the full payload but exposes only its version.
func NewAgentResumedDraft(sessionID, agentID, payload string) Draft {
	return Draft{
		typ:           TypeAgentResumed,
		sessionID:     sessionID,
		agentID:       agentID,
		reason:        "requested",
		payload:       agentResumedPublicPayload,
		storedPayload: payload,
	}
}

// Commit seals a copied draft with its durable sequence and timestamp.
func Commit(seq uint64, at time.Time, draft Draft) (Event, error) {
	if seq == 0 {
		return Event{}, ErrUncommittedEvent
	}
	if at.IsZero() {
		return Event{}, errors.New("event: commit timestamp is required")
	}
	switch draft.typ {
	case TypeStateChanged,
		TypeOutput,
		TypeOutputChunk,
		TypeError,
		TypeSessionLifecycle,
		TypeAgentInput,
		TypeAgentSignal,
		TypeAgentResized,
		TypeAgentAttachment,
		TypeAgentResumed:
	default:
		return Event{}, fmt.Errorf("event: invalid draft type %q", draft.typ)
	}
	if draft.sessionID == "" || draft.agentID == "" {
		return Event{}, errors.New("event: draft session and agent IDs are required")
	}
	if draft.sessionID != draft.agentID {
		return Event{}, errors.New("event: draft session and agent IDs must match")
	}
	storedPayload := draft.payload
	var outputAttachment []byte
	if draft.typ == TypeOutputChunk {
		hydrated, err := DecodeOutputChunkPayload(draft.payload)
		if err != nil {
			return Event{}, err
		}
		data, err := hydrated.DecodeData()
		if err != nil {
			return Event{}, err
		}
		if !bytes.Equal(data, draft.outputAttachment) {
			return Event{}, errors.New("event: output chunk attachment does not match payload")
		}
		metadata, err := DecodeOutputChunkPayload(draft.storedPayload)
		if err != nil {
			return Event{}, err
		}
		if metadata.DataB64 != "" {
			return Event{}, errors.New("event: stored output chunk metadata contains data")
		}
		if metadata.Version != hydrated.Version ||
			metadata.Offset != hydrated.Offset ||
			metadata.Len != hydrated.Len {
			return Event{}, errors.New("event: stored output chunk metadata does not match payload")
		}
		storedPayload = draft.storedPayload
		outputAttachment = append([]byte(nil), draft.outputAttachment...)
	} else if draft.typ == TypeAgentSignal {
		publicPayload, err := PublicPayload(
			TypeAgentSignal,
			draft.reason,
			draft.storedPayload,
		)
		if err != nil {
			return Event{}, err
		}
		if draft.payload != publicPayload {
			return Event{}, errors.New("event: invalid public agent.signal payload")
		}
		storedPayload = draft.storedPayload
	} else if draft.typ == TypeAgentResumed {
		if _, err := DecodeAgentResumedPayload(draft.storedPayload); err != nil {
			return Event{}, err
		}
		if draft.payload != agentResumedPublicPayload {
			return Event{}, errors.New("event: invalid public agent.resumed payload")
		}
		storedPayload = draft.storedPayload
	} else if draft.typ == TypeSessionLifecycle && draft.storedPayload != "" {
		if draft.reason != "created" {
			return Event{}, errors.New(
				"event: private lifecycle payload requires the created reason",
			)
		}
		if draft.payload == "" {
			return Event{}, errors.New(
				"event: private lifecycle payload requires a public payload",
			)
		}
		storedPayload = draft.storedPayload
	} else if len(draft.outputAttachment) != 0 || draft.storedPayload != "" {
		return Event{}, errors.New("event: non-output chunk draft contains private output data")
	}
	if draft.typ == TypeAgentResized {
		if _, err := DecodeAgentResizedPayload(draft.payload); err != nil {
			return Event{}, err
		}
	}
	if draft.typ == TypeAgentAttachment {
		if _, err := DecodeAttachmentAuditPayload(draft.payload); err != nil {
			return Event{}, err
		}
	}
	return Event{
		Seq:              seq,
		Timestamp:        at,
		Type:             draft.typ,
		AgentID:          draft.agentID,
		SessionID:        draft.sessionID,
		From:             draft.from,
		To:               draft.to,
		Reason:           draft.reason,
		Payload:          draft.payload,
		storedPayload:    storedPayload,
		outputAttachment: outputAttachment,
	}, nil
}

// StoredPayload 返回事件写入不可变 envelope 时使用的载荷。
func (e Event) StoredPayload() string {
	if e.storedPayload != "" {
		return e.storedPayload
	}
	return e.Payload
}

// OutputAttachment 返回事件私有输出附件的副本。
func (e Event) OutputAttachment() []byte {
	return append([]byte(nil), e.outputAttachment...)
}

// AgentResumedPayloadV1 identifies the opaque vendor session selected for resume.
type AgentResumedPayloadV1 struct {
	Version          int    `json:"version"`
	VendorSessionRef string `json:"vendor_session_ref"`
}

// Validate requires version 1 and a bounded printable ASCII reference.
func (p AgentResumedPayloadV1) Validate() error {
	if p.Version != 1 {
		return fmt.Errorf("event: unsupported agent.resumed payload version %d", p.Version)
	}
	if p.VendorSessionRef == "" ||
		len(p.VendorSessionRef) > 256 ||
		!ascii(p.VendorSessionRef) {
		return errors.New(
			"event: vendor_session_ref must contain 1 to 256 printable ASCII bytes without surrounding whitespace",
		)
	}
	return nil
}

// DecodeAgentResumedPayload validates a persisted native resume payload.
func DecodeAgentResumedPayload(raw string) (AgentResumedPayloadV1, error) {
	var payload AgentResumedPayloadV1
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return AgentResumedPayloadV1{}, fmt.Errorf(
			"event: decode agent.resumed payload: %w",
			err,
		)
	}
	if err := payload.Validate(); err != nil {
		return AgentResumedPayloadV1{}, err
	}
	return payload, nil
}

// RedactAgentResumedPayload validates a persisted payload and removes its reference.
func RedactAgentResumedPayload(raw string) (string, error) {
	if _, err := DecodeAgentResumedPayload(raw); err != nil {
		return "", err
	}
	return agentResumedPublicPayload, nil
}

// PublicPayload removes private fields from a stored event payload.
func PublicPayload(typ Type, reason, raw string) (string, error) {
	switch {
	case typ == TypeAgentSignal:
		return removePayloadFields(
			raw,
			"vendor_session_ref",
			"vendor_session_id",
		)
	case typ == TypeAgentResumed:
		return RedactAgentResumedPayload(raw)
	case typ == TypeSessionLifecycle && reason == "created":
		if raw == "" {
			return "", nil
		}
		return removePayloadFields(raw, "working_dir", "workspace")
	default:
		return raw, nil
	}
}

func removePayloadFields(raw string, fields ...string) (string, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "", fmt.Errorf("event: decode private payload: %w", err)
	}
	if payload == nil {
		return "", errors.New("event: private payload must be one JSON object")
	}
	changed := false
	for _, field := range fields {
		if _, ok := payload[field]; ok {
			delete(payload, field)
			changed = true
		}
	}
	if !changed {
		return raw, nil
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("event: encode public payload: %w", err)
	}
	return string(encoded), nil
}

// SignalPayloadV1 is the redacted audit payload for TypeAgentSignal.
type SignalPayloadV1 struct {
	Version          int     `json:"version"`
	Source           string  `json:"source"`
	Kind             string  `json:"kind"`
	Vendor           string  `json:"vendor,omitempty"`
	VendorEvent      string  `json:"vendor_event"`
	Scope            string  `json:"scope"`
	VendorSessionRef string  `json:"vendor_session_ref,omitempty"`
	VendorSessionID  string  `json:"vendor_session_id,omitempty"`
	VendorTurnID     string  `json:"vendor_turn_id,omitempty"`
	Notification     string  `json:"notification,omitempty"`
	Evidence         string  `json:"evidence,omitempty"`
	Confidence       float64 `json:"confidence"`
	OccurredAt       string  `json:"occurred_at,omitempty"`
	ReceivedAt       string  `json:"received_at"`
	DeliveryID       string  `json:"delivery_id,omitempty"`
	Outcome          string  `json:"outcome"`
	ExitCode         *int    `json:"exit_code,omitempty"`
	ExitKind         string  `json:"exit_kind,omitempty"`
}

// VendorSessionReference returns the current reference or its legacy alias.
func (p SignalPayloadV1) VendorSessionReference() string {
	if p.VendorSessionRef != "" {
		return p.VendorSessionRef
	}
	return p.VendorSessionID
}

// Validate rejects malformed or privacy-unsafe signal metadata.
func (p SignalPayloadV1) Validate() error {
	return validateSignalPayload(p, 1, false, false, false)
}

// SignalPayloadV2 adds the non-authoritative notify source.
type SignalPayloadV2 SignalPayloadV1

// Validate rejects malformed or privacy-unsafe signal metadata.
func (p SignalPayloadV2) Validate() error {
	return validateSignalPayload(SignalPayloadV1(p), 2, true, false, false)
}

// ScreenAttributionPayload is bounded static metadata for one screen rule edge.
type ScreenAttributionPayload struct {
	Rule          string `json:"rule"`
	Edge          string `json:"edge"`
	Region        string `json:"region"`
	OutputOffset  uint64 `json:"output_offset"`
	LastOutputSeq uint64 `json:"last_output_seq"`
	Evidence      string `json:"evidence"`
}

// Validate rejects malformed or unbounded screen attribution.
func (p ScreenAttributionPayload) Validate() error {
	if p.Rule == "" || len(p.Rule) > 64 || !ascii(p.Rule) {
		return errors.New("event: screen rule must contain 1 to 64 ASCII bytes")
	}
	if !oneOf(p.Edge, "present", "cleared") {
		return fmt.Errorf("event: invalid screen edge %q", p.Edge)
	}
	if p.Region == "" || len(p.Region) > 64 || !ascii(p.Region) {
		return errors.New("event: screen region must contain 1 to 64 ASCII bytes")
	}
	if p.OutputOffset == 0 {
		return errors.New("event: screen output_offset must be positive")
	}
	if p.LastOutputSeq == 0 {
		return errors.New("event: screen last_output_seq must be positive")
	}
	if p.Evidence == "" || len(p.Evidence) > 128 || !ascii(p.Evidence) {
		return errors.New("event: screen evidence must contain 1 to 128 ASCII bytes")
	}
	return nil
}

// SignalPayloadV3 adds optional typed screen attribution.
type SignalPayloadV3 struct {
	SignalPayloadV1
	Screen *ScreenAttributionPayload `json:"screen,omitempty"`
}

// Validate rejects malformed or mismatched screen signal attribution.
func (p SignalPayloadV3) Validate() error {
	base := p.SignalPayloadV1
	if err := validateSignalPayload(base, 3, true, true, false); err != nil {
		return err
	}
	return validateSignalScreenAttribution(base, p.Screen)
}

func validateSignalScreenAttribution(
	base SignalPayloadV1,
	screen *ScreenAttributionPayload,
) error {
	if base.Source == "screen" {
		if screen == nil {
			return errors.New("event: screen signal requires screen attribution")
		}
		if base.Scope != "root" || base.VendorEvent != "screen_rule" {
			return errors.New("event: screen signal requires root scope and screen_rule event")
		}
		if !ascii(base.Vendor) {
			return errors.New("event: screen signal vendor must be a stable ASCII token")
		}
		if err := screen.Validate(); err != nil {
			return err
		}
		if base.Evidence != "" {
			return errors.New("event: screen signal evidence belongs in its attribution")
		}
		if !strings.HasPrefix(screen.Rule, base.Vendor+".") {
			return errors.New("event: screen rule must belong to its vendor")
		}
		if !screenKindMatchesEdge(base.Kind, screen.Edge) {
			return errors.New("event: screen signal kind and edge are incompatible")
		}
		if knownScreenRule(screen.Rule) &&
			!knownScreenRuleMatchesKind(screen.Rule, base.Kind) {
			return errors.New("event: known screen rule has an incompatible kind")
		}
	} else if screen != nil {
		return errors.New("event: only screen signals may contain screen attribution")
	}
	return nil
}

// TerminalAttributionPayload identifies a redacted committed terminal location.
type TerminalAttributionPayload struct {
	Protocol      string `json:"protocol"`
	OutputOffset  uint64 `json:"output_offset"`
	LastOutputSeq uint64 `json:"last_output_seq"`
}

// Validate rejects unsupported protocols and incomplete committed locations.
func (p TerminalAttributionPayload) Validate() error {
	if p.Protocol != "osc9" {
		return fmt.Errorf("event: invalid terminal protocol %q", p.Protocol)
	}
	if p.OutputOffset == 0 {
		return errors.New("event: terminal output_offset must be positive")
	}
	if p.LastOutputSeq == 0 {
		return errors.New("event: terminal last_output_seq must be positive")
	}
	return nil
}

// SignalPayloadV4 adds optional typed terminal attribution.
type SignalPayloadV4 struct {
	SignalPayloadV1
	Screen   *ScreenAttributionPayload   `json:"screen,omitempty"`
	Terminal *TerminalAttributionPayload `json:"terminal,omitempty"`
}

// Validate rejects malformed or mismatched terminal signal attribution.
func (p SignalPayloadV4) Validate() error {
	base := p.SignalPayloadV1
	terminalNotify := p.Terminal != nil
	if err := validateSignalPayload(base, 4, true, true, terminalNotify); err != nil {
		return err
	}
	if err := validateSignalScreenAttribution(base, p.Screen); err != nil {
		return err
	}
	if p.Terminal == nil {
		return nil
	}
	if base.Source != "notify" {
		return errors.New("event: only notify signals may contain terminal attribution")
	}
	if base.Scope != "root" {
		return errors.New("event: terminal notify signal must target root scope")
	}
	return p.Terminal.Validate()
}

func validateSignalPayload(
	p SignalPayloadV1,
	version int,
	allowNotify bool,
	allowScreen bool,
	terminalNotify bool,
) error {
	if p.Version != version {
		return fmt.Errorf("event: unsupported signal payload version %d", p.Version)
	}
	validSource := oneOf(p.Source, "process", "hook", "heuristic", "timer")
	if allowNotify {
		validSource = validSource || p.Source == "notify"
	}
	if allowScreen {
		validSource = validSource || p.Source == "screen"
	}
	if !validSource {
		return fmt.Errorf("event: invalid signal source %q", p.Source)
	}
	if terminalNotify && p.Source != "notify" {
		return errors.New("event: terminal attribution requires notify source")
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
		len(p.VendorSessionRef) > 256 ||
		len(p.VendorSessionID) > 256 ||
		len(p.VendorTurnID) > 256 ||
		len(p.Notification) > 64 ||
		len(p.Evidence) > 128 {
		return errors.New("event: signal metadata exceeds its size limit")
	}
	if p.VendorSessionRef != "" && !ascii(p.VendorSessionRef) {
		return errors.New(
			"event: vendor_session_ref must contain printable ASCII without surrounding whitespace",
		)
	}
	if p.VendorSessionRef != "" && p.VendorSessionID != "" {
		return errors.New("event: signal contains current and legacy vendor session references")
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
	if p.Source == "hook" || p.Source == "notify" || p.Source == "screen" {
		if p.Vendor == "" {
			return errors.New("event: delivered or screen signal vendor is required")
		}
	}
	if p.Source == "hook" || p.Source == "notify" && !terminalNotify {
		if !canonicalUUID(p.DeliveryID) {
			return errors.New("event: delivered signal delivery_id must be a canonical UUID")
		}
	} else if p.Source == "notify" && terminalNotify {
		if p.DeliveryID != "" {
			return errors.New("event: terminal notify signal cannot contain delivery_id")
		}
	} else if p.DeliveryID != "" {
		return errors.New("event: only delivered signals may contain delivery_id")
	}
	if p.Source == "notify" && terminalNotify && p.Kind != "permission_requested" {
		return fmt.Errorf("event: terminal notify source cannot report kind %q", p.Kind)
	}
	if p.Source == "notify" && !terminalNotify && p.Kind != "turn_stopped" {
		return fmt.Errorf("event: notify source cannot report kind %q", p.Kind)
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

func screenKindMatchesEdge(kind, edge string) bool {
	switch kind {
	case "human_input_required":
		return edge == "present"
	case "human_input_resolved":
		return edge == "cleared"
	case "interrupted", "idle_prompt":
		return edge == "present" || edge == "cleared"
	default:
		return false
	}
}

func knownScreenRule(rule string) bool {
	switch rule {
	case "claude.approval_prompt",
		"claude.idle_prompt",
		"claude.interrupted",
		"codex.approval_prompt",
		"codex.idle_prompt":
		return true
	default:
		return false
	}
}

func knownScreenRuleMatchesKind(rule, kind string) bool {
	switch rule {
	case "claude.approval_prompt", "codex.approval_prompt":
		return kind == "human_input_required" || kind == "human_input_resolved"
	case "claude.idle_prompt", "codex.idle_prompt":
		return kind == "idle_prompt"
	case "claude.interrupted":
		return kind == "interrupted"
	default:
		return false
	}
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
	return validateStateEvidencePayload(p, 1, false, false, false)
}

// StateEvidencePayloadV2 adds transition evidence from a notify candidate.
type StateEvidencePayloadV2 StateEvidencePayloadV1

// Validate rejects malformed transition evidence.
func (p StateEvidencePayloadV2) Validate() error {
	return validateStateEvidencePayload(StateEvidencePayloadV1(p), 2, true, false, false)
}

// StateEvidencePayloadV3 adds optional typed screen attribution.
type StateEvidencePayloadV3 struct {
	StateEvidencePayloadV1
	Screen *ScreenAttributionPayload `json:"screen,omitempty"`
}

// Validate rejects malformed or mismatched transition attribution.
func (p StateEvidencePayloadV3) Validate() error {
	base := p.StateEvidencePayloadV1
	if err := validateStateEvidencePayload(base, 3, true, true, false); err != nil {
		return err
	}
	return validateStateScreenAttribution(base, p.Screen)
}

func validateStateScreenAttribution(
	base StateEvidencePayloadV1,
	screen *ScreenAttributionPayload,
) error {
	if base.Source == "screen" {
		if screen == nil {
			return errors.New("event: screen state evidence requires screen attribution")
		}
		if err := screen.Validate(); err != nil {
			return err
		}
		if base.Event != screen.Rule {
			return errors.New("event: screen state evidence event must match its rule")
		}
	} else if screen != nil {
		return errors.New("event: only screen state evidence may contain screen attribution")
	}
	return nil
}

// StateEvidencePayloadV4 adds optional typed terminal attribution.
type StateEvidencePayloadV4 struct {
	StateEvidencePayloadV1
	Screen   *ScreenAttributionPayload   `json:"screen,omitempty"`
	Terminal *TerminalAttributionPayload `json:"terminal,omitempty"`
}

// Validate rejects malformed or mismatched terminal state attribution.
func (p StateEvidencePayloadV4) Validate() error {
	base := p.StateEvidencePayloadV1
	terminalNotify := p.Terminal != nil
	if err := validateStateEvidencePayload(
		base,
		4,
		true,
		true,
		terminalNotify,
	); err != nil {
		return err
	}
	if err := validateStateScreenAttribution(base, p.Screen); err != nil {
		return err
	}
	if p.Terminal == nil {
		return nil
	}
	if base.Source != "notify" {
		return errors.New(
			"event: only notify state evidence may contain terminal attribution",
		)
	}
	return p.Terminal.Validate()
}

func validateStateEvidencePayload(
	p StateEvidencePayloadV1,
	version int,
	allowNotify bool,
	allowScreen bool,
	terminalNotify bool,
) error {
	if p.Version != version {
		return fmt.Errorf("event: unsupported state evidence version %d", p.Version)
	}
	validSource := oneOf(
		p.Source,
		"session",
		"process",
		"hook",
		"heuristic",
		"timer",
		"recovery",
	)
	if allowNotify {
		validSource = validSource || p.Source == "notify"
	}
	if allowScreen {
		validSource = validSource || p.Source == "screen"
	}
	if !validSource {
		return fmt.Errorf("event: invalid state evidence source %q", p.Source)
	}
	if terminalNotify && p.Source != "notify" {
		return errors.New(
			"event: terminal attribution requires notify state evidence",
		)
	}
	if p.Event == "" || len(p.Event) > 64 || !ascii(p.Event) {
		return errors.New("event: state evidence event must contain 1 to 64 ASCII bytes")
	}
	if math.IsNaN(p.Confidence) || math.IsInf(p.Confidence, 0) ||
		p.Confidence < 0 || p.Confidence > 1 {
		return fmt.Errorf("event: invalid state evidence confidence %v", p.Confidence)
	}
	if p.Source == "hook" || p.Source == "notify" && !terminalNotify {
		if !canonicalUUID(p.DeliveryID) {
			return errors.New(
				"event: delivered state evidence delivery_id must be a canonical UUID",
			)
		}
	} else if p.Source == "notify" && terminalNotify {
		if p.DeliveryID != "" {
			return errors.New(
				"event: terminal notify state evidence cannot contain delivery_id",
			)
		}
	} else if p.DeliveryID != "" {
		return errors.New(
			"event: only delivered state evidence may contain delivery_id",
		)
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
		if r > 0x7e || r < 0x20 {
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

// PublishBatch atomically validates and broadcasts one committed batch.
func (h *Hub) PublishBatch(events []Event) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrHubClosed
	}
	nextSeq := h.lastSeq + 1
	for i := range events {
		if events[i].Seq == 0 {
			return ErrUncommittedEvent
		}
		if events[i].Seq != nextSeq+uint64(i) {
			return fmt.Errorf(
				"%w: got %d, want %d",
				ErrSequenceOrder,
				events[i].Seq,
				nextSeq+uint64(i),
			)
		}
	}
	if len(events) == 0 {
		return nil
	}
	h.lastSeq = events[len(events)-1].Seq

	for _, s := range h.subs {
		for _, committed := range events {
			select {
			case s.ch <- committed:
			default:
				atomic.AddUint64(&s.dropped, 1)
			}
		}
	}
	return nil
}

// Publish broadcasts one committed event.
func (h *Hub) Publish(ev Event) error {
	return h.PublishBatch([]Event{ev})
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
