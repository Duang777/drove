package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
)

func TestManagerExplainDecodesKnownPayloadVersions(t *testing.T) {
	manager, st := newTestManager(t)
	id := addExplainTestAgent(manager, "explain-versions")
	base := time.Date(2026, time.October, 4, 10, 0, 0, 0, time.UTC)
	deliveryID := "00000000-0000-4000-8000-000000000001"
	screen := &event.ScreenAttributionPayload{
		Rule:          "claude.approval_prompt",
		Edge:          "present",
		Region:        "viewport.bottom",
		OutputOffset:  10,
		LastOutputSeq: 9,
		Evidence:      "approval prompt",
	}

	rows := []store.EventRow{
		explainSignalRow(t, 1, id, base, event.SignalPayloadV1{
			Version:     1,
			Source:      "heuristic",
			Kind:        "output_activity",
			VendorEvent: "output_activity",
			Scope:       "root",
			Evidence:    "output activity",
			Confidence:  1,
			ReceivedAt:  base.Format(time.RFC3339Nano),
			Outcome:     "observed",
		}),
		explainSignalRow(t, 2, id, base.Add(time.Second), event.SignalPayloadV2{
			Version:     2,
			Source:      "notify",
			Kind:        "turn_stopped",
			Vendor:      "claude",
			VendorEvent: "notify",
			Scope:       "root",
			Confidence:  1,
			ReceivedAt:  base.Add(time.Second).Format(time.RFC3339Nano),
			DeliveryID:  deliveryID,
			Outcome:     "candidate",
		}),
		explainSignalRow(t, 3, id, base.Add(2*time.Second), event.SignalPayloadV3{
			SignalPayloadV1: event.SignalPayloadV1{
				Version:     3,
				Source:      "screen",
				Kind:        "human_input_required",
				Vendor:      "claude",
				VendorEvent: "screen_rule",
				Scope:       "root",
				Confidence:  1,
				ReceivedAt:  base.Add(2 * time.Second).Format(time.RFC3339Nano),
				Outcome:     "suppressed",
			},
			Screen: screen,
		}),
		explainStateRow(t, 4, id, base.Add(3*time.Second), "pending", "starting",
			event.StateEvidencePayloadV1{
				Version: 1, Source: "session", Event: "session_start", Confidence: 1,
			}),
		explainStateRow(t, 5, id, base.Add(4*time.Second), "starting", "working",
			event.StateEvidencePayloadV2{
				Version: 2, Source: "notify", Event: "turn_stopped",
				Confidence: 1, DeliveryID: deliveryID,
			}),
		explainStateRow(t, 6, id, base.Add(5*time.Second), "working", "blocked",
			event.StateEvidencePayloadV3{
				StateEvidencePayloadV1: event.StateEvidencePayloadV1{
					Version: 3, Source: "screen",
					Event: "claude.approval_prompt", Confidence: 1,
				},
				Screen: screen,
			}),
	}
	for _, row := range rows {
		if err := st.AppendEvent(row); err != nil {
			t.Fatalf("append seq %d: %v", row.Seq, err)
		}
	}

	explanation, err := manager.Explain(
		context.Background(),
		id,
		ExplainOptions{Limit: len(rows)},
	)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if explanation.AgentID != string(id) ||
		explanation.State != agent.StatePending ||
		explanation.HookStatus != detect.HookDetached ||
		explanation.Attached ||
		explanation.Screen != nil {
		t.Fatalf("explanation header = %+v", explanation)
	}
	if len(explanation.Events) != len(rows) {
		t.Fatalf("event count = %d, want %d", len(explanation.Events), len(rows))
	}
	if got := explanation.Events[0]; got.Source != agent.EvidenceHeuristic ||
		got.Kind != detect.KindOutputActivity ||
		got.Outcome != detect.OutcomeObserved ||
		got.Evidence != "output activity" {
		t.Fatalf("v1 signal = %+v", got)
	}
	if got := explanation.Events[1]; got.Source != agent.EvidenceNotify ||
		got.Kind != detect.KindTurnStopped ||
		got.Outcome != detect.OutcomeCandidate {
		t.Fatalf("v2 signal = %+v", got)
	}
	if got := explanation.Events[2]; got.Source != agent.EvidenceScreen ||
		got.Rule != screen.Rule ||
		got.Edge != agent.ScreenEdgePresent ||
		got.Region != screen.Region ||
		got.Evidence != screen.Evidence ||
		got.SuppressionReason != suppressedSignalReason {
		t.Fatalf("v3 signal = %+v", got)
	}
	if got := explanation.Events[3]; got.From != agent.StatePending ||
		got.To != agent.StateStarting ||
		got.Source != agent.EvidenceSession ||
		got.Evidence != "session_start" {
		t.Fatalf("v1 state evidence = %+v", got)
	}
	if got := explanation.Events[4]; got.Source != agent.EvidenceNotify ||
		got.Evidence != "turn_stopped" {
		t.Fatalf("v2 state evidence = %+v", got)
	}
	if got := explanation.Events[5]; got.Source != agent.EvidenceScreen ||
		got.Rule != screen.Rule ||
		got.Evidence != screen.Evidence {
		t.Fatalf("v3 state evidence = %+v", got)
	}
}

