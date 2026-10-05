package clitui

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	xterm "github.com/charmbracelet/x/term"
	"github.com/creack/pty"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/cliattach"
	"github.com/Duang777/drove/internal/client"
	"github.com/Duang777/drove/internal/session"
)

func TestAcceptanceTenChangedSessionsRenderBeforeDeadlineAndQuitDoesNotStop(
	t *testing.T,
) {
	t.Parallel()

	initial := make([]*session.Status, 10)
	changed := make([]*session.Status, 10)
	for index := range 10 {
		initial[index] = newFleetStatus(
			"before-"+twoDigits(index),
			agent.StatePending,
			time.Now(),
		)
		changed[index] = newFleetStatus(
			"agent-"+twoDigits(index),
			agent.StateWorking,
			time.Now(),
		)
	}

	var listCalls atomic.Int32
	var stopCalls atomic.Int32
	daemon := &fakeTUIClient{
		listFn: func(context.Context) ([]*session.Status, error) {
			if listCalls.Add(1) == 1 {
				return initial, nil
			}
			return changed, nil
		},
		stopFn: func(context.Context, string) error {
			stopCalls.Add(1)
			return nil
		},
	}
	preview := newRunTestPreview(nil)
	input, sendKey := io.Pipe()
	defer input.Close()
	defer sendKey.Close()
	output := &lockedText{}
	m := newModel(context.Background(), daemon, preview, func(
		context.Context,
		string,
		cliattach.Options,
	) error {
		return nil
	})
	m.resize(130, 40)
	program := tea.NewProgram(
		m,
		tea.WithInput(input),
		tea.WithOutput(output),
		tea.WithoutSignals(),
	)

	started := time.Now()
	result := make(chan error, 1)
	go func() {
		_, err := program.Run()
		result <- err
	}()

	waitForText(t, output, "agent-09", time.Second)
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("ten-session refresh rendered after %s", elapsed)
	}
	if got := listCalls.Load(); got < 2 {
		t.Fatalf("list calls = %d, want an immediate and periodic refresh", got)
	}
	if _, err := sendKey.Write([]byte("q")); err != nil {
		t.Fatalf("send quit key: %v", err)
	}
	if err := receiveAcceptanceResult(t, result); err != nil {
		t.Fatalf("run program: %v", err)
	}
	if err := preview.Close(); err != nil {
		t.Fatalf("close preview: %v", err)
	}
	if got := stopCalls.Load(); got != 0 {
		t.Fatalf("quit called stop %d times", got)
	}
}

func TestAcceptanceAttachModesPreserveSelection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		key      string
		readOnly bool
	}{
		{name: "writable", key: "a", readOnly: false},
		{name: "read-only", key: "r", readOnly: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var gotOptions cliattach.Options
			m := newModelWithDependencies(context.Background(), modelDependencies{
				daemon:  &fakeTUIClient{},
				preview: newRecordingPreview(),
				attach: func(
					_ context.Context,
					agentID string,
					options cliattach.Options,
				) error {
					if agentID != "agent-2" {
						return errors.New("attached the wrong agent")
					}
					gotOptions = options
					return nil
				},
				exec: func(command tea.ExecCommand, callback tea.ExecCallback) tea.Cmd {
					return func() tea.Msg {
						return callback(command.Run())
					}
				},
				tick: func(time.Duration, func(time.Time) tea.Msg) tea.Cmd {
					return nil
				},
			})
			seedModel(m, "agent-1", "agent-2")
			_, _ = m.Update(keyMessage("down"))

			_, command := m.Update(keyMessage(test.key))
			if command == nil {
				t.Fatal("attach key returned no command")
			}
			result := command().(attachFinishedMsg)
			m.allRows = []fleetRow{m.allRows[1], m.allRows[0]}
			m.rebuildVisibleRows()
			_, _ = m.Update(result)

			if m.selection.AgentID != "agent-2" {
				t.Fatalf("selection = %q, want agent-2", m.selection.AgentID)
			}
			if gotOptions.ReadOnly != test.readOnly {
				t.Fatalf(
					"read-only option = %t, want %t",
					gotOptions.ReadOnly,
					test.readOnly,
				)
			}
		})
	}
}

