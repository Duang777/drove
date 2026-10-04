package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
)

const testSignalToken = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestStartInjectsIsolatedHookRelayEnvironment(t *testing.T) {
	manager, _ := newSignalTestManager(t, "http://127.0.0.1:7373")
	manager.signalSocketPath = "/tmp/drove-test.sock"
	status, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "claude",
		Hooks:   agent.HooksAuto,
		Name:    "hook-env",
		Command: "/bin/sh",
		Args: []string{
			"-c",
			`printf '%s|%s|%s|%s\n' "$DROVE_AGENT_ID" "$DROVE_SIGNAL_URL" "$DROVE_SIGNAL_SOCKET" "$DROVE_SIGNAL_TOKEN"; exec /bin/cat`,
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
			if row.Type == string(event.TypeOutputChunk) {
				output := strings.TrimRight(string(outputChunkData(t, row)), "\r\n")
				if strings.Contains(output, "|") {
					envLine = output
					break
				}
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
	if parts[2] != "/tmp/drove-test.sock" {
		t.Fatalf("signal socket = %q, want injected path", parts[2])
	}
	wantMask := string(newStreamingRedactor([]byte(testSignalToken)).mask)
	if parts[3] != wantMask {
		t.Fatalf("persisted signal token = %q, want redaction", parts[3])
	}
	token := attachedSignalToken(t, manager, agent.ID(status.AgentID))
	tokenBytes, decodeErr := base64.RawURLEncoding.DecodeString(token)
	if decodeErr != nil || len(tokenBytes) != signalTokenBytes {
		t.Fatalf("signal token = %q, decode error = %v", token, decodeErr)
	}
}

func TestStartNormalizesHookPolicyByAdapterCapability(t *testing.T) {
	manager, _ := newSignalTestManager(t, "http://127.0.0.1:7373")

	claudeStatus, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "claude",
		Command: "/bin/cat",
	})
	if err != nil {
		t.Fatalf("start Claude: %v", err)
	}
	if claudeStatus.HookPolicy != agent.HooksAuto ||
		claudeStatus.HookStatus != detect.HookAwaiting {
		t.Fatalf("Claude status = %+v, want auto awaiting_hook", claudeStatus)
	}

	genericStatus, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "generic",
		Command: "/bin/cat",
	})
	if err != nil {
		t.Fatalf("start generic: %v", err)
	}
	if genericStatus.HookPolicy != agent.HooksOff ||
		genericStatus.HookStatus != detect.HookOff {
		t.Fatalf("generic status = %+v, want off", genericStatus)
	}

	fallbackStatus, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "generic",
		Command: "/bin/cat",
		Hooks:   agent.HooksAuto,
	})
	if err != nil {
		t.Fatalf("start generic auto: %v", err)
	}
	if fallbackStatus.HookPolicy != agent.HooksAuto ||
		fallbackStatus.HookStatus != detect.HookFallback {
		t.Fatalf("generic auto status = %+v, want auto fallback", fallbackStatus)
	}
}

func TestHookEnabledStartRequiresConfiguredOriginBeforePersistence(t *testing.T) {
	manager, st := newSignalTestManager(t, "")

	_, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "claude",
		Command: "/bin/cat",
		Hooks:   agent.HooksAuto,
	})
	if !errors.Is(err, ErrSignalOriginUnavailable) {
		t.Fatalf("start error = %v, want ErrSignalOriginUnavailable", err)
	}
	lastSeq, seqErr := st.LastSeq()
	if seqErr != nil {
		t.Fatalf("last sequence: %v", seqErr)
	}
	if lastSeq != 0 || len(manager.List()) != 0 {
		t.Fatalf("failed start persisted state: seq=%d statuses=%+v", lastSeq, manager.List())
	}

	status, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "claude",
		Command: "/bin/cat",
		Hooks:   agent.HooksOff,
	})
	if err != nil {
		t.Fatalf("start hooks-off session without origin: %v", err)
	}
	if status.HookStatus != detect.HookOff {
		t.Fatalf("hook status = %s, want off", status.HookStatus)
	}
}

func TestStartRejectsInvalidHookPolicyBeforePersistence(t *testing.T) {
	manager, st := newSignalTestManager(t, "")

	_, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "generic",
		Command: "/bin/cat",
		Hooks:   "sometimes",
	})
	if !errors.Is(err, ErrInvalidHookPolicy) {
		t.Fatalf("start error = %v, want ErrInvalidHookPolicy", err)
	}
	lastSeq, seqErr := st.LastSeq()
	if seqErr != nil {
		t.Fatalf("last sequence: %v", seqErr)
	}
	if lastSeq != 0 {
		t.Fatalf("last sequence = %d, want 0", lastSeq)
	}
}

