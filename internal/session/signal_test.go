package session

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
)

func TestStartInjectsIsolatedHookRelayEnvironment(t *testing.T) {
	manager, _ := newSignalTestManager(t, detect.PolicyAuto, "http://127.0.0.1:7373")
	status, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "claude",
		Name:    "hook-env",
		Command: "/bin/sh",
		Args: []string{
			"-c",
			`printf '%s|%s|%s|%s\n' "$DROVE_AGENT_ID" "$DROVE_SIGNAL_URL" "$DROVE_SIGNAL_TOKEN" "$DROVE_SIGNAL_VENDOR"; exec /bin/cat`,
		},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	var envLine string
	deadline := time.Now().Add(2 * time.Second)
	for envLine == "" {
		rows, replayErr := manager.Replay(status.AgentID)
		if replayErr != nil {
			t.Fatalf("replay: %v", replayErr)
		}
		for _, row := range rows {
			if row.Type == string(event.TypeOutput) && strings.Contains(row.Payload, "|") {
				envLine = row.Payload
				break
			}
		}
		if envLine != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("hook environment output missing: %+v", rows)
		}
		time.Sleep(time.Millisecond)
	}

	parts := strings.Split(envLine, "|")
	if len(parts) != 4 {
		t.Fatalf("environment line = %q", envLine)
	}
	if parts[0] != status.AgentID {
		t.Fatalf("agent ID = %q, want %q", parts[0], status.AgentID)
	}
	wantURL := "http://127.0.0.1:7373/api/v1/agents/" + status.AgentID + "/signal"
	if parts[1] != wantURL {
		t.Fatalf("signal URL = %q, want %q", parts[1], wantURL)
	}
	tokenBytes, decodeErr := hex.DecodeString(parts[2])
	if decodeErr != nil || len(tokenBytes) != signalTokenBytes {
		t.Fatalf("signal token = %q, decode error = %v", parts[2], decodeErr)
	}
	if parts[3] != "claude" {
		t.Fatalf("signal vendor = %q, want claude", parts[3])
	}
}

