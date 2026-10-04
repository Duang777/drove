package event

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewSessionLifecycleCarriesEncodedPayload(t *testing.T) {
	ev := NewSessionLifecycle(7, "session-1", "agent-1", "created", `{"version":1}`)

	if ev.Seq != 7 ||
		ev.Type != TypeSessionLifecycle ||
		ev.SessionID != "session-1" ||
		ev.AgentID != "agent-1" ||
		ev.Reason != "created" ||
		ev.Payload != `{"version":1}` {
		t.Fatalf("session lifecycle event = %+v", ev)
	}
	if ev.Timestamp.IsZero() {
		t.Fatal("session lifecycle timestamp is zero")
	}
}

func TestNewAgentInputCarriesRedactedPayload(t *testing.T) {
	ev := NewAgentInput(8, "session-1", "agent-1", `{"version":1,"bytes":9}`)

	if ev.Seq != 8 ||
		ev.Type != TypeAgentInput ||
		ev.SessionID != "session-1" ||
		ev.AgentID != "agent-1" ||
		ev.Reason != "accepted" ||
		ev.Payload != `{"version":1,"bytes":9}` {
		t.Fatalf("agent input event = %+v", ev)
	}
	if ev.Timestamp.IsZero() {
		t.Fatal("agent input timestamp is zero")
	}
}

func TestNewAgentSignalCarriesNormalizedPayload(t *testing.T) {
	ev := NewAgentSignal(9, "session-1", "agent-1", "hook", `{"version":1}`)

	if ev.Seq != 9 ||
		ev.Type != TypeAgentSignal ||
		ev.SessionID != "session-1" ||
		ev.AgentID != "agent-1" ||
		ev.Reason != "hook" ||
		ev.Payload != `{"version":1}` {
		t.Fatalf("agent signal event = %+v", ev)
	}
}

func TestCommitSealsDraft(t *testing.T) {
	at := time.Date(2026, time.October, 3, 10, 0, 0, 0, time.UTC)
	committed, err := Commit(
		7,
		at,
		NewStateChangedDraft("agent-1", "agent-1", "working", "blocked", "waiting", `{"version":1}`),
	)
	if err != nil {
		t.Fatalf("commit draft: %v", err)
	}
	if committed.Seq != 7 ||
		!committed.Timestamp.Equal(at) ||
		committed.Type != TypeStateChanged ||
		committed.From != "working" ||
		committed.To != "blocked" ||
		committed.Payload != `{"version":1}` {
		t.Fatalf("committed event = %+v", committed)
	}
	if _, err := Commit(0, at, NewOutputDraft("agent-1", "agent-1", "line")); !errors.Is(err, ErrUncommittedEvent) {
		t.Fatalf("zero-sequence commit error = %v", err)
	}
}

func TestAgentResizedPayloadValidationAndCommit(t *testing.T) {
	payload := AgentResizedPayloadV1{
		Version:      AgentResizedPayloadVersion,
		Rows:         50,
		Columns:      160,
		OutputOffset: 98304,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal resize payload: %v", err)
	}
	decoded, err := DecodeAgentResizedPayload(string(encoded))
	if err != nil {
		t.Fatalf("decode resize payload: %v", err)
	}
	if decoded != payload {
		t.Fatalf("decoded resize payload = %+v, want %+v", decoded, payload)
	}

	draft, err := NewAgentResizedDraft(
		"agent-1",
		"agent-1",
		payload.Rows,
		payload.Columns,
		payload.OutputOffset,
	)
	if err != nil {
		t.Fatalf("new resize draft: %v", err)
	}
	committed, err := Commit(
		10,
		time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC),
		draft,
	)
	if err != nil {
		t.Fatalf("commit resize event: %v", err)
	}
	if committed.Type != TypeAgentResized ||
		committed.Reason != "applied" ||
		committed.Payload != string(encoded) {
		t.Fatalf("committed resize event = %+v", committed)
	}

	for _, invalid := range []AgentResizedPayloadV1{
		{Version: 2, Rows: 50, Columns: 160},
		{Version: 1, Columns: 160},
		{Version: 1, Rows: 50},
	} {
		invalidJSON, err := json.Marshal(invalid)
		if err != nil {
			t.Fatalf("marshal invalid payload: %v", err)
		}
		if _, err := DecodeAgentResizedPayload(string(invalidJSON)); err == nil {
			t.Fatalf("decode accepted invalid resize payload %+v", invalid)
		}
	}
	if _, err := DecodeAgentResizedPayload(`{"version":1`); err == nil {
		t.Fatal("decode accepted malformed resize payload")
	}
}