func TestDeliverHookAuthenticatesDeduplicatesAndTransitions(t *testing.T) {
	manager, _ := newSignalTestManager(t, "http://127.0.0.1:7373")
	status, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "claude",
		Hooks:   agent.HooksAuto,
		Command: "/bin/cat",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := agent.ID(status.AgentID)
	token := attachedSignalToken(t, manager, id)

	if err := deliverTestHook(manager,
		context.Background(),
		id,
		"Bearer wrong",
		"claude",
		"delivery-auth",
		claudeHook("SessionStart"),
	); !errors.Is(err, ErrHookUnauthorized) {
		t.Fatalf("wrong token error = %v, want ErrHookUnauthorized", err)
	}
	if err := deliverTestHook(manager,
		context.Background(),
		id,
		"Bearer "+token,
		"codex",
		"delivery-vendor",
		claudeHook("SessionStart"),
	); !errors.Is(err, ErrHookVendorMismatch) {
		t.Fatalf("wrong vendor error = %v, want ErrHookVendorMismatch", err)
	}

	for _, delivery := range []struct {
		id    string
		event string
		raw   []byte
	}{
		{id: "delivery-start", event: "SessionStart", raw: claudeHook("SessionStart")},
		{
			id:    "delivery-blocked",
			event: "Elicitation",
			raw: []byte(`{
				"hook_event_name":"Elicitation",
				"session_id":"vendor-session",
				"tool_input":{"secret":"never persist"}
			}`),
		},
	} {
		if err := deliverTestHook(manager,
			context.Background(),
			id,
			"Bearer "+token,
			"claude",
			testDeliveryID(delivery.id),
			delivery.raw,
		); err != nil {
			t.Fatalf("accept %s: %v", delivery.event, err)
		}
	}
	if got, err := manager.Status(id); err != nil || got.State != agent.StateBlocked {
		t.Fatalf("blocked status = %+v, error = %v", got, err)
	}
	managed, ok := manager.managed(id)
	if !ok {
		t.Fatal("managed agent is missing")
	}
	if got := managed.vendorSessionReference(); got != "vendor-session" {
		t.Fatalf("committed vendor session reference = %q", got)
	}
	blockedStatus, err := manager.Status(id)
	if err != nil {
		t.Fatalf("blocked status: %v", err)
	}
	if blockedStatus.HookPolicy != agent.HooksAuto ||
		blockedStatus.HookStatus != detect.HookActive ||
		blockedStatus.LastTransition == nil ||
		blockedStatus.LastTransition.Source != agent.EvidenceHook ||
		blockedStatus.LastTransition.Event != "Elicitation" ||
		blockedStatus.LastTransition.DeliveryID != testDeliveryID("delivery-blocked") {
		t.Fatalf("blocked status metadata = %+v", blockedStatus)
	}

	rowsBeforeDuplicate, err := manager.Replay(status.AgentID)
	if err != nil {
		t.Fatalf("replay before duplicate: %v", err)
	}
	if err := deliverTestHook(manager,
		context.Background(),
		id,
		"Bearer "+token,
		"claude",
		testDeliveryID("delivery-blocked"),
		claudeHook("Elicitation"),
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

	if err := deliverTestHook(manager,
		context.Background(),
		id,
		"Bearer "+token,
		"claude",
		testDeliveryID("delivery-resume"),
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
			strings.Contains(row.Payload, "tool_input") ||
			strings.Contains(row.Payload, "vendor_session_id") ||
			strings.Contains(row.Payload, "vendor_turn_id") {
			t.Fatalf("signal row retained raw payload: %+v", row)
		}
		if i+1 < len(rows) && rows[i+1].Type == string(event.TypeStateChanged) {
			if rows[i+1].Seq != row.Seq+1 {
				t.Fatalf("signal/state batch is not consecutive: %+v %+v", row, rows[i+1])
			}
		}
		var audit event.SignalPayloadV1
		if err := json.Unmarshal([]byte(row.Payload), &audit); err != nil {
			t.Fatalf("decode audit: %v", err)
		}
		if audit.Version != 1 || audit.Source == "" || audit.VendorEvent == "" {
			t.Fatalf("audit = %+v", audit)
		}
		if audit.Vendor == "claude" && audit.VendorSessionRef != "vendor-session" {
			t.Fatalf("hook audit session reference = %q", audit.VendorSessionRef)
		}
	}
}

func TestHookIdleConfirmationAndBlockedRecovery(t *testing.T) {
	manager, _ := newSignalTestManager(t, "http://127.0.0.1:7373")
	manager.detectConfig.StopConfirmation = 5 * time.Millisecond
	status, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "claude",
		Command: "/bin/cat",
		Hooks:   agent.HooksAuto,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := agent.ID(status.AgentID)
	token := attachedSignalToken(t, manager, id)

	if err := deliverTestHook(manager,
		context.Background(),
		id,
		"Bearer "+token,
		"claude",
		testDeliveryID("delivery-block"),
		claudeHook("Elicitation"),
	); err != nil {
		t.Fatalf("block: %v", err)
	}
	if err := deliverTestHook(manager,
		context.Background(),
		id,
		"Bearer "+token,
		"claude",
		testDeliveryID("delivery-stop"),
		claudeHook("Stop"),
	); err != nil {
		t.Fatalf("stop turn: %v", err)
	}
	waitForState(t, manager, id, agent.StateIdle)

	if err := deliverTestHook(manager,
		context.Background(),
		id,
		"Bearer "+token,
		"claude",
		testDeliveryID("delivery-resume"),
		claudeHook("UserPromptSubmit"),
	); err != nil {
		t.Fatalf("resume: %v", err)
	}
	waitForState(t, manager, id, agent.StateWorking)
}

func TestRequiredHookPolicyRejectsUnprovisionedOrInactiveSessions(t *testing.T) {
	t.Run("unsupported vendor", func(t *testing.T) {
		manager, st := newSignalTestManager(t, "http://127.0.0.1:7373")
		_, err := manager.Start(context.Background(), StartRequest{
			Vendor:  "generic",
			Command: "/bin/cat",
			Hooks:   agent.HooksRequired,
		})
		if !errors.Is(err, ErrHookUnsupported) {
			t.Fatalf("start error = %v, want ErrHookUnsupported", err)
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
		manager, _ := newSignalTestManager(t, "http://127.0.0.1:7373")
		manager.detectConfig.HookActivation = 10 * time.Millisecond
		_, err := manager.Start(context.Background(), StartRequest{
			Vendor:  "claude",
			Command: "/bin/cat",
			Hooks:   agent.HooksRequired,
		})
		if !errors.Is(err, ErrHookRequired) {
			t.Fatalf("start error = %v, want ErrHookRequired", err)
		}
		statuses := manager.List()
		if len(statuses) != 1 ||
			statuses[0].State != agent.StateStopped ||
			statuses[0].PID != 0 {
			t.Fatalf("statuses = %+v, want one stopped session", statuses)
		}
	})

	t.Run("short lived oneshot remains stopped without activation", func(t *testing.T) {
		manager, _ := newSignalTestManager(t, "http://127.0.0.1:7373")
		manager.detectConfig.HookActivation = 10 * time.Millisecond
		_, err := manager.Start(context.Background(), StartRequest{
			Vendor:  "claude",
			Command: "/usr/bin/true",
			Mode:    agent.RunModeOneshot,
			Hooks:   agent.HooksRequired,
		})
		if !errors.Is(err, ErrHookRequired) {
			t.Fatalf("start error = %v, want ErrHookRequired", err)
		}
		statuses := manager.List()
		if len(statuses) != 1 || statuses[0].State != agent.StateStopped {
			t.Fatalf("statuses = %+v, want one stopped session", statuses)
		}
	})

	t.Run("observed activation", func(t *testing.T) {
		manager, _ := newSignalTestManager(t, "http://127.0.0.1:7373")
		manager.detectConfig.HookActivation = time.Second
		result := make(chan struct {
			status *Status
			err    error
		}, 1)
		go func() {
			status, err := manager.Start(context.Background(), StartRequest{
				Vendor:  "claude",
				Command: "/bin/cat",
				Hooks:   agent.HooksRequired,
			})
			result <- struct {
				status *Status
				err    error
			}{status: status, err: err}
		}()

		id, token := waitForAttachedSignalToken(t, manager)
		if err := deliverTestHook(manager,
			context.Background(),
			id,
			"Bearer "+token,
			"claude",
			testDeliveryID("delivery-start"),
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

func TestRequiredCodexStartupAnswersTerminalQueryBeforeHookActivation(t *testing.T) {
	manager, _ := newSignalTestManager(t, "http://127.0.0.1:7373")
	manager.detectConfig.HookActivation = 2 * time.Second
	result := make(chan struct {
		status *Status
		err    error
	}, 1)
	go func() {
		status, err := manager.Start(context.Background(), StartRequest{
			Vendor:  "codex",
			Name:    "codex-query-startup",
			Command: "/bin/sh",
			Args: []string{
				"-c",
				`stty raw -echo; printf '\033[6n'; dd bs=1 count=6 >/dev/null 2>&1; printf 'QUERY_OK\n'; sleep 30`,
			},
			Hooks: agent.HooksRequired,
		})
		result <- struct {
			status *Status
			err    error
		}{status: status, err: err}
	}()

	id, token := waitForAttachedSignalToken(t, manager)
	waitForOutputContains(t, manager, id, "QUERY_OK")
	select {
	case started := <-result:
		t.Fatalf("start returned before hook activation: %+v", started)
	default:
	}

	if err := deliverTestHook(
		manager,
		context.Background(),
		id,
		"Bearer "+token,
		"codex",
		testDeliveryID("codex-query-startup"),
		[]byte(`{"hook_event_name":"SessionStart","session_id":"vendor-session"}`),
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
		t.Fatal("required start did not return after query reply and hook activation")
	}

	rows, err := manager.Replay(string(id))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	for _, row := range rows {
		if row.Type == string(event.TypeAgentInput) {
			t.Fatalf("terminal query reply created input audit: %+v", row)
		}
		if row.Type == string(event.TypeOutputChunk) &&
			bytes.Contains(outputChunkData(t, row), []byte("\x1b[1;1R")) {
			t.Fatalf("terminal query reply entered durable output: %+v", row)
		}
	}
	if err := manager.Stop(id); err != nil {
		t.Fatalf("stop query child: %v", err)
	}
}

func TestUnknownHookEventDoesNotActivatePolicy(t *testing.T) {
	t.Run("auto falls back", func(t *testing.T) {
		manager, _ := newSignalTestManager(t, "http://127.0.0.1:7373")
		manager.detectConfig.HookActivation = 20 * time.Millisecond
		status, err := manager.Start(context.Background(), StartRequest{
			Vendor:  "claude",
			Command: "/bin/cat",
			Hooks:   agent.HooksAuto,
		})
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		id := agent.ID(status.AgentID)
		err = deliverTestHook(
			manager,
			context.Background(),
			id,
			"Bearer "+attachedSignalToken(t, manager, id),
			"claude",
			testDeliveryID("unknown-auto"),
			claudeHook("FutureEvent"),
		)
		if !errors.Is(err, ErrHookInvalid) {
			t.Fatalf("unknown event error = %v, want ErrHookInvalid", err)
		}
		waitForHookStatus(t, manager, id, detect.HookFallback)
	})

	t.Run("required fails", func(t *testing.T) {
		manager, _ := newSignalTestManager(t, "http://127.0.0.1:7373")
		manager.detectConfig.HookActivation = 20 * time.Millisecond
		result := make(chan error, 1)
		go func() {
			_, err := manager.Start(context.Background(), StartRequest{
				Vendor:  "codex",
				Command: "/bin/cat",
				Hooks:   agent.HooksRequired,
			})
			result <- err
		}()

		id, token := waitForAttachedSignalToken(t, manager)
		err := deliverTestHook(
			manager,
			context.Background(),
			id,
			"Bearer "+token,
			"codex",
			testDeliveryID("unknown-required"),
			[]byte(`{"hook_event_name":"FutureEvent","session_id":"vendor-session"}`),
		)
		if !errors.Is(err, ErrHookInvalid) {
			t.Fatalf("unknown event error = %v, want ErrHookInvalid", err)
		}
		select {
		case err := <-result:
			if !errors.Is(err, ErrHookRequired) {
				t.Fatalf("start error = %v, want ErrHookRequired", err)
			}
		case <-time.After(time.Second):
			t.Fatal("required start did not fail after activation deadline")
		}
	})
}

func TestIgnoredCodexNotifyWritesNoEventAndDoesNotActivateHooks(t *testing.T) {
	manager, _ := newSignalTestManager(t, "http://127.0.0.1:7373")
	manager.detectConfig.HookActivation = 20 * time.Millisecond
	status, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "codex",
		Command: "/bin/cat",
		Hooks:   agent.HooksAuto,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := agent.ID(status.AgentID)
	before, err := manager.Replay(status.AgentID)
	if err != nil {
		t.Fatalf("replay before notify: %v", err)
	}

	err = deliverTestHook(
		manager,
		context.Background(),
		id,
		"Bearer "+attachedSignalToken(t, manager, id),
		"codex",
		testDeliveryID("ignored-title"),
		[]byte(`{
			"type":"agent-turn-complete",
			"thread-id":"title-thread",
			"turn-id":"title-turn",
			"input-messages":[
				"Generate a concise, single-line task title for this work"
			]
		}`),
	)
	if err != nil {
		t.Fatalf("deliver ignored notify: %v", err)
	}
	after, err := manager.Replay(status.AgentID)
	if err != nil {
		t.Fatalf("replay after notify: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("events before=%d after=%d, want unchanged", len(before), len(after))
	}
	waitForHookStatus(t, manager, id, detect.HookFallback)
}

func TestCodexNotifyPersistsVersionTwoAndConfirmsFallbackIdle(t *testing.T) {
	manager, _ := newSignalTestManager(t, "http://127.0.0.1:7373")
	manager.detectConfig.HookActivation = 20 * time.Millisecond
	manager.detectConfig.StopConfirmation = 20 * time.Millisecond
	status, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "codex",
		Command: "/bin/cat",
		Hooks:   agent.HooksAuto,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := agent.ID(status.AgentID)
	waitForHookStatus(t, manager, id, detect.HookFallback)

	err = deliverTestHook(
		manager,
		context.Background(),
		id,
		"Bearer "+attachedSignalToken(t, manager, id),
		"codex",
		testDeliveryID("notify-idle"),
		[]byte(`{
			"type":"agent-turn-complete",
			"thread-id":"thread-1",
			"turn-id":"turn-1",
			"input-messages":["private prompt"],
			"last-assistant-message":"private response"
		}`),
	)
	if err != nil {
		t.Fatalf("deliver notify: %v", err)
	}
	waitForState(t, manager, id, agent.StateIdle)

	rows, err := manager.Replay(status.AgentID)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	var (
		foundSignal bool
		foundState  bool
	)
	for _, row := range rows {
		switch {
		case row.Type == string(event.TypeAgentSignal) &&
			strings.Contains(row.Payload, `"source":"notify"`):
			foundSignal = strings.Contains(row.Payload, `"version":2`) &&
				!strings.Contains(row.Payload, "private")
		case row.Type == string(event.TypeStateChanged) &&
			row.To == string(agent.StateIdle):
			foundState = strings.Contains(row.Payload, `"version":2`) &&
				strings.Contains(row.Payload, `"source":"notify"`)
		}
	}
	if !foundSignal || !foundState {
		t.Fatalf(
			"notify signal=%t state=%t rows=%+v",
			foundSignal,
			foundState,
			rows,
		)
	}
	current, err := manager.Status(id)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if current.HookStatus != detect.HookFallback {
		t.Fatalf("hook status = %s, want fallback", current.HookStatus)
	}
}

func TestDeliverHookWaitsForProcessStartCommit(t *testing.T) {
	manager, _ := newSignalTestManager(
		t,
		"http://127.0.0.1:7373",
	)
	a := agent.New(
		"starting-agent",
		agent.WithName("starting-agent"),
		agent.WithVendor("claude"),
		agent.WithRunMode(agent.RunModeInteractive),
		agent.WithHookPolicy(agent.HooksRequired),
	)
	commitTestState(t, manager, a, agent.StateStarting, "test start")
	managed := newManagedAgent(a)
	running, _, _, err := manager.prepareManagedRuntime(
		managed,
		manager.reg.For("claude"),
		false,
	)
	if err != nil {
		t.Fatalf("prepare runtime: %v", err)
	}
	running.process = &fakeProcessSession{}
	manager.mu.Lock()
	manager.agents[a.ID()] = managed
	manager.sessions[a.ID()] = running
	manager.mu.Unlock()
	t.Cleanup(func() {
		manager.detach(a.ID(), running)
		running.observer.Close()
	})

	result := make(chan error, 1)
	go func() {
		result <- deliverTestHook(manager,
			context.Background(),
			a.ID(),
			"Bearer "+testSignalToken,
			"claude",
			testDeliveryID("delivery-starting"),
			claudeHook("SessionStart"),
		)
	}()
	select {
	case signalErr := <-result:
		t.Fatalf("starting signal returned before readiness: %v", signalErr)
	case <-time.After(10 * time.Millisecond):
	}

	started, err := processObservation(
		detect.KindProcessStarted,
		manager.clock.Now(),
		&detect.ProcessFact{HookAvailable: true},
	)
	if err != nil {
		t.Fatalf("process observation: %v", err)
	}
	if err := running.observer.Deliver(context.Background(), started); err != nil {
		t.Fatalf("commit process start: %v", err)
	}
	close(running.signalReady)
	if err := <-result; err != nil {
		t.Fatalf("accept starting signal: %v", err)
	}
	select {
	case <-running.observer.Active():
	default:
		t.Fatal("starting signal did not activate hooks")
	}
}

func TestConfigureSignalOriginRejectsRemoteHost(t *testing.T) {
	manager, _ := newSignalTestManager(t, "")
	err := manager.ConfigureSignalOrigin(&url.URL{
		Scheme: "http",
		Host:   "example.com:7373",
	})
	if !errors.Is(err, ErrSignalOriginUnavailable) {
		t.Fatalf("configure error = %v, want ErrSignalOriginUnavailable", err)
	}
}

func TestConfigureSignalOriginAcceptsOnePathlessLoopbackOrigin(t *testing.T) {
	manager, _ := newSignalTestManager(t, "")
	origin := &url.URL{Scheme: "http", Host: "[::1]:7373"}
	if err := manager.ConfigureSignalOrigin(origin); err != nil {
		t.Fatalf("configure origin: %v", err)
	}
	if err := manager.ConfigureSignalOrigin(origin); !errors.Is(err, ErrSignalOriginUnavailable) {
		t.Fatalf("second configure error = %v, want ErrSignalOriginUnavailable", err)
	}

	other, _ := newSignalTestManager(t, "")
	for _, invalid := range []*url.URL{
		{Scheme: "https", Host: "127.0.0.1:7373"},
		{Scheme: "http", Host: "127.0.0.1:7373", Path: "/"},
		{Scheme: "http", Host: "127.0.0.1:7373", RawQuery: "debug=1"},
		{Scheme: "http", Host: "127.0.0.1:7373", Fragment: "signal"},
	} {
		if err := other.ConfigureSignalOrigin(invalid); !errors.Is(err, ErrSignalOriginUnavailable) {
			t.Fatalf("configure %+v error = %v, want ErrSignalOriginUnavailable", invalid, err)
		}
	}
}

func TestExitClaimInvalidatesSignalCredential(t *testing.T) {
	manager, _ := newSignalTestManager(t, "http://127.0.0.1:7373")
	status, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "claude",
		Command: "/bin/cat",
		Hooks:   agent.HooksAuto,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := agent.ID(status.AgentID)
	token := attachedSignalToken(t, manager, id)

	manager.mu.RLock()
	running := manager.sessions[id]
	manager.mu.RUnlock()
	if err := manager.Stop(id); err != nil {
		t.Fatalf("stop: %v", err)
	}
	waitForState(t, manager, id, agent.StateStopped)

	manager.mu.RLock()
	hasToken := running.hasSignalToken
	digest := running.signalDigest
	manager.mu.RUnlock()
	if hasToken || digest != (signalTokenDigest{}) {
		t.Fatalf("signal credential remains after exit: present=%t digest=%x", hasToken, digest)
	}
	err = deliverTestHook(
		manager,
		context.Background(),
		id,
		"Bearer "+token,
		"claude",
		testDeliveryID("delivery-after-exit"),
		claudeHook("SessionStart"),
	)
	if !errors.Is(err, ErrHookDetached) {
		t.Fatalf("delivery after exit error = %v, want ErrHookDetached", err)
	}
}

func TestDeliverHookReturnsBackpressureWhenObservationInboxIsFull(t *testing.T) {
	manager, _ := newSignalTestManager(t, "http://127.0.0.1:7373")
	status, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "claude",
		Command: "/bin/cat",
		Hooks:   agent.HooksAuto,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := agent.ID(status.AgentID)
	token := attachedSignalToken(t, manager, id)

	saturated := &observationActor{
		requests: make(chan observationRequest, observationInboxSize),
	}
	for range observationInboxSize {
		saturated.requests <- observationRequest{}
	}
	manager.mu.Lock()
	running := manager.sessions[id]
	original := running.observer
	running.observer = saturated
	manager.mu.Unlock()
	t.Cleanup(func() {
		manager.mu.Lock()
		if manager.sessions[id] == running {
			running.observer = original
		}
		manager.mu.Unlock()
	})

	startedAt := time.Now()
	err = deliverTestHook(
		manager,
		context.Background(),
		id,
		"Bearer "+token,
		"claude",
		testDeliveryID("delivery-backpressure"),
		claudeHook("SessionStart"),
	)
	if !errors.Is(err, ErrHookBackpressure) {
		t.Fatalf("delivery error = %v, want ErrHookBackpressure", err)
	}
	if elapsed := time.Since(startedAt); elapsed < signalAdmissionWait {
		t.Fatalf("delivery returned after %s, want at least %s", elapsed, signalAdmissionWait)
	}
}

func TestSignalCommitFailureLeavesStateAndHubUnchanged(t *testing.T) {
	manager, st := newSignalTestManager(t, "http://127.0.0.1:7373")
	status, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "claude",
		Command: "/bin/cat",
		Hooks:   agent.HooksAuto,
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

	err = deliverTestHook(manager,
		context.Background(),
		id,
		"Bearer "+token,
		"claude",
		testDeliveryID("delivery-block"),
		claudeHook("PermissionRequest"),
	)
	if !errors.Is(err, ErrEventCommitterUnavailable) {
		t.Fatalf("signal error = %v, want ErrEventCommitterUnavailable", err)
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
	signalBaseURL string,
) (*Manager, *store.Store) {
	t.Helper()

	st := newTestStore(t)
	manager := NewManager(adapter.NewRegistry(), event.NewHub(0), st, 0)
	manager.newCredential = func() (string, signalTokenDigest, error) {
		return testSignalToken,
			signalTokenDigest(sha256.Sum256([]byte(testSignalToken))),
			nil
	}
	if signalBaseURL != "" {
		origin, err := url.Parse(signalBaseURL)
		if err != nil {
			t.Fatalf("parse signal origin: %v", err)
		}
		if err := manager.ConfigureSignalOrigin(origin); err != nil {
			t.Fatalf("configure signal origin: %v", err)
		}
	}
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
	if running == nil ||
		!verifySignalToken(running.signalDigest, running.hasSignalToken, testSignalToken) {
		t.Fatalf("agent %q has no signal token", id)
	}
	return testSignalToken
}

func waitForAttachedSignalToken(t *testing.T, manager *Manager) (agent.ID, string) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for {
		manager.mu.RLock()
		for id, running := range manager.sessions {
			if verifySignalToken(
				running.signalDigest,
				running.hasSignalToken,
				testSignalToken,
			) {
				manager.mu.RUnlock()
				return id, testSignalToken
			}
		}
		manager.mu.RUnlock()
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for attached signal token")
		}
		time.Sleep(time.Millisecond)
	}
}

func deliverTestHook(
	manager *Manager,
	ctx context.Context,
	id agent.ID,
	authorization string,
	vendor string,
	deliveryID string,
	payload []byte,
) error {
	token, _ := strings.CutPrefix(authorization, "Bearer ")
	return manager.DeliverHook(ctx, id, HookDelivery{
		Token:      token,
		Vendor:     vendor,
		DeliveryID: deliveryID,
		Payload:    payload,
	})
}

func claudeHook(eventName string) []byte {
	return []byte(`{"hook_event_name":"` + eventName + `","session_id":"vendor-session"}`)
}

func testDeliveryID(label string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(label)).String()
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

func waitForOutputContains(
	t *testing.T,
	manager *Manager,
	id agent.ID,
	want string,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		rows, err := manager.Replay(string(id))
		if err != nil {
			t.Fatalf("replay output: %v", err)
		}
		if bytes.Contains(joinOutputRows(t, rows), []byte(want)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("output did not contain %q", want)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForHookStatus(
	t *testing.T,
	manager *Manager,
	id agent.ID,
	want detect.HookStatus,
) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for {
		status, err := manager.Status(id)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if status.HookStatus == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("hook status = %s, want %s", status.HookStatus, want)
		}
		time.Sleep(time.Millisecond)
	}
}