func TestAcceptSignalAuthenticatesDeduplicatesAndTransitions(t *testing.T) {
	manager, _ := newSignalTestManager(t, detect.PolicyAuto, "http://127.0.0.1:7373")
	status, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "claude",
		Command: "/bin/cat",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := agent.ID(status.AgentID)
	token := attachedSignalToken(t, manager, id)

	if err := manager.AcceptSignal(
		context.Background(),
		id,
		"Bearer wrong",
		"claude",
		"delivery-auth",
		claudeHook("SessionStart"),
	); !errors.Is(err, ErrSignalUnauthorized) {
		t.Fatalf("wrong token error = %v, want ErrSignalUnauthorized", err)
	}
	if err := manager.AcceptSignal(
		context.Background(),
		id,
		"Bearer "+token,
		"codex",
		"delivery-vendor",
		claudeHook("SessionStart"),
	); !errors.Is(err, ErrSignalVendorMismatch) {
		t.Fatalf("wrong vendor error = %v, want ErrSignalVendorMismatch", err)
	}

	for _, delivery := range []struct {
		id    string
		event string
		raw   []byte
	}{
		{id: "delivery-start", event: "SessionStart", raw: claudeHook("SessionStart")},
		{
			id:    "delivery-blocked",
			event: "PermissionRequest",
			raw: []byte(`{
				"hook_event_name":"PermissionRequest",
				"session_id":"vendor-session",
				"tool_input":{"secret":"never persist"}
			}`),
		},
	} {
		if err := manager.AcceptSignal(
			context.Background(),
			id,
			"Bearer "+token,
			"claude",
			delivery.id,
			delivery.raw,
		); err != nil {
			t.Fatalf("accept %s: %v", delivery.event, err)
		}
	}
	if got, err := manager.Status(id); err != nil || got.State != agent.StateBlocked {
		t.Fatalf("blocked status = %+v, error = %v", got, err)
	}

	rowsBeforeDuplicate, err := manager.Replay(status.AgentID)
	if err != nil {
		t.Fatalf("replay before duplicate: %v", err)
	}
	if err := manager.AcceptSignal(
		context.Background(),
		id,
		"Bearer "+token,
		"claude",
		"delivery-blocked",
		claudeHook("PermissionRequest"),
	); err != nil {
		t.Fatalf("accept duplicate: %v", err)
	}
	rowsAfterDuplicate, err := manager.Replay(status.AgentID)
	if err != nil {
		t.Fatalf("replay after duplicate: %v", err)
	}
	if len(rowsAfterDuplicate) != len(rowsBeforeDuplicate) {
		t.Fatalf(
			"duplicate changed event count from %d to %d",
			len(rowsBeforeDuplicate),
			len(rowsAfterDuplicate),
		)
	}

	if err := manager.AcceptSignal(
		context.Background(),
		id,
		"Bearer "+token,
		"claude",
		"delivery-resume",
		claudeHook("UserPromptSubmit"),
	); err != nil {
		t.Fatalf("accept resume: %v", err)
	}
	if got, err := manager.Status(id); err != nil || got.State != agent.StateWorking {
		t.Fatalf("resumed status = %+v, error = %v", got, err)
	}

	rows, err := manager.Replay(status.AgentID)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	for i, row := range rows {
		if row.Type != string(event.TypeAgentSignal) {
			continue
		}
		if strings.Contains(row.Payload, "never persist") ||
			strings.Contains(row.Payload, "tool_input") {
			t.Fatalf("signal row retained raw payload: %+v", row)
		}
		if i+1 < len(rows) && rows[i+1].Type == string(event.TypeStateChanged) {
			if rows[i+1].Seq != row.Seq+1 {
				t.Fatalf("signal/state batch is not consecutive: %+v %+v", row, rows[i+1])
			}
		}
		var audit signalAuditPayload
		if err := json.Unmarshal([]byte(row.Payload), &audit); err != nil {
			t.Fatalf("decode audit: %v", err)
		}
		if audit.Version != 1 || audit.Source == "" || audit.VendorEvent == "" {
			t.Fatalf("audit = %+v", audit)
		}
	}
}

func TestDetectorFallbackConfidenceAndBlockedRecovery(t *testing.T) {
	manager, _ := newSignalTestManager(t, detect.PolicyAuto, "")
	status, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "claude",
		Command: "/bin/cat",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := agent.ID(status.AgentID)
	entry := manager.reg.For("claude")

	manager.onOutput(id, "Error: low confidence", entry)
	if got, _ := manager.Status(id); got.State != agent.StateWorking {
		t.Fatalf("low-confidence hint changed state to %s", got.State)
	}
	manager.onOutput(id, "Waiting for your input", entry)
	if got, _ := manager.Status(id); got.State != agent.StateBlocked {
		t.Fatalf("blocked hint left state at %s", got.State)
	}
	manager.onOutput(id, "resuming first line", entry)
	if got, _ := manager.Status(id); got.State != agent.StateBlocked {
		t.Fatalf("one activity line changed state to %s", got.State)
	}
	manager.onOutput(id, "resuming second line", entry)
	if got, _ := manager.Status(id); got.State != agent.StateWorking {
		t.Fatalf("sustained output left state at %s", got.State)
	}
}

func TestActiveHookSuppressesHeuristicStateChanges(t *testing.T) {
	manager, _ := newSignalTestManager(t, detect.PolicyAuto, "http://127.0.0.1:7373")
	status, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "claude",
		Command: "/bin/cat",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := agent.ID(status.AgentID)
	token := attachedSignalToken(t, manager, id)
	if err := manager.AcceptSignal(
		context.Background(),
		id,
		"Bearer "+token,
		"claude",
		"delivery-start",
		claudeHook("SessionStart"),
	); err != nil {
		t.Fatalf("activate hook: %v", err)
	}

	manager.onOutput(id, "Waiting for your input", manager.reg.For("claude"))
	if got, _ := manager.Status(id); got.State != agent.StateWorking {
		t.Fatalf("active hook allowed heuristic state %s", got.State)
	}
}