func TestAgentResumedPayloadAndDraft(t *testing.T) {
	payload := AgentResumedPayloadV1{
		Version:          1,
		VendorSessionRef: "vendor-session-1",
	}
	if err := payload.Validate(); err != nil {
		t.Fatalf("validate payload: %v", err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	at := time.Date(2026, time.October, 4, 14, 0, 0, 0, time.UTC)
	committed, err := Commit(
		10,
		at,
		NewAgentResumedDraft("agent-1", "agent-1", string(encoded)),
	)
	if err != nil {
		t.Fatalf("commit resumed draft: %v", err)
	}
	if committed.Type != TypeAgentResumed ||
		committed.Reason != "requested" ||
		committed.Payload != `{"version":1}` ||
		committed.StoredPayload() != string(encoded) ||
		strings.Contains(committed.Payload, "vendor-session-1") {
		t.Fatalf("committed event = %+v", committed)
	}

	for _, ref := range []string{"", " leading", strings.Repeat("a", 257)} {
		invalid := AgentResumedPayloadV1{
			Version:          1,
			VendorSessionRef: ref,
		}
		if err := invalid.Validate(); err == nil {
			t.Fatalf("reference %q passed validation", ref)
		}
	}
}

func TestOutputChunkDraftCopiesPrivateAttachment(t *testing.T) {
	data := []byte("prompt\x00without newline")
	draft, err := NewOutputChunkDraft("agent-1", "agent-1", 17, data)
	if err != nil {
		t.Fatalf("new output chunk draft: %v", err)
	}
	data[0] = 'X'

	at := time.Date(2026, time.October, 4, 10, 0, 0, 0, time.UTC)
	committed, err := Commit(8, at, draft)
	if err != nil {
		t.Fatalf("commit output chunk: %v", err)
	}
	if committed.Type != TypeOutputChunk {
		t.Fatalf("event type = %q, want %q", committed.Type, TypeOutputChunk)
	}
	payload, err := DecodeOutputChunkPayload(committed.Payload)
	if err != nil {
		t.Fatalf("decode hydrated payload: %v", err)
	}
	decoded, err := payload.DecodeData()
	if err != nil {
		t.Fatalf("decode output data: %v", err)
	}
	want := []byte("prompt\x00without newline")
	if !bytes.Equal(decoded, want) {
		t.Fatalf("decoded data = %q, want %q", decoded, want)
	}
	if payload.Offset != 17 || payload.Len != len(want) {
		t.Fatalf("payload = %+v, want offset 17 and len %d", payload, len(want))
	}

	stored, err := DecodeOutputChunkPayload(committed.StoredPayload())
	if err != nil {
		t.Fatalf("decode stored metadata: %v", err)
	}
	if stored.DataB64 != "" {
		t.Fatalf("stored metadata contains Base64 data: %+v", stored)
	}
	attachment := committed.OutputAttachment()
	if !bytes.Equal(attachment, want) {
		t.Fatalf("attachment = %q, want %q", attachment, want)
	}
	attachment[0] = 'Y'
	if got := committed.OutputAttachment(); !bytes.Equal(got, want) {
		t.Fatalf("event attachment mutated through accessor: %q", got)
	}

	encoded, err := json.Marshal(committed)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if bytes.Contains(encoded, []byte(`outputAttachment`)) ||
		bytes.Contains(encoded, []byte(`prompt`)) {
		t.Fatalf("marshaled event exposed private attachment: %s", encoded)
	}
}

func TestOutputChunkPayloadValidation(t *testing.T) {
	valid := OutputChunkPayloadV1{
		Version: OutputChunkPayloadVersion,
		Offset:  4,
		Len:     3,
		DataB64: "YWJj",
	}
	if err := valid.ValidateMetadata(); err != nil {
		t.Fatalf("validate metadata: %v", err)
	}
	if err := valid.ValidateHydrated(); err != nil {
		t.Fatalf("validate hydrated payload: %v", err)
	}
	expired := valid
	expired.DataB64 = ""
	if err := expired.ValidateMetadata(); err != nil {
		t.Fatalf("validate expired metadata: %v", err)
	}
	if err := expired.ValidateHydrated(); err == nil {
		t.Fatal("hydrated validation accepted missing data")
	}

	tests := []struct {
		name    string
		payload OutputChunkPayloadV1
	}{
		{name: "unknown version", payload: OutputChunkPayloadV1{Version: 2, Len: 1}},
		{name: "empty", payload: OutputChunkPayloadV1{Version: 1}},
		{name: "oversized", payload: OutputChunkPayloadV1{Version: 1, Len: MaxOutputChunkBytes + 1}},
		{name: "malformed Base64", payload: OutputChunkPayloadV1{Version: 1, Len: 1, DataB64: "!"}},
		{name: "unpadded Base64", payload: OutputChunkPayloadV1{Version: 1, Len: 2, DataB64: "YWI"}},
		{name: "length mismatch", payload: OutputChunkPayloadV1{Version: 1, Len: 2, DataB64: "YWJj"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.payload.ValidateHydrated(); err == nil {
				t.Fatalf("validation accepted %+v", test.payload)
			}
		})
	}
}

func TestNewOutputChunkDraftRejectsInvalidLengths(t *testing.T) {
	if _, err := NewOutputChunkDraft("agent", "agent", 0, nil); err == nil {
		t.Fatal("constructor accepted an empty chunk")
	}
	if _, err := NewOutputChunkDraft(
		"agent",
		"agent",
		0,
		make([]byte, MaxOutputChunkBytes+1),
	); err == nil {
		t.Fatal("constructor accepted an oversized chunk")
	}
}

func TestHydrateOutputChunkPayloadRejectsLengthMismatch(t *testing.T) {
	metadata := `{"version":1,"offset":9,"len":3}`
	hydrated, err := HydrateOutputChunkPayload(metadata, []byte("abc"))
	if err != nil {
		t.Fatalf("hydrate output chunk: %v", err)
	}
	payload, err := DecodeOutputChunkPayload(hydrated)
	if err != nil {
		t.Fatalf("decode hydrated output chunk: %v", err)
	}
	if err := payload.ValidateHydrated(); err != nil {
		t.Fatalf("validate hydrated output chunk: %v", err)
	}
	if _, err := HydrateOutputChunkPayload(metadata, []byte("ab")); err == nil {
		t.Fatal("hydration accepted a length-mismatched attachment")
	}
}

func TestSignalPayloadV1Validation(t *testing.T) {
	payload := SignalPayloadV1{
		Version:     1,
		Source:      "hook",
		Kind:        "tool_activity",
		Vendor:      "claude",
		VendorEvent: "PostToolUse",
		Scope:       "root",
		Confidence:  1,
		ReceivedAt:  time.Date(2026, time.October, 3, 10, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		DeliveryID:  "550e8400-e29b-41d4-a716-446655440000",
		Outcome:     "observed",
	}
	if err := payload.Validate(); err != nil {
		t.Fatalf("validate signal payload: %v", err)
	}
	legacy := payload
	legacy.Kind = ""
	legacy.Outcome = ""
	legacy.Evidence = "activity"
	if err := legacy.Validate(); err != nil {
		t.Fatalf("validate transitional signal payload: %v", err)
	}

	tests := []struct {
		name   string
		change func(*SignalPayloadV1)
	}{
		{name: "unknown source", change: func(p *SignalPayloadV1) { p.Source = "config" }},
		{name: "unknown kind", change: func(p *SignalPayloadV1) { p.Kind = "completed" }},
		{name: "missing event", change: func(p *SignalPayloadV1) { p.VendorEvent = "" }},
		{name: "invalid scope", change: func(p *SignalPayloadV1) { p.Scope = "child" }},
		{name: "invalid confidence", change: func(p *SignalPayloadV1) { p.Confidence = 2 }},
		{name: "invalid receive time", change: func(p *SignalPayloadV1) { p.ReceivedAt = "today" }},
		{name: "invalid delivery ID", change: func(p *SignalPayloadV1) { p.DeliveryID = "delivery-1" }},
		{name: "unknown outcome", change: func(p *SignalPayloadV1) { p.Outcome = "ignored" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := payload
			test.change(&invalid)
			if err := invalid.Validate(); err == nil {
				t.Fatalf("validation accepted %+v", invalid)
			}
		})
	}
}

func TestSignalPayloadVendorSessionReferenceCompatibility(t *testing.T) {
	base := SignalPayloadV1{
		Version:     1,
		Source:      "hook",
		Kind:        "session_started",
		Vendor:      "claude",
		VendorEvent: "SessionStart",
		Scope:       "root",
		Confidence:  1,
		ReceivedAt:  time.Date(2026, time.October, 4, 10, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		DeliveryID:  "550e8400-e29b-41d4-a716-446655440000",
		Outcome:     "observed",
	}

	current := base
	current.VendorSessionRef = "session-ref"
	if err := current.Validate(); err != nil {
		t.Fatalf("validate current payload: %v", err)
	}
	if got := current.VendorSessionReference(); got != "session-ref" {
		t.Fatalf("current session reference = %q", got)
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		t.Fatalf("encode current payload: %v", err)
	}
	if strings.Contains(string(encoded), "vendor_session_id") ||
		strings.Contains(string(encoded), "vendor_turn_id") {
		t.Fatalf("current payload contains legacy identifiers: %s", encoded)
	}

	legacy := base
	legacy.VendorSessionID = "legacy-session"
	legacy.VendorTurnID = "legacy-turn"
	if err := legacy.Validate(); err != nil {
		t.Fatalf("validate legacy payload: %v", err)
	}
	if got := legacy.VendorSessionReference(); got != "legacy-session" {
		t.Fatalf("legacy session reference = %q", got)
	}

	for _, ref := range []string{" leading", "trailing ", "line\nbreak", strings.Repeat("x", 257)} {
		invalid := base
		invalid.VendorSessionRef = ref
		if err := invalid.Validate(); err == nil {
			t.Fatalf("accepted invalid session reference %q", ref)
		}
	}

	ambiguous := current
	ambiguous.VendorSessionID = "other-session"
	if err := ambiguous.Validate(); err == nil {
		t.Fatal("accepted conflicting current and legacy session references")
	}
}

func TestSignalPayloadV2ValidatesNotifyOnly(t *testing.T) {
	payload := SignalPayloadV2{
		Version:          2,
		Source:           "notify",
		Kind:             "turn_stopped",
		Vendor:           "codex",
		VendorEvent:      "agent-turn-complete",
		Scope:            "root",
		VendorSessionRef: "thread-1",
		Confidence:       1,
		ReceivedAt:       time.Date(2026, time.October, 3, 10, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		DeliveryID:       "550e8400-e29b-41d4-a716-446655440000",
		Outcome:          "candidate",
	}
	if err := payload.Validate(); err != nil {
		t.Fatalf("validate notify payload: %v", err)
	}

	invalidKind := payload
	invalidKind.Kind = "turn_started"
	if err := invalidKind.Validate(); err == nil {
		t.Fatal("notify payload accepted turn_started")
	}

	legacy := SignalPayloadV1(payload)
	legacy.Version = 1
	if err := legacy.Validate(); err == nil {
		t.Fatal("version 1 payload accepted notify source")
	}
}

func TestSignalPayloadV3ValidatesScreenOutcomes(t *testing.T) {
	base := SignalPayloadV1{
		Version:     3,
		Source:      "screen",
		Kind:        "human_input_resolved",
		Vendor:      "claude",
		VendorEvent: "screen_rule",
		Scope:       "root",
		Confidence:  1,
		ReceivedAt:  time.Date(2026, time.October, 4, 10, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
	}
	screen := &ScreenAttributionPayload{
		Rule:          "claude.approval_prompt",
		Edge:          "cleared",
		Region:        "viewport.bottom",
		OutputOffset:  4312,
		LastOutputSeq: 918,
		Evidence:      "approval prompt",
	}
	for _, outcome := range []string{
		"candidate",
		"suppressed",
		"transitioned",
		"stale",
		"terminal",
	} {
		payload := SignalPayloadV3{
			SignalPayloadV1: base,
			Screen:          screen,
		}
		payload.Outcome = outcome
		if err := payload.Validate(); err != nil {
			t.Fatalf("validate %s screen outcome: %v", outcome, err)
		}
	}

	missing := SignalPayloadV3{SignalPayloadV1: base}
	missing.Outcome = "candidate"
	if err := missing.Validate(); err == nil {
		t.Fatal("screen signal accepted missing attribution")
	}
	foreign := SignalPayloadV3{
		SignalPayloadV1: base,
		Screen:          screen,
	}
	foreign.Source = "heuristic"
	foreign.Kind = "heuristic_blocked"
	foreign.Outcome = "candidate"
	if err := foreign.Validate(); err == nil {
		t.Fatal("non-screen signal accepted screen attribution")
	}
	mismatched := *screen
	mismatched.Edge = "present"
	invalid := SignalPayloadV3{
		SignalPayloadV1: base,
		Screen:          &mismatched,
	}
	invalid.Outcome = "candidate"
	if err := invalid.Validate(); err == nil {
		t.Fatal("screen signal accepted incompatible kind and edge")
	}
	wrongRuleKind := *screen
	wrongRuleKind.Edge = "present"
	invalid = SignalPayloadV3{
		SignalPayloadV1: base,
		Screen:          &wrongRuleKind,
	}
	invalid.Kind = "idle_prompt"
	invalid.Outcome = "candidate"
	if err := invalid.Validate(); err == nil {
		t.Fatal("known screen rule accepted an incompatible kind")
	}
	idleCleared := SignalPayloadV3{
		SignalPayloadV1: base,
		Screen: &ScreenAttributionPayload{
			Rule:          "claude.idle_prompt",
			Edge:          "cleared",
			Region:        "viewport.bottom",
			OutputOffset:  4312,
			LastOutputSeq: 918,
			Evidence:      "idle prompt",
		},
	}
	idleCleared.Kind = "idle_prompt"
	idleCleared.Outcome = "suppressed"
	if err := idleCleared.Validate(); err != nil {
		t.Fatalf("validate cleared idle edge: %v", err)
	}
}

func TestStateEvidencePayloadV1Validation(t *testing.T) {
	evidence := StateEvidencePayloadV1{
		Version:    1,
		Source:     "process",
		Event:      "process_started",
		Confidence: 1,
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("validate state evidence: %v", err)
	}

	evidence.DeliveryID = "550e8400-e29b-41d4-a716-446655440000"
	if err := evidence.Validate(); err == nil {
		t.Fatal("validation accepted a non-hook delivery ID")
	}
}

func TestStateEvidencePayloadV2ValidatesNotify(t *testing.T) {
	evidence := StateEvidencePayloadV2{
		Version:    2,
		Source:     "notify",
		Event:      "agent-turn-complete",
		Confidence: 1,
		DeliveryID: "550e8400-e29b-41d4-a716-446655440000",
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("validate notify evidence: %v", err)
	}
}

func TestStateEvidencePayloadV3ValidatesScreenAttribution(t *testing.T) {
	screen := &ScreenAttributionPayload{
		Rule:          "claude.approval_prompt",
		Edge:          "cleared",
		Region:        "viewport.bottom",
		OutputOffset:  4312,
		LastOutputSeq: 918,
		Evidence:      "approval prompt",
	}
	payload := StateEvidencePayloadV3{
		StateEvidencePayloadV1: StateEvidencePayloadV1{
			Version:    3,
			Source:     "screen",
			Event:      "claude.approval_prompt",
			Confidence: 1,
		},
		Screen: screen,
	}
	if err := payload.Validate(); err != nil {
		t.Fatalf("validate screen evidence: %v", err)
	}
	payload.Event = "claude.idle_prompt"
	if err := payload.Validate(); err == nil {
		t.Fatal("screen evidence accepted a mismatched event")
	}
}

func TestHubFanOut(t *testing.T) {
	h := NewHub(0)
	s1 := h.Subscribe(16)
	s2 := h.Subscribe(16)
	defer h.Unsubscribe(s1)
	defer h.Unsubscribe(s2)

	ev := NewOutput(1, "sess", "agent", "hello\n")
	if err := h.Publish(ev); err != nil {
		t.Fatalf("publish: %v", err)
	}

	for _, s := range []*Subscription{s1, s2} {
		select {
		case got := <-s.C():
			if got.Seq != 1 || got.Payload != "hello\n" {
				t.Fatalf("subscriber got %+v", got)
			}
		case <-time.After(time.Second):
			t.Fatal("subscriber did not receive event")
		}
	}
}

func TestHubSlowSubscriberDropped(t *testing.T) {
	h := NewHub(0)
	s := h.Subscribe(1)
	defer h.Unsubscribe(s)

	if err := h.Publish(NewOutput(1, "s", "a", "one")); err != nil {
		t.Fatalf("publish first event: %v", err)
	}
	// 缓冲已满，第二条被丢弃（发布者不阻塞）。
	if err := h.Publish(NewOutput(2, "s", "a", "two")); err != nil {
		t.Fatalf("publish second event: %v", err)
	}
	if err := h.Publish(NewOutput(3, "s", "a", "three")); err != nil {
		t.Fatalf("publish third event: %v", err)
	}

	<-s.C() // 消费第一条
	time.Sleep(10 * time.Millisecond)

	if d := s.Dropped(); d < 2 {
		t.Fatalf("expected >=2 dropped events, got %d", d)
	}
}

func TestPublishWithPresetSeq(t *testing.T) {
	h := NewHub(41)
	ev := NewStateChanged(42, "s", "a", "working", "blocked", "waiting")
	if err := h.Publish(ev); err != nil {
		t.Fatalf("publish preset sequence: %v", err)
	}
	if got := h.LastSeq(); got != 42 {
		t.Fatalf("last seq = %d, want 42", got)
	}
}

func TestPublishBatchValidatesBeforeDelivery(t *testing.T) {
	h := NewHub(4)
	sub := h.Subscribe(4)
	defer h.Unsubscribe(sub)

	err := h.PublishBatch([]Event{
		NewOutput(5, "s", "s", "five"),
		NewOutput(7, "s", "s", "seven"),
	})
	if !errors.Is(err, ErrSequenceOrder) {
		t.Fatalf("publish batch error = %v, want ErrSequenceOrder", err)
	}
	if got := h.LastSeq(); got != 4 {
		t.Fatalf("last seq = %d, want 4", got)
	}
	select {
	case delivered := <-sub.C():
		t.Fatalf("invalid batch partially delivered %+v", delivered)
	default:
	}

	if err := h.PublishBatch([]Event{
		NewOutput(5, "s", "s", "five"),
		NewOutput(6, "s", "s", "six"),
	}); err != nil {
		t.Fatalf("publish valid batch: %v", err)
	}
}

func TestHubRejectsUncommittedAndOutOfOrderEvents(t *testing.T) {
	h := NewHub(4)

	if err := h.Publish(NewOutput(0, "s", "a", "draft")); !errors.Is(err, ErrUncommittedEvent) {
		t.Fatalf("uncommitted publish error = %v, want ErrUncommittedEvent", err)
	}
	if err := h.Publish(NewOutput(4, "s", "a", "old")); !errors.Is(err, ErrSequenceOrder) {
		t.Fatalf("out-of-order publish error = %v, want ErrSequenceOrder", err)
	}
	if got := h.LastSeq(); got != 4 {
		t.Fatalf("last seq = %d, want unchanged 4", got)
	}
}

func TestUnsubscribeClosesChannel(t *testing.T) {
	h := NewHub(0)
	s := h.Subscribe(4)
	h.Unsubscribe(s)
	select {
	case _, ok := <-s.C():
		if ok {
			t.Fatal("channel should be closed after unsubscribe")
		}
	default:
		t.Fatal("expected closed channel to be immediately readable")
	}
}

func TestHubTracksInitialSequence(t *testing.T) {
	h := NewHub(41)

	if got := h.LastSeq(); got != 41 {
		t.Fatalf("last seq = %d, want 41", got)
	}
}

func TestHubCloseClosesSubscriptionsAndIsIdempotent(t *testing.T) {
	h := NewHub(0)
	first := h.Subscribe(1)
	second := h.Subscribe(1)

	h.Close()
	h.Close()
	h.Unsubscribe(first)
	h.Unsubscribe(second)

	for _, sub := range []*Subscription{first, second} {
		select {
		case _, ok := <-sub.C():
			if ok {
				t.Fatal("subscription channel remained open after Hub.Close")
			}
		default:
			t.Fatal("closed subscription channel was not immediately readable")
		}
	}

	afterClose := h.Subscribe(1)
	select {
	case _, ok := <-afterClose.C():
		if ok {
			t.Fatal("subscription created after Hub.Close remained open")
		}
	default:
		t.Fatal("subscription created after Hub.Close was not closed")
	}
	if err := h.Publish(NewOutput(1, "session", "agent", "ignored")); !errors.Is(err, ErrHubClosed) {
		t.Fatalf("publish after close error = %v, want ErrHubClosed", err)
	}
}

func TestHubCloseIsSafeWithConcurrentPublishAndUnsubscribe(t *testing.T) {
	h := NewHub(0)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var seq atomic.Uint64

	for i := 0; i < 16; i++ {
		sub := h.Subscribe(32)
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 100; j++ {
				h.Publish(NewOutput(seq.Add(1), "session", "agent", "line"))
			}
		}()
		go func(sub *Subscription) {
			defer wg.Done()
			<-start
			h.Unsubscribe(sub)
		}(sub)
	}

	close(start)
	h.Close()
	wg.Wait()
}
