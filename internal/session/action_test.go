package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/pty"
	"github.com/Duang777/drove/internal/term"
)

const (
	actionTestStateSeq = StateSeq(42)
	actionTestDeviceID = "123e4567-e89b-42d3-a456-426614174000"
)

func TestActionContextAndRespondUseCurrentApprovalScreen(t *testing.T) {
	tests := []struct {
		name       string
		vendor     string
		kind       agent.ActionKind
		reply      string
		wantInput  []byte
		replyBytes int
	}{
		{
			name:      "Claude approve",
			vendor:    "claude",
			kind:      agent.ActionApprove,
			wantInput: []byte("1\r"),
		},
		{
			name:       "Codex trimmed reply",
			vendor:     "codex",
			kind:       agent.ActionReply,
			reply:      "  use read-only  ",
			wantInput:  []byte("\x1buse read-only\r"),
			replyBytes: len("use read-only"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newActionTestHarness(t, test.vendor, "approval")

			actionContext, err := harness.manager.ActionContext(
				context.Background(),
				harness.id,
				actionTestStateSeq,
			)
			if err != nil {
				t.Fatalf("ActionContext: %v", err)
			}
			if actionContext.StateSeq != actionTestStateSeq ||
				actionContext.Screen == nil ||
				len(actionContext.Actions) != 3 {
				t.Fatalf("action context = %+v", actionContext)
			}

			result, err := harness.manager.Respond(
				context.Background(),
				harness.id,
				ActionRequest{
					ExpectedStateSeq: actionTestStateSeq,
					Kind:             test.kind,
					Reply:            test.reply,
					Channel:          "web_push",
					DeviceID:         actionTestDeviceID,
				},
			)
			if err != nil {
				t.Fatalf("Respond: %v", err)
			}
			if result.StateSeq != actionTestStateSeq ||
				result.BytesWritten != len(test.wantInput) ||
				result.InputSeq != result.ActionSeq+1 {
				t.Fatalf("action result = %+v", result)
			}
			writes := harness.process.Writes()
			if len(writes) != 1 || !bytes.Equal(writes[0], test.wantInput) {
				t.Fatalf("PTY writes = %q, want [%q]", writes, test.wantInput)
			}

			rows := harness.store.Rows()
			if len(rows) < 2 {
				t.Fatalf("stored rows = %+v", rows)
			}
			actionRow := rows[len(rows)-2]
			inputRow := rows[len(rows)-1]
			if actionRow.Type != string(event.TypeAgentAction) ||
				inputRow.Type != string(event.TypeAgentInput) ||
				actionRow.Seq+1 != inputRow.Seq ||
				!actionRow.Timestamp.Equal(inputRow.Timestamp) {
				t.Fatalf("audit rows = %+v, %+v", actionRow, inputRow)
			}
			payload, err := event.DecodeAgentActionPayload(actionRow.Payload)
			if err != nil {
				t.Fatalf("decode action audit: %v", err)
			}
			if payload.Action != string(test.kind) ||
				payload.BlockedSeq != actionTestStateSeq.String() ||
				payload.ReplyBytes != test.replyBytes ||
				payload.PromptRule != test.vendor+".approval_prompt" {
				t.Fatalf("action payload = %+v", payload)
			}
			if test.reply != "" &&
				(strings.Contains(actionRow.Payload, "use read-only") ||
					strings.Contains(inputRow.Payload, "use read-only")) {
				t.Fatal("audit rows contain reply text")
			}
		})
	}
}

func TestRespondRejectsStaleOrUnavailablePromptWithoutWriting(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		seq     StateSeq
		want    error
	}{
		{
			name:    "stale sequence",
			fixture: "approval",
			seq:     actionTestStateSeq - 1,
			want:    ErrActionStale,
		},
		{
			name:    "cleared prompt",
			fixture: "idle_after_approval",
			seq:     actionTestStateSeq,
			want:    ErrActionUnavailable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newActionTestHarness(t, "claude", test.fixture)
			_, err := harness.manager.Respond(
				context.Background(),
				harness.id,
				ActionRequest{
					ExpectedStateSeq: test.seq,
					Kind:             agent.ActionApprove,
					Channel:          "web_push",
					DeviceID:         actionTestDeviceID,
				},
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("Respond error = %v, want %v", err, test.want)
			}
			if writes := harness.process.Writes(); len(writes) != 0 {
				t.Fatalf("PTY writes = %q, want none", writes)
			}
		})
	}
}

