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
)

func TestAttachmentLatestOwnershipLifecycle(t *testing.T) {
	manager, id, running, process := newAttachmentTestRuntime(t)

	first, err := manager.AttachTerminal(context.Background(), id, AttachmentOptions{
		Mode:    AttachmentWritable,
		Rows:    24,
		Columns: 80,
	})
	if err != nil {
		t.Fatalf("attach first writer: %v", err)
	}
	defer first.Close()
	second, err := manager.AttachTerminal(context.Background(), id, AttachmentOptions{
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
		}
	}
	if want := []event.Type{
		event.TypeAgentResized,
		event.TypeAgentResized,
		event.TypeAgentResized,
		event.TypeAgentInput,
		event.TypeAgentResized,
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
		Mode:    AttachmentWritable,
		Rows:    24,
		Columns: 80,
	})
	if err != nil {
		t.Fatalf("attach first writer: %v", err)
	}
	defer first.Close()
	second, err := manager.AttachTerminal(context.Background(), id, AttachmentOptions{
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

func TestReadOnlyAttachmentCannotWriteOrOwnSize(t *testing.T) {
	manager, id, running, process := newAttachmentTestRuntime(t)
	readOnly, err := manager.AttachTerminal(context.Background(), id, AttachmentOptions{
		Mode: AttachmentReadOnly,
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
		AttachmentOptions{Mode: AttachmentWritable},
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
	if len(rows) != 0 {
		t.Fatalf("duplicate effective size persisted events: %+v", rows)
	}
	if err := running.output.End(0); err != nil {
		t.Fatalf("end output: %v", err)
	}
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