func TestManagerExplainMarksUnsupportedVersionsWithoutPayload(t *testing.T) {
	manager, st := newTestManager(t)
	id := addExplainTestAgent(manager, "explain-unknown")
	now := time.Now().UTC()
	for _, row := range []store.EventRow{
		{
			Seq:       1,
			Timestamp: now,
			Type:      string(event.TypeAgentSignal),
			SessionID: string(id),
			AgentID:   string(id),
			Payload:   `{"version":77,"secret":"signal-secret"}`,
		},
		{
			Seq:       2,
			Timestamp: now.Add(time.Second),
			Type:      string(event.TypeStateChanged),
			SessionID: string(id),
			AgentID:   string(id),
			From:      "pending",
			To:        "starting",
			Reason:    "test",
			Payload:   `{"version":88,"secret":"state-secret"}`,
		},
	} {
		if err := st.AppendEvent(row); err != nil {
			t.Fatalf("append seq %d: %v", row.Seq, err)
		}
	}

	explanation, err := manager.Explain(context.Background(), id, ExplainOptions{})
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if len(explanation.Events) != 2 ||
		explanation.Events[0].UnsupportedVersion == nil ||
		*explanation.Events[0].UnsupportedVersion != 77 ||
		explanation.Events[1].UnsupportedVersion == nil ||
		*explanation.Events[1].UnsupportedVersion != 88 {
		t.Fatalf("unsupported events = %+v", explanation.Events)
	}
	encoded, err := json.Marshal(explanation)
	if err != nil {
		t.Fatalf("marshal explanation: %v", err)
	}
	if strings.Contains(string(encoded), "signal-secret") ||
		strings.Contains(string(encoded), "state-secret") {
		t.Fatalf("typed explanation exposed unknown payload: %s", encoded)
	}
}

func TestManagerExplainRejectsMalformedKnownPayload(t *testing.T) {
	manager, st := newTestManager(t)
	id := addExplainTestAgent(manager, "explain-malformed")
	if err := st.AppendEvent(store.EventRow{
		Seq:       1,
		Timestamp: time.Now().UTC(),
		Type:      string(event.TypeAgentSignal),
		SessionID: string(id),
		AgentID:   string(id),
		Payload:   `{"version":3,"source":"screen"}`,
	}); err != nil {
		t.Fatalf("append malformed signal: %v", err)
	}

	_, err := manager.Explain(context.Background(), id, ExplainOptions{})
	if err == nil || !strings.Contains(err.Error(), "seq 1") {
		t.Fatalf("explain error = %v, want malformed seq context", err)
	}
}

func TestManagerExplainAppliesLimitsAfterRelevantFiltering(t *testing.T) {
	manager, st := newTestManager(t)
	id := addExplainTestAgent(manager, "explain-limits")
	base := time.Now().UTC()
	for seq := uint64(1); seq <= 55; seq++ {
		if err := st.AppendEvent(store.EventRow{
			Seq:       seq,
			Timestamp: base.Add(time.Duration(seq) * time.Second),
			Type:      string(event.TypeStateChanged),
			SessionID: string(id),
			AgentID:   string(id),
			From:      "pending",
			To:        "pending",
			Reason:    "legacy",
		}); err != nil {
			t.Fatalf("append seq %d: %v", seq, err)
		}
	}

	explanation, err := manager.Explain(context.Background(), id, ExplainOptions{})
	if err != nil {
		t.Fatalf("explain default: %v", err)
	}
	if len(explanation.Events) != DefaultExplainLimit ||
		explanation.Events[0].Seq != 6 ||
		explanation.Events[len(explanation.Events)-1].Seq != 55 {
		t.Fatalf("default tail = %+v", explanation.Events)
	}
	explanation, err = manager.Explain(
		context.Background(),
		id,
		ExplainOptions{Limit: 1},
	)
	if err != nil {
		t.Fatalf("explain limit 1: %v", err)
	}
	if len(explanation.Events) != 1 || explanation.Events[0].Seq != 55 {
		t.Fatalf("limit-one tail = %+v", explanation.Events)
	}

	for _, limit := range []int{-1, MaxExplainLimit + 1} {
		_, err := manager.Explain(
			context.Background(),
			id,
			ExplainOptions{Limit: limit},
		)
		if !errors.Is(err, ErrInvalidExplainLimit) {
			t.Fatalf("limit %d error = %v, want ErrInvalidExplainLimit", limit, err)
		}
	}
}