func TestHookIdleConfirmationAndBlockedRecovery(t *testing.T) {
	manager, _ := newSignalTestManager(t, detect.PolicyAuto, "http://127.0.0.1:7373")
	manager.detectorIdleDelay = 5 * time.Millisecond
	status, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "claude",
		Command: "/bin/cat",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := agent.ID(status.AgentID)
	token := attachedSignalToken(t, manager, id)

	if err := manager.AcceptSignal(
		context.Background(),
		id,
		"Bearer "+token,
		"claude",
		"delivery-block",
		claudeHook("PermissionRequest"),
	); err != nil {
		t.Fatalf("block: %v", err)
	}
	if err := manager.AcceptSignal(
		context.Background(),
		id,
		"Bearer "+token,
		"claude",
		"delivery-stop",
		claudeHook("Stop"),
	); err != nil {
		t.Fatalf("stop turn: %v", err)
	}
	waitForState(t, manager, id, agent.StateIdle)

	if err := manager.AcceptSignal(
		context.Background(),
		id,
		"Bearer "+token,
		"claude",
		"delivery-resume",
		claudeHook("UserPromptSubmit"),
	); err != nil {
		t.Fatalf("resume: %v", err)
	}
	waitForState(t, manager, id, agent.StateWorking)
}

func TestRequiredHookPolicyRejectsUnprovisionedOrInactiveSessions(t *testing.T) {
	t.Run("unsupported vendor", func(t *testing.T) {
		manager, st := newSignalTestManager(t, detect.PolicyRequired, "http://127.0.0.1:7373")
		_, err := manager.Start(context.Background(), StartRequest{
			Vendor:  "generic",
			Command: "/bin/cat",
		})
		if !errors.Is(err, ErrHookUnavailable) {
			t.Fatalf("start error = %v, want ErrHookUnavailable", err)
		}
		lastSeq, seqErr := st.LastSeq()
		if seqErr != nil {
			t.Fatalf("last seq: %v", seqErr)
		}
		if lastSeq != 0 {
			t.Fatalf("last seq = %d, want no persisted session", lastSeq)
		}
	})

	t.Run("activation timeout", func(t *testing.T) {
		manager, _ := newSignalTestManager(t, detect.PolicyRequired, "http://127.0.0.1:7373")
		manager.hookActivationTimeout = 10 * time.Millisecond
		_, err := manager.Start(context.Background(), StartRequest{
			Vendor:  "claude",
			Command: "/bin/cat",
		})
		if !errors.Is(err, ErrHookUnavailable) {
			t.Fatalf("start error = %v, want ErrHookUnavailable", err)
		}
		statuses := manager.List()
		if len(statuses) != 1 ||
			statuses[0].State != agent.StateStopped ||
			statuses[0].PID != 0 {
			t.Fatalf("statuses = %+v, want one stopped session", statuses)
		}
	})

	t.Run("observed activation", func(t *testing.T) {
		manager, _ := newSignalTestManager(t, detect.PolicyRequired, "http://127.0.0.1:7373")
		manager.hookActivationTimeout = time.Second
		result := make(chan struct {
			status *Status
			err    error
		}, 1)
		go func() {
			status, err := manager.Start(context.Background(), StartRequest{
				Vendor:  "claude",
				Command: "/bin/cat",
			})
			result <- struct {
				status *Status
				err    error
			}{status: status, err: err}
		}()

		id, token := waitForAttachedSignalToken(t, manager)
		if err := manager.AcceptSignal(
			context.Background(),
			id,
			"Bearer "+token,
			"claude",
			"delivery-start",
			claudeHook("SessionStart"),
		); err != nil {
			t.Fatalf("activate required hook: %v", err)
		}

		select {
		case started := <-result:
			if started.err != nil || started.status == nil ||
				started.status.AgentID != string(id) {
				t.Fatalf("start result = %+v", started)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("required start did not return after activation")
		}
	})
}

func TestStartRejectsRemoteSignalBaseURLBeforePersisting(t *testing.T) {
	manager, st := newSignalTestManager(t, detect.PolicyAuto, "http://example.com:7373")
	_, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "claude",
		Command: "/bin/cat",
	})
	if err == nil || !strings.Contains(err.Error(), "must be loopback") {
		t.Fatalf("start error = %v, want loopback rejection", err)
	}
	lastSeq, seqErr := st.LastSeq()
	if seqErr != nil {
		t.Fatalf("last seq: %v", seqErr)
	}
	if lastSeq != 0 {
		t.Fatalf("last seq = %d, want no persisted session", lastSeq)
	}
}