func TestLocalInputFencesRemoteResponse(t *testing.T) {
	harness := newActionTestHarness(t, "claude", "approval")
	local := []byte("local response\r")
	if _, err := harness.manager.SendInput(harness.id, local); err != nil {
		t.Fatalf("SendInput: %v", err)
	}

	_, err := harness.manager.Respond(
		context.Background(),
		harness.id,
		ActionRequest{
			ExpectedStateSeq: actionTestStateSeq,
			Kind:             agent.ActionApprove,
			Channel:          "web_push",
			DeviceID:         actionTestDeviceID,
		},
	)
	if !errors.Is(err, ErrActionAlreadyAnswered) {
		t.Fatalf("Respond error = %v, want ErrActionAlreadyAnswered", err)
	}
	writes := harness.process.Writes()
	if len(writes) != 1 || !bytes.Equal(writes[0], local) {
		t.Fatalf("PTY writes = %q, want local input only", writes)
	}
}

func TestAttachmentInputFencesRemoteResponse(t *testing.T) {
	harness := newActionTestHarness(t, "claude", "approval")
	attachment, err := harness.manager.AttachTerminal(
		context.Background(),
		harness.id,
		AttachmentOptions{
			Purpose: AttachmentPurposeUser,
			Mode:    AttachmentWritable,
			Rows:    20,
			Columns: 100,
		},
	)
	if err != nil {
		t.Fatalf("AttachTerminal: %v", err)
	}
	defer attachment.Close()
	if _, err := attachment.SendInput(
		context.Background(),
		[]byte("local response\r"),
	); err != nil {
		t.Fatalf("attachment SendInput: %v", err)
	}

	if _, err := harness.manager.Respond(
		context.Background(),
		harness.id,
		harness.approveRequest(),
	); !errors.Is(err, ErrActionAlreadyAnswered) {
		t.Fatalf("Respond error = %v, want ErrActionAlreadyAnswered", err)
	}
}

func TestRespondWriteFailureControlsResponseFence(t *testing.T) {
	t.Run("zero bytes leaves fence open", func(t *testing.T) {
		harness := newActionTestHarness(t, "claude", "approval")
		harness.setWriteBehavior(0, pty.ErrWriteBackpressure)

		result, err := harness.manager.Respond(
			context.Background(),
			harness.id,
			harness.approveRequest(),
		)
		if !errors.Is(err, ErrActionBackpressure) || result.BytesWritten != 0 {
			t.Fatalf("first Respond result=%+v error=%v", result, err)
		}
		harness.setWriteBehavior(0, nil)
		if _, err := harness.manager.Respond(
			context.Background(),
			harness.id,
			harness.approveRequest(),
		); err != nil {
			t.Fatalf("retry after zero-byte failure: %v", err)
		}
	})

	t.Run("partial write closes fence", func(t *testing.T) {
		harness := newActionTestHarness(t, "claude", "approval")
		harness.setWriteBehavior(1, nil)

		result, err := harness.manager.Respond(
			context.Background(),
			harness.id,
			harness.approveRequest(),
		)
		if !errors.Is(err, ErrActionWrite) ||
			result.BytesWritten != 1 ||
			!strings.Contains(err.Error(), "do not retry") {
			t.Fatalf("partial Respond result=%+v error=%v", result, err)
		}
		harness.setWriteBehavior(0, nil)
		if _, err := harness.manager.Respond(
			context.Background(),
			harness.id,
			harness.approveRequest(),
		); !errors.Is(err, ErrActionAlreadyAnswered) {
			t.Fatalf("retry error = %v, want ErrActionAlreadyAnswered", err)
		}
	})
}

func TestRespondAuditFailureClosesResponseFence(t *testing.T) {
	harness := newActionTestHarness(t, "claude", "approval")
	harness.store.mu.Lock()
	harness.store.appendErr = errors.New("disk unavailable")
	harness.store.mu.Unlock()

	result, err := harness.manager.Respond(
		context.Background(),
		harness.id,
		harness.approveRequest(),
	)
	if !errors.Is(err, ErrActionAudit) ||
		result.BytesWritten != len("1\r") ||
		!strings.Contains(err.Error(), "do not retry") {
		t.Fatalf("Respond result=%+v error=%v", result, err)
	}
	if _, err := harness.manager.Respond(
		context.Background(),
		harness.id,
		harness.approveRequest(),
	); !errors.Is(err, ErrActionAlreadyAnswered) {
		t.Fatalf("retry error = %v, want ErrActionAlreadyAnswered", err)
	}
}