func TestAcceptanceResizeAndSelectionReplacementJoinPreviewWorkers(t *testing.T) {
	t.Parallel()

	const workerCount = 4
	started := make([]chan struct{}, workerCount)
	canceled := make([]chan struct{}, workerCount)
	release := make([]chan struct{}, workerCount)
	streams := make([]*scriptedPreviewStream, workerCount)
	for index := range workerCount {
		started[index] = make(chan struct{})
		canceled[index] = make(chan struct{})
		release[index] = make(chan struct{})
		index := index
		streams[index] = &scriptedPreviewStream{
			nextFn: func(ctx context.Context, _ func(client.TerminalMessage) error) error {
				close(started[index])
				<-ctx.Done()
				close(canceled[index])
				<-release[index]
				return ctx.Err()
			},
		}
	}

	var openCalls atomic.Int32
	actor := newPreviewActor(context.Background(), func(
		context.Context,
	) (previewStream, error) {
		index := int(openCalls.Add(1)) - 1
		if index >= len(streams) {
			return nil, errors.New("opened too many preview workers")
		}
		return streams[index], nil
	})
	m := newTestModel(&fakeTUIClient{}, actor)
	seedModel(m, "agent-1", "agent-2", "agent-3", "agent-4")
	receivePreviewSignal(t, started[0])

	for index := range 50 {
		_, _ = m.Update(tea.WindowSizeMsg{
			Width:  80 + index,
			Height: 24 + index%5,
		})
	}
	if got := openCalls.Load(); got != 1 {
		t.Fatalf("resize opened %d preview workers, want 1", got)
	}

	for index := 0; index < workerCount-1; index++ {
		_, _ = m.Update(keyMessage("down"))
		receivePreviewSignal(t, canceled[index])
		select {
		case <-started[index+1]:
			t.Fatalf("worker %d started before worker %d joined", index+1, index)
		default:
		}
		close(release[index])
		receivePreviewSignal(t, started[index+1])
	}

	closeResult := make(chan error, 1)
	go func() {
		closeResult <- actor.Close()
	}()
	receivePreviewSignal(t, canceled[workerCount-1])
	select {
	case err := <-closeResult:
		t.Fatalf("preview closed before active worker joined: %v", err)
	default:
	}
	close(release[workerCount-1])
	if err := receiveAcceptanceResult(t, closeResult); err != nil {
		t.Fatalf("close preview actor: %v", err)
	}
	for index, stream := range streams {
		if got := stream.closeCalls.Load(); got != 1 {
			t.Fatalf("stream %d close calls = %d, want 1", index, got)
		}
	}
}

func TestAcceptanceRepeatedExecRestoresPseudoTerminal(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("open pseudo-terminal: %v", err)
	}
	defer master.Close()
	defer slave.Close()
	if err := pty.Setsize(slave, &pty.Winsize{Rows: 30, Cols: 120}); err != nil {
		t.Fatalf("set pseudo-terminal size: %v", err)
	}
	initialState, err := xterm.GetState(slave.Fd())
	if err != nil {
		t.Fatalf("read initial terminal state: %v", err)
	}
	go func() {
		_, _ = io.Copy(io.Discard, master)
	}()

	attachResults := make(chan error, 2)
	preview := newRunTestPreview(nil)
	status := newFleetStatus("agent-1", agent.StateWorking, time.Now())
	m := newModelWithDependencies(context.Background(), modelDependencies{
		daemon: &fakeTUIClient{
			listFn: func(context.Context) ([]*session.Status, error) {
				return []*session.Status{status}, nil
			},
		},
		preview: preview,
		attach: func(
			context.Context,
			string,
			cliattach.Options,
		) error {
			current, err := xterm.GetState(slave.Fd())
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(current, initialState) {
				return errors.New("Bubble Tea did not release the terminal")
			}
			previous, err := xterm.MakeRaw(slave.Fd())
			if err != nil {
				return err
			}
			err = xterm.Restore(slave.Fd(), previous)
			attachResults <- err
			return err
		},
		exec: tea.Exec,
	})
	seedModel(m, "agent-1")
	program := tea.NewProgram(
		m,
		tea.WithInput(slave),
		tea.WithOutput(slave),
		tea.WithAltScreen(),
		tea.WithoutSignals(),
	)
	result := make(chan error, 1)
	go func() {
		_, runErr := program.Run()
		result <- runErr
	}()

	waitForTerminalState(t, slave, initialState, false)
	for range 2 {
		if _, err := master.Write([]byte("a")); err != nil {
			t.Fatalf("send attach key: %v", err)
		}
		if err := receiveAcceptanceResult(t, attachResults); err != nil {
			t.Fatalf("attach handoff: %v", err)
		}
		waitForTerminalState(t, slave, initialState, false)
	}
	if _, err := master.Write([]byte("q")); err != nil {
		t.Fatalf("send quit key: %v", err)
	}
	if err := receiveAcceptanceResult(t, result); err != nil {
		t.Fatalf("run pseudo-terminal program: %v", err)
	}
	if err := preview.Close(); err != nil {
		t.Fatalf("close preview: %v", err)
	}
	restoredState, err := xterm.GetState(slave.Fd())
	if err != nil {
		t.Fatalf("read restored terminal state: %v", err)
	}
	if !reflect.DeepEqual(restoredState, initialState) {
		t.Fatal("Bubble Tea did not restore the terminal after repeated attach")
	}
}

type lockedText struct {
	mu      sync.RWMutex
	builder strings.Builder
}

func (b *lockedText) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.builder.Write(data)
}

func (b *lockedText) Contains(text string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return strings.Contains(b.builder.String(), text)
}

func waitForText(
	t *testing.T,
	output *lockedText,
	text string,
	timeout time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if output.Contains(text) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("output did not contain %q before %s", text, timeout)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForTerminalState(
	t *testing.T,
	terminal interface{ Fd() uintptr },
	initial *xterm.State,
	wantInitial bool,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		current, err := xterm.GetState(terminal.Fd())
		if err != nil {
			t.Fatalf("read terminal state: %v", err)
		}
		if reflect.DeepEqual(current, initial) == wantInitial {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal initial state match = %t, want %t", !wantInitial, wantInitial)
		}
		time.Sleep(time.Millisecond)
	}
}

func receiveAcceptanceResult[T any](t *testing.T, result <-chan T) T {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for acceptance result")
		var zero T
		return zero
	}
}

func twoDigits(value int) string {
	return string(rune('0'+value/10)) + string(rune('0'+value%10))
}