func TestSignalCommitFailureLeavesStateAndHubUnchanged(t *testing.T) {
	manager, st := newSignalTestManager(t, detect.PolicyAuto, "http://127.0.0.1:7373")
	status, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "claude",
		Command: "/bin/cat",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := agent.ID(status.AgentID)
	token := attachedSignalToken(t, manager, id)
	subscription := manager.hub.Subscribe(2)
	defer manager.hub.Unsubscribe(subscription)
	lastSeq := manager.hub.LastSeq()
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	err = manager.AcceptSignal(
		context.Background(),
		id,
		"Bearer "+token,
		"claude",
		"delivery-block",
		claudeHook("PermissionRequest"),
	)
	if err == nil {
		t.Fatal("signal succeeded with closed store")
	}
	if got, _ := manager.Status(id); got.State != agent.StateWorking {
		t.Fatalf("state = %s, want unchanged working", got.State)
	}
	if manager.hub.LastSeq() != lastSeq {
		t.Fatalf("hub seq = %d, want %d", manager.hub.LastSeq(), lastSeq)
	}
	select {
	case published := <-subscription.C():
		t.Fatalf("published event after failed signal batch: %+v", published)
	default:
	}
}

func newSignalTestManager(
	t *testing.T,
	policy detect.Policy,
	signalBaseURL string,
) (*Manager, *store.Store) {
	t.Helper()

	st := newTestStore(t)
	manager := NewManager(
		adapter.NewRegistry(),
		event.NewHub(0),
		st,
		0,
		WithHookPolicy(policy),
		WithSignalBaseURL(signalBaseURL),
	)
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})
	return manager, st
}

func attachedSignalToken(t *testing.T, manager *Manager, id agent.ID) string {
	t.Helper()

	manager.mu.RLock()
	defer manager.mu.RUnlock()
	running := manager.sessions[id]
	if running == nil || running.signalToken == "" {
		t.Fatalf("agent %q has no signal token", id)
	}
	return running.signalToken
}

func waitForAttachedSignalToken(t *testing.T, manager *Manager) (agent.ID, string) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for {
		manager.mu.RLock()
		for id, running := range manager.sessions {
			if running.signalToken != "" {
				token := running.signalToken
				manager.mu.RUnlock()
				return id, token
			}
		}
		manager.mu.RUnlock()
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for attached signal token")
		}
		time.Sleep(time.Millisecond)
	}
}

func claudeHook(eventName string) []byte {
	return []byte(`{"hook_event_name":"` + eventName + `","session_id":"vendor-session"}`)
}

func waitForState(t *testing.T, manager *Manager, id agent.ID, want agent.State) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for {
		status, err := manager.Status(id)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if status.State == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %s, want %s", status.State, want)
		}
		time.Sleep(time.Millisecond)
	}
}