func TestConcurrentResponsesWriteOnlyOnce(t *testing.T) {
	harness := newActionTestHarness(t, "claude", "approval")
	writeRelease := make(chan struct{})
	harness.process.mu.Lock()
	harness.process.writeStarted = make(chan struct{})
	harness.process.writeRelease = writeRelease
	writeStarted := harness.process.writeStarted
	harness.process.mu.Unlock()

	first := make(chan error, 1)
	go func() {
		_, err := harness.manager.Respond(
			context.Background(),
			harness.id,
			harness.approveRequest(),
		)
		first <- err
	}()
	<-writeStarted

	second := make(chan error, 1)
	go func() {
		_, err := harness.manager.Respond(
			context.Background(),
			harness.id,
			harness.approveRequest(),
		)
		second <- err
	}()
	close(writeRelease)

	if err := <-first; err != nil {
		t.Fatalf("first Respond: %v", err)
	}
	if err := <-second; !errors.Is(err, ErrActionAlreadyAnswered) {
		t.Fatalf("second Respond error = %v, want ErrActionAlreadyAnswered", err)
	}
	if writes := harness.process.Writes(); len(writes) != 1 {
		t.Fatalf("PTY writes = %q, want one", writes)
	}
}

func TestRespondWaitsForEarlierOutput(t *testing.T) {
	harness := newActionTestHarness(t, "claude", "")
	appendStarted := make(chan struct{})
	appendRelease := make(chan struct{})
	var appendOnce sync.Once
	harness.store.mu.Lock()
	harness.store.onAppend = func() {
		appendOnce.Do(func() {
			close(appendStarted)
		})
		<-appendRelease
	}
	harness.store.mu.Unlock()
	harness.process.mu.Lock()
	harness.process.writeStarted = make(chan struct{})
	writeStarted := harness.process.writeStarted
	harness.process.mu.Unlock()

	feedDone := make(chan error, 1)
	go func() {
		feedDone <- harness.feedFixture("approval")
	}()
	<-appendStarted

	respondDone := make(chan error, 1)
	go func() {
		_, err := harness.manager.Respond(
			context.Background(),
			harness.id,
			harness.approveRequest(),
		)
		respondDone <- err
	}()
	select {
	case <-writeStarted:
		t.Fatal("action write overtook earlier admitted output")
	case <-time.After(25 * time.Millisecond):
	}
	close(appendRelease)

	if err := <-feedDone; err != nil {
		t.Fatalf("feed approval fixture: %v", err)
	}
	if err := <-respondDone; err != nil {
		t.Fatalf("Respond: %v", err)
	}
}

func TestDifferentAgentActionsDoNotShareControlGate(t *testing.T) {
	first := newActionTestHarness(t, "claude", "approval")
	second := addActionTestRuntime(
		t,
		first.manager,
		first.store,
		"second-agent",
		"codex",
		"approval",
	)
	firstRelease := make(chan struct{})
	first.process.mu.Lock()
	first.process.writeStarted = make(chan struct{})
	first.process.writeRelease = firstRelease
	firstStarted := first.process.writeStarted
	first.process.mu.Unlock()

	firstDone := make(chan error, 1)
	go func() {
		_, err := first.manager.Respond(
			context.Background(),
			first.id,
			first.approveRequest(),
		)
		firstDone <- err
	}()
	<-firstStarted

	secondDone := make(chan error, 1)
	go func() {
		_, err := second.manager.Respond(
			context.Background(),
			second.id,
			second.approveRequest(),
		)
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("second Respond: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second Agent waited for the first Agent control gate")
	}
	close(firstRelease)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Respond: %v", err)
	}
}

type actionTestHarness struct {
	manager *Manager
	store   *memoryCommitStore
	id      agent.ID
	running *runningSession
	process *terminalTestProcess
}