func TestManagerExplainAttachedScreenAndExitClaim(t *testing.T) {
	manager, _ := newTestManager(t)
	clock := newTerminalTestClock(time.Unix(5000, 0).UTC())
	manager.clock = clock
	id := addExplainTestAgent(manager, "explain-attached")
	a, _ := manager.agent(id)
	running := attachTestRuntime(t, manager, a, manager.reg.For("generic"))
	terminalActor := newTerminalTestActor(
		t,
		"generic",
		clock,
		&terminalTestProcess{},
		running.observer,
	)
	running.terminal = terminalActor
	defer terminalActor.Close()
	defer running.observer.Close()

	data := []byte("first\r\nsecond")
	feedTerminalTestChunk(t, terminalActor, data, uint64(len(data)), 1, clock.Now())
	if err := terminalActor.EndOutput(context.Background(), uint64(len(data))); err != nil {
		t.Fatalf("end output: %v", err)
	}

	explanation, err := manager.Explain(context.Background(), id, ExplainOptions{})
	if err != nil {
		t.Fatalf("explain attached: %v", err)
	}
	if !explanation.Attached || explanation.HookStatus != detect.HookOff {
		t.Fatalf("attached explanation = %+v", explanation)
	}
	if explanation.Screen == nil ||
		!explanation.Screen.CapturedAt.Equal(clock.Now()) ||
		len(explanation.Screen.Rows) == 0 ||
		explanation.Screen.Rows[len(explanation.Screen.Rows)-1] != "second" {
		t.Fatalf("attached screen = %+v", explanation.Screen)
	}

	manager.mu.Lock()
	running.exitClaimed = true
	manager.mu.Unlock()
	explanation, err = manager.Explain(context.Background(), id, ExplainOptions{})
	if err != nil {
		t.Fatalf("explain exit-claimed: %v", err)
	}
	if explanation.Attached ||
		explanation.HookStatus != detect.HookDetached ||
		explanation.Screen != nil {
		t.Fatalf("exit-claimed explanation retained live state: %+v", explanation)
	}

	manager.detach(id, running)
	explanation, err = manager.Explain(context.Background(), id, ExplainOptions{})
	if err != nil {
		t.Fatalf("explain detached: %v", err)
	}
	if explanation.Attached ||
		explanation.HookStatus != detect.HookDetached ||
		explanation.Screen != nil {
		t.Fatalf("detached explanation retained live state: %+v", explanation)
	}
}

func TestManagerExplainEmptyHistoryAndUnknownAgent(t *testing.T) {
	manager, _ := newTestManager(t)
	id := addExplainTestAgent(manager, "explain-empty")
	explanation, err := manager.Explain(context.Background(), id, ExplainOptions{})
	if err != nil {
		t.Fatalf("explain empty history: %v", err)
	}
	if explanation.Events == nil || len(explanation.Events) != 0 {
		t.Fatalf("empty events = %#v, want non-nil empty slice", explanation.Events)
	}

	_, err = manager.Explain(
		context.Background(),
		agent.ID("missing"),
		ExplainOptions{},
	)
	if !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("unknown agent error = %v, want ErrUnknownAgent", err)
	}
}

func addExplainTestAgent(manager *Manager, rawID string) agent.ID {
	id := agent.ID(rawID)
	manager.mu.Lock()
	manager.agents[id] = agent.New(
		id,
		agent.WithName(rawID),
		agent.WithVendor("generic"),
		agent.WithHookPolicy(agent.HooksOff),
	)
	manager.mu.Unlock()
	return id
}

func explainSignalRow(
	t *testing.T,
	seq uint64,
	id agent.ID,
	at time.Time,
	payload any,
) store.EventRow {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal signal payload at seq %d: %v", seq, err)
	}
	return store.EventRow{
		Seq:       seq,
		Timestamp: at,
		Type:      string(event.TypeAgentSignal),
		SessionID: string(id),
		AgentID:   string(id),
		Payload:   string(encoded),
	}
}

func explainStateRow(
	t *testing.T,
	seq uint64,
	id agent.ID,
	at time.Time,
	from string,
	to string,
	payload any,
) store.EventRow {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal state payload at seq %d: %v", seq, err)
	}
	return store.EventRow{
		Seq:       seq,
		Timestamp: at,
		Type:      string(event.TypeStateChanged),
		SessionID: string(id),
		AgentID:   string(id),
		From:      from,
		To:        to,
		Reason:    "test",
		Payload:   string(encoded),
	}
}
