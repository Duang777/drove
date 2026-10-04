package session

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/pty"
)

func TestAttachmentLatestOwnershipLifecycle(t *testing.T) {
	manager, id, running, process := newAttachmentTestRuntime(t)

	first, err := manager.AttachTerminal(context.Background(), id, AttachmentOptions{
		Purpose: AttachmentPurposeUser,
		Mode:    AttachmentWritable,
		Rows:    24,
		Columns: 80,
	})
	if err != nil {
		t.Fatalf("attach first writer: %v", err)
	}
	defer first.Close()
	second, err := manager.AttachTerminal(context.Background(), id, AttachmentOptions{
		Purpose: AttachmentPurposeUser,
		Mode:    AttachmentWritable,
		Rows:    30,
		Columns: 100,
	})
	if err != nil {
		t.Fatalf("attach second writer: %v", err)
	}
	defer second.Close()
	if first.ID() == "" || second.ID() == "" || first.ID() == second.ID() {
		t.Fatalf("attachment IDs first=%q second=%q", first.ID(), second.ID())
	}

	if err := second.Resize(context.Background(), 35, 110); err != nil {
		t.Fatalf("propose non-owner resize: %v", err)
	}
	if err := first.Resize(context.Background(), 25, 90); err != nil {
		t.Fatalf("resize owner: %v", err)
	}
	result, err := second.SendInput(context.Background(), []byte("promote\n"))
	if err != nil {
		t.Fatalf("promoting input: %v", err)
	}
	if result.BytesWritten != len("promote\n") {
		t.Fatalf("bytes written = %d", result.BytesWritten)
	}
	if err := first.Resize(context.Background(), 26, 91); err != nil {
		t.Fatalf("update fallback proposal: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("detach owner: %v", err)
	}

	wantResizes := [][2]uint16{
		{24, 80},
		{25, 90},
		{35, 110},
		{26, 91},
	}
	if got := process.Resizes(); !slices.Equal(got, wantResizes) {
		t.Fatalf("PTY resizes = %v, want %v", got, wantResizes)
	}
	if got := process.Writes(); len(got) != 1 || string(got[0]) != "promote\n" {
		t.Fatalf("PTY writes = %q", got)
	}

	rows, err := manager.Replay(string(id))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	var resizePayloads []event.AgentResizedPayloadV1
	var recordingTypes []event.Type
	for _, row := range rows {
		if strings.Contains(row.Payload, string(first.ID())) ||
			strings.Contains(row.Payload, string(second.ID())) {
			t.Fatalf("event persisted attachment ID: %+v", row)
		}
		switch event.Type(row.Type) {
		case event.TypeAgentResized:
			payload, decodeErr := event.DecodeAgentResizedPayload(row.Payload)
			if decodeErr != nil {
				t.Fatalf("decode resize: %v", decodeErr)
			}
			resizePayloads = append(resizePayloads, payload)
			recordingTypes = append(recordingTypes, event.TypeAgentResized)
		case event.TypeAgentInput:
			recordingTypes = append(recordingTypes, event.TypeAgentInput)
		case event.TypeAgentAttachment:
			recordingTypes = append(recordingTypes, event.TypeAgentAttachment)
		}
	}
	if want := []event.Type{
		event.TypeAgentResized,
		event.TypeAgentAttachment,
		event.TypeAgentAttachment,
		event.TypeAgentResized,
		event.TypeAgentResized,
		event.TypeAgentInput,
		event.TypeAgentResized,
		event.TypeAgentAttachment,
	}; !slices.Equal(recordingTypes, want) {
		t.Fatalf("recording types = %v, want %v", recordingTypes, want)
	}
	for index, want := range wantResizes {
		payload := resizePayloads[index]
		if payload.Rows != want[0] || payload.Columns != want[1] {
			t.Fatalf("resize %d payload = %+v, want %v", index, payload, want)
		}
		if payload.OutputOffset != 0 {
			t.Fatalf("resize %d offset = %d, want 0", index, payload.OutputOffset)
		}
	}

	if err := running.output.End(0); err != nil {
		t.Fatalf("end output: %v", err)
	}
}

func TestAttachmentFailedInputDoesNotPromoteOwner(t *testing.T) {
	manager, id, running, process := newAttachmentTestRuntime(t)
	process.writeErr = errors.New("input rejected")

	first, err := manager.AttachTerminal(context.Background(), id, AttachmentOptions{
		Purpose: AttachmentPurposeUser,
		Mode:    AttachmentWritable,
		Rows:    24,
		Columns: 80,
	})
	if err != nil {
		t.Fatalf("attach first writer: %v", err)
	}
	defer first.Close()
	second, err := manager.AttachTerminal(context.Background(), id, AttachmentOptions{
		Purpose: AttachmentPurposeUser,
		Mode:    AttachmentWritable,
		Rows:    30,
		Columns: 100,
	})
	if err != nil {
		t.Fatalf("attach second writer: %v", err)
	}
	defer second.Close()

	if _, err := second.SendInput(
		context.Background(),
		[]byte("rejected"),
	); !errors.Is(err, ErrInputWrite) {
		t.Fatalf("failed input error = %v, want ErrInputWrite", err)
	}
	if err := first.Resize(context.Background(), 25, 90); err != nil {
		t.Fatalf("resize original owner: %v", err)
	}
	want := [][2]uint16{{24, 80}, {30, 100}, {25, 90}}
	if got := process.Resizes(); !slices.Equal(got, want) {
		t.Fatalf("PTY resizes = %v, want %v", got, want)
	}
	if err := running.output.End(0); err != nil {
		t.Fatalf("end output: %v", err)
	}
}

func TestAttachmentInputRejectsConcurrentAdmission(t *testing.T) {
	manager, id, running, _ := newAttachmentTestRuntime(t)
	attachment, err := manager.AttachTerminal(
		context.Background(),
		id,
		AttachmentOptions{
			Purpose: AttachmentPurposeUser,
			Mode:    AttachmentWritable,
			Rows:    24,
			Columns: 80,
		},
	)
	if err != nil {
		t.Fatalf("attach writer: %v", err)
	}
	defer attachment.Close()

	running.inputMu.Lock()
	result, err := attachment.SendInput(context.Background(), []byte("input"))
	running.inputMu.Unlock()
	if !errors.Is(err, ErrInputBackpressure) {
		t.Fatalf("send input error = %v, want ErrInputBackpressure", err)
	}
	if result.BytesWritten != 0 {
		t.Fatalf("bytes written = %d, want 0", result.BytesWritten)
	}
	if err := running.output.End(0); err != nil {
		t.Fatalf("end output: %v", err)
	}
}

func TestReadOnlyAttachmentCannotWriteOrOwnSize(t *testing.T) {
	manager, id, running, process := newAttachmentTestRuntime(t)
	readOnly, err := manager.AttachTerminal(context.Background(), id, AttachmentOptions{
		Purpose: AttachmentPurposeUser,
		Mode:    AttachmentReadOnly,
	})
	if err != nil {
		t.Fatalf("attach read-only: %v", err)
	}
	defer readOnly.Close()

	if _, err := readOnly.SendInput(
		context.Background(),
		[]byte("input"),
	); !errors.Is(err, ErrAttachmentReadOnly) {
		t.Fatalf("read-only input error = %v", err)
	}
	if err := readOnly.Resize(
		context.Background(),
		24,
		80,
	); !errors.Is(err, ErrAttachmentReadOnly) {
		t.Fatalf("read-only resize error = %v", err)
	}
	if got := process.Resizes(); len(got) != 0 {
		t.Fatalf("read-only attachment resized PTY: %v", got)
	}
	if err := running.output.End(0); err != nil {
		t.Fatalf("end output: %v", err)
	}
}

func TestAttachmentDuplicateEffectiveSizeWritesNoEvent(t *testing.T) {
	manager, id, running, process := newAttachmentTestRuntime(t)
	attachment, err := manager.AttachTerminal(
		context.Background(),
		id,
		AttachmentOptions{
			Purpose: AttachmentPurposeUser,
			Mode:    AttachmentWritable,
		},
	)
	if err != nil {
		t.Fatalf("attach writer: %v", err)
	}
	defer attachment.Close()
	if err := attachment.Resize(
		context.Background(),
		initialTerminalRows,
		initialTerminalColumns,
	); err != nil {
		t.Fatalf("repeat initial size: %v", err)
	}
	if got := process.Resizes(); len(got) != 0 {
		t.Fatalf("duplicate effective resizes = %v", got)
	}
	rows, err := manager.Replay(string(id))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	for _, row := range rows {
		if row.Type == string(event.TypeAgentResized) {
			t.Fatalf("duplicate effective size persisted resize: %+v", row)
		}
	}
	if err := running.output.End(0); err != nil {
		t.Fatalf("end output: %v", err)
	}
}

func TestAttachmentRequiresExplicitCompatiblePurpose(t *testing.T) {
	manager, id, _, _ := newAttachmentTestRuntime(t)
	tests := []AttachmentOptions{
		{Mode: AttachmentReadOnly},
		{
			Purpose: AttachmentPurposeRecording,
			Mode:    AttachmentWritable,
		},
	}
	for _, options := range tests {
		if _, err := manager.AttachTerminal(
			context.Background(),
			id,
			options,
		); err == nil {
			t.Fatalf("attachment options %+v were accepted", options)
		}
	}
}

func TestUserAttachmentCloseAuditsExactlyOnce(t *testing.T) {
	manager, id, _, _ := newAttachmentTestRuntime(t)
	attachment, err := manager.AttachTerminal(
		context.Background(),
		id,
		AttachmentOptions{
			Purpose: AttachmentPurposeUser,
			Mode:    AttachmentReadOnly,
		},
	)
	if err != nil {
		t.Fatalf("attach reader: %v", err)
	}
	if err := attachment.Close(); err != nil {
		t.Fatalf("close attachment: %v", err)
	}
	if err := attachment.Close(); err != nil {
		t.Fatalf("close attachment again: %v", err)
	}

	audits := attachmentAudits(t, manager, id)
	want := []event.AttachmentAuditPayloadV1{
		{
			Version: event.AttachmentAuditPayloadVersion,
			Action:  event.AttachmentAttached,
			Access:  event.AttachmentReadOnly,
		},
		{
			Version: event.AttachmentAuditPayloadVersion,
			Action:  event.AttachmentDetached,
			Access:  event.AttachmentReadOnly,
		},
	}
	if !slices.Equal(audits, want) {
		t.Fatalf("attachment audits = %+v, want %+v", audits, want)
	}
}

func TestForcedAttachmentRemovalAuditsUsersOnly(t *testing.T) {
	tests := []struct {
		name   string
		remove func(*Manager, agent.ID, *runningSession) error
	}{
		{
			name: "detach all",
			remove: func(_ *Manager, _ agent.ID, running *runningSession) error {
				return running.output.DetachAll()
			},
		},
		{
			name: "output close",
			remove: func(_ *Manager, _ agent.ID, running *runningSession) error {
				return running.output.Close()
			},
		},
		{
			name: "manager shutdown",
			remove: func(manager *Manager, _ agent.ID, _ *runningSession) error {
				return manager.Close()
			},
		},
		{
			name: "process exit",
			remove: func(manager *Manager, id agent.ID, running *runningSession) error {
				manager.onExit(id, running, pty.ExitInfo{Code: 0})
				return running.output.Close()
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, id, running, _ := newAttachmentTestRuntime(t)
			user, err := manager.AttachTerminal(
				context.Background(),
				id,
				AttachmentOptions{
					Purpose: AttachmentPurposeUser,
					Mode:    AttachmentWritable,
				},
			)
			if err != nil {
				t.Fatalf("attach user: %v", err)
			}
			recording, err := manager.AttachTerminal(
				context.Background(),
				id,
				AttachmentOptions{
					Purpose: AttachmentPurposeRecording,
					Mode:    AttachmentReadOnly,
				},
			)
			if err != nil {
				t.Fatalf("attach recording reader: %v", err)
			}
			if err := test.remove(manager, id, running); err != nil {
				t.Fatalf("remove attachments: %v", err)
			}
			if err := user.Close(); err != nil {
				t.Fatalf("close removed user attachment: %v", err)
			}
			if err := recording.Close(); err != nil {
				t.Fatalf("close removed recording attachment: %v", err)
			}

			audits := attachmentAudits(t, manager, id)
			if len(audits) != 2 ||
				audits[0].Action != event.AttachmentAttached ||
				audits[0].Access != event.AttachmentReadWrite ||
				audits[1].Action != event.AttachmentDetached ||
				audits[1].Access != event.AttachmentReadWrite {
				t.Fatalf("attachment audits = %+v", audits)
			}
		})
	}
}

func TestFailedUserAttachmentSetupCleansLocalState(t *testing.T) {
	manager, id, running, _ := newAttachmentTestRuntime(t)
	manager.committer.Close()
	appendErr := errors.New("attachment audit unavailable")
	failingStore := &memoryCommitStore{appendErr: appendErr}
	manager.committer = newCommitter(0, failingStore, manager.hub)

	if _, err := manager.AttachTerminal(
		context.Background(),
		id,
		AttachmentOptions{
			Purpose: AttachmentPurposeUser,
			Mode:    AttachmentReadOnly,
		},
	); !errors.Is(err, appendErr) {
		t.Fatalf("attach error = %v, want append failure", err)
	}
	if err := running.output.DetachAll(); err != nil {
		t.Fatalf("failed setup left an attachment to detach: %v", err)
	}
	if rows := failingStore.Rows(); len(rows) != 0 {
		t.Fatalf("failed setup persisted rows: %+v", rows)
	}
	select {
	case fatalErr := <-manager.Fatal():
		if !errors.Is(fatalErr, appendErr) {
			t.Fatalf("fatal error = %v, want append failure", fatalErr)
		}
	case <-time.After(time.Second):
		t.Fatal("failed attachment audit did not fail the committer")
	}
}

func attachmentAudits(
	t *testing.T,
	manager *Manager,
	id agent.ID,
) []event.AttachmentAuditPayloadV1 {
	t.Helper()
	rows, err := manager.Replay(string(id))
	if err != nil {
		t.Fatalf("replay attachment audits: %v", err)
	}
	var audits []event.AttachmentAuditPayloadV1
	for _, row := range rows {
		if row.Type != string(event.TypeAgentAttachment) {
			continue
		}
		payload, err := event.DecodeAttachmentAuditPayload(row.Payload)
		if err != nil {
			t.Fatalf("decode attachment audit: %v", err)
		}
		audits = append(audits, payload)
	}
	return audits
}

func newAttachmentTestRuntime(
	t *testing.T,
) (*Manager, agent.ID, *runningSession, *terminalTestProcess) {
	t.Helper()
	manager, _ := newTestManager(t)
	id := agent.ID("attachment-agent")
	target := agent.New(
		id,
		agent.WithName("attachment-agent"),
		agent.WithVendor("generic"),
		agent.WithRunMode(agent.RunModeInteractive),
	)
	process := &terminalTestProcess{}
	running := &runningSession{
		process: process,
		vendor:  "generic",
	}
	running.output = newOutputProcessor(manager, id, running, "")
	running.terminal = newTerminalTestActor(
		t,
		"generic",
		newTerminalTestClock(time.Unix(80, 0).UTC()),
		process,
		&recordingTerminalObserver{},
	)
	manager.mu.Lock()
	manager.agents[id] = newManagedAgent(target)
	manager.sessions[id] = running
	manager.mu.Unlock()
	return manager, id, running, process
}