func newActionTestHarness(
	t *testing.T,
	vendor string,
	fixture string,
) *actionTestHarness {
	t.Helper()
	st := &memoryCommitStore{lastSeq: uint64(actionTestStateSeq)}
	hub := event.NewHub(uint64(actionTestStateSeq))
	committer := newCommitter(uint64(actionTestStateSeq), st, hub)
	manager := &Manager{
		reg:       adapter.NewRegistry(),
		hub:       hub,
		committer: committer,
		clock:     systemObservationClock{},
		agents:    make(map[agent.ID]*managedAgent),
		sessions:  make(map[agent.ID]*runningSession),
	}
	id := agent.ID("action-" + vendor)
	now := time.Now().UTC()
	target, err := agent.Restore(agent.RestoreSnapshot{
		ID:         id,
		Name:       string(id),
		Vendor:     vendor,
		RunMode:    agent.RunModeInteractive,
		HookPolicy: agent.HooksOff,
		State:      agent.StateBlocked,
		CreatedAt:  now.Add(-time.Minute),
		UpdatedAt:  now,
	})
	if err != nil {
		t.Fatalf("restore Agent: %v", err)
	}
	managed := newManagedAgent(target)
	managed.setStateSeq(actionTestStateSeq)
	classifier, err := manager.reg.For(vendor).NewScreenClassifier()
	if err != nil {
		t.Fatalf("NewScreenClassifier: %v", err)
	}
	process := &terminalTestProcess{}
	running := &runningSession{
		process:    process,
		classifier: classifier,
		vendor:     vendor,
	}
	size, err := term.NewSize(20, 100)
	if err != nil {
		t.Fatalf("NewSize: %v", err)
	}
	running.terminal, err = newTerminalActor(
		size,
		process,
		classifier,
		nil,
		&recordingTerminalObserver{},
		vendor,
		newTerminalTestClock(now),
		nil,
	)
	if err != nil {
		t.Fatalf("new terminal actor: %v", err)
	}
	running.output = newOutputProcessor(manager, id, running, "")
	manager.agents[id] = managed
	manager.sessions[id] = running

	t.Cleanup(func() {
		_ = running.output.Close()
		_ = running.terminal.Close()
		committer.Close()
	})

	harness := &actionTestHarness{
		manager: manager,
		store:   st,
		id:      id,
		running: running,
		process: process,
	}
	if fixture != "" {
		if err := harness.feedFixture(fixture); err != nil {
			t.Fatalf("feed %s fixture: %v", fixture, err)
		}
	}
	return harness
}

func (h *actionTestHarness) approveRequest() ActionRequest {
	return ActionRequest{
		ExpectedStateSeq: actionTestStateSeq,
		Kind:             agent.ActionApprove,
		Channel:          "web_push",
		DeviceID:         actionTestDeviceID,
	}
}

func (h *actionTestHarness) setWriteBehavior(limit int, err error) {
	h.process.mu.Lock()
	h.process.writeLimit = limit
	h.process.writeErr = err
	h.process.mu.Unlock()
}

func (h *actionTestHarness) feedFixture(name string) error {
	data, err := os.ReadFile(filepath.Join(
		"..",
		"adapter",
		"testdata",
		h.running.vendor,
		name+".bin",
	))
	if err != nil {
		return fmt.Errorf("read fixture: %w", err)
	}
	rows := h.store.Rows()
	offset := uint64(0)
	for _, row := range rows {
		if row.SessionID != string(h.id) ||
			row.Type != string(event.TypeOutputChunk) {
			continue
		}
		payload, decodeErr := event.DecodeOutputChunkPayload(row.Payload)
		if decodeErr != nil {
			return fmt.Errorf("decode output fixture row: %w", decodeErr)
		}
		offset = payload.Offset + uint64(payload.Len)
	}
	return h.running.output.Feed(data, offset)
}

func addActionTestRuntime(
	t *testing.T,
	manager *Manager,
	st *memoryCommitStore,
	id agent.ID,
	vendor string,
	fixture string,
) *actionTestHarness {
	t.Helper()
	now := time.Now().UTC()
	target, err := agent.Restore(agent.RestoreSnapshot{
		ID:         id,
		Name:       string(id),
		Vendor:     vendor,
		RunMode:    agent.RunModeInteractive,
		HookPolicy: agent.HooksOff,
		State:      agent.StateBlocked,
		CreatedAt:  now.Add(-time.Minute),
		UpdatedAt:  now,
	})
	if err != nil {
		t.Fatalf("restore Agent: %v", err)
	}
	managed := newManagedAgent(target)
	managed.setStateSeq(actionTestStateSeq)
	classifier, err := manager.reg.For(vendor).NewScreenClassifier()
	if err != nil {
		t.Fatalf("NewScreenClassifier: %v", err)
	}
	process := &terminalTestProcess{}
	running := &runningSession{
		process:    process,
		classifier: classifier,
		vendor:     vendor,
	}
	size, err := term.NewSize(20, 100)
	if err != nil {
		t.Fatalf("NewSize: %v", err)
	}
	running.terminal, err = newTerminalActor(
		size,
		process,
		classifier,
		nil,
		&recordingTerminalObserver{},
		vendor,
		newTerminalTestClock(now),
		nil,
	)
	if err != nil {
		t.Fatalf("new terminal actor: %v", err)
	}
	running.output = newOutputProcessor(manager, id, running, "")
	manager.mu.Lock()
	manager.agents[id] = managed
	manager.sessions[id] = running
	manager.mu.Unlock()
	harness := &actionTestHarness{
		manager: manager,
		store:   st,
		id:      id,
		running: running,
		process: process,
	}
	t.Cleanup(func() {
		_ = running.output.Close()
		_ = running.terminal.Close()
	})
	if err := harness.feedFixture(fixture); err != nil {
		t.Fatalf("feed %s fixture: %v", fixture, err)
	}
	return harness
}
