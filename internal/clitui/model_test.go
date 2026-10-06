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

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/cliattach"
	"github.com/Duang777/drove/internal/client"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/session"
)

func TestModelSelectionFilterAndPreviewGeneration(t *testing.T) {
	t.Parallel()

	preview := newRecordingPreview()
	m := newTestModel(&fakeTUIClient{}, preview)
	m.fleetInFlight = true
	m.fleetRequestID = 1
	_, _ = m.Update(fleetResultMsg{
		RequestID: 1,
		Statuses: []*session.Status{
			newFleetStatus("pending", agent.StatePending, time.Now()),
			newFleetStatus("blocked", agent.StateBlocked, time.Now()),
			newFleetStatus("working", agent.StateWorking, time.Now()),
		},
	})

	if got := rowIDs(m.rows); !reflect.DeepEqual(got, []string{"blocked", "pending", "working"}) {
		t.Fatalf("visible rows = %v", got)
	}
	if m.selection.AgentID != "blocked" {
		t.Fatalf("selected agent = %q, want blocked", m.selection.AgentID)
	}
	firstTarget := preview.lastReplacement(t)
	if firstTarget != (previewTarget{AgentID: "blocked", Generation: 1}) {
		t.Fatalf("first preview target = %+v", firstTarget)
	}

	accepted := &client.TerminalSnapshot{AgentID: "blocked", Lines: []string{"current"}}
	_, _ = m.Update(previewResultMsg{
		OK: true,
		Event: previewEvent{
			Target:   firstTarget,
			Snapshot: accepted,
		},
	})
	_, _ = m.Update(previewResultMsg{
		OK: true,
		Event: previewEvent{
			Target: previewTarget{AgentID: "blocked", Generation: 0},
			Snapshot: &client.TerminalSnapshot{
				AgentID: "blocked",
				Lines:   []string{"stale"},
			},
		},
	})
	if got := m.previewSnapshot.Lines; !reflect.DeepEqual(got, []string{"current"}) {
		t.Fatalf("snapshot lines = %q, want current snapshot", got)
	}

	_, _ = m.Update(keyMessage("down"))
	if m.selection.AgentID != "pending" {
		t.Fatalf("selected agent after down = %q, want pending", m.selection.AgentID)
	}
	secondTarget := preview.lastReplacement(t)
	if secondTarget != (previewTarget{AgentID: "pending", Generation: 2}) {
		t.Fatalf("second preview target = %+v", secondTarget)
	}

	_, _ = m.Update(keyMessage("f"))
	_, _ = m.Update(keyMessage("right"))
	if m.focus != focusFilter || m.filter != filterPending {
		t.Fatalf("filter focus = %d filter = %q", m.focus, m.filter)
	}
	if got := rowIDs(m.rows); !reflect.DeepEqual(got, []string{"pending"}) {
		t.Fatalf("pending rows = %v", got)
	}
	if got := preview.replacementCount(); got != 2 {
		t.Fatalf("preview replacements = %d, want selection retained", got)
	}
	_, _ = m.Update(keyMessage("enter"))
	if m.focus != focusFleet {
		t.Fatalf("focus after enter = %d, want fleet", m.focus)
	}
}

func TestPollRunsImmediatelyWithoutOverlapAndKeepsLastGoodFleet(t *testing.T) {
	t.Parallel()

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var (
		calls   atomic.Int32
		active  atomic.Int32
		maximum atomic.Int32
	)
	daemon := &fakeTUIClient{
		listFn: func(context.Context) ([]*session.Status, error) {
			call := calls.Add(1)
			nowActive := active.Add(1)
			for {
				old := maximum.Load()
				if nowActive <= old || maximum.CompareAndSwap(old, nowActive) {
					break
				}
			}
			defer active.Add(-1)
			if call == 1 {
				close(firstStarted)
				<-releaseFirst
				return []*session.Status{
					newFleetStatus("agent-1", agent.StateWorking, time.Now()),
				}, nil
			}
			if call == 2 {
				return []*session.Status{
					newFleetStatus("agent-2", agent.StateBlocked, time.Now()),
				}, nil
			}
			return nil, errors.New("list failed")
		},
	}
	var tickDelay time.Duration
	m := newModelWithDependencies(context.Background(), modelDependencies{
		daemon: daemon,
		tick: func(delay time.Duration, _ func(time.Time) tea.Msg) tea.Cmd {
			tickDelay = delay
			return nil
		},
	})
	initial := m.Init()
	if initial == nil || !m.fleetInFlight {
		t.Fatal("Init did not start an immediate fleet request")
	}
	if tickDelay != fleetRefreshInterval {
		t.Fatalf("poll delay = %s, want %s", tickDelay, fleetRefreshInterval)
	}

	results := make(chan fleetResultMsg, 1)
	go func() {
		results <- initial().(fleetResultMsg)
	}()
	receivePreviewSignal(t, firstStarted)

	_, _ = m.Update(fleetTickMsg{})
	_, _ = m.Update(fleetTickMsg{})
	if !m.trailingFleet {
		t.Fatal("polls during an active request did not retain a trailing refresh")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("list calls during active request = %d, want 1", got)
	}

	close(releaseFirst)
	firstResult := receivePreviewValue(t, results)
	_, trailing := m.Update(firstResult)
	if trailing == nil || !m.fleetInFlight || m.trailingFleet {
		t.Fatal("completed request did not start exactly one trailing refresh")
	}
	secondResult := trailing().(fleetResultMsg)
	_, _ = m.Update(secondResult)
	if got := rowIDs(m.rows); !reflect.DeepEqual(got, []string{"agent-2"}) {
		t.Fatalf("rows after trailing refresh = %v", got)
	}
	if maximum.Load() != 1 || calls.Load() != 2 {
		t.Fatalf("list calls=%d maximum overlap=%d, want 2 and 1", calls.Load(), maximum.Load())
	}

	failing := m.startFleetRefresh()
	_, _ = m.Update(failing().(fleetResultMsg))
	if got := rowIDs(m.rows); !reflect.DeepEqual(got, []string{"agent-2"}) {
		t.Fatalf("failed refresh replaced last good rows: %v", got)
	}
	if m.fleetErr == nil {
		t.Fatal("failed refresh did not retain its error")
	}
}

func TestPollReplacesPreviewWhenSelectedAgentProcessChanges(t *testing.T) {
	t.Parallel()

	preview := newRecordingPreview()
	m := newTestModel(&fakeTUIClient{}, preview)
	first := newFleetStatus("agent-1", agent.StateWorking, time.Now())
	first.PID = 101
	m.fleetInFlight = true
	m.fleetRequestID = 1
	_, _ = m.Update(fleetResultMsg{
		RequestID: 1,
		Statuses:  []*session.Status{first},
	})
	m.previewSnapshot = &clientSnapshot{Lines: []string{"old process"}}
	m.beginAction(actionStop, "agent-1")
	m.focus = focusStop

	resumed := newFleetStatus("agent-1", agent.StateWorking, time.Now())
	resumed.PID = 202
	m.fleetInFlight = true
	m.fleetRequestID = 2
	_, _ = m.Update(fleetResultMsg{
		RequestID: 2,
		Statuses:  []*session.Status{resumed},
	})

	if m.previewSnapshot != nil {
		t.Fatal("process replacement retained the previous snapshot")
	}
	if m.focus != focusFleet || m.actionID != "" {
		t.Fatalf(
			"process replacement retained action focus=%d agent=%q",
			m.focus,
			m.actionID,
		)
	}
	if got := preview.lastReplacement(t); got != (previewTarget{
		AgentID:    "agent-1",
		Generation: 2,
	}) {
		t.Fatalf("replacement target = %+v", got)
	}
}

func TestActionSendAddsExactlyOneNewlineAndEnforcesByteLimit(t *testing.T) {
	t.Parallel()

	var (
		mu       sync.Mutex
		payloads [][]byte
	)
	daemon := &fakeTUIClient{
		sendFn: func(_ context.Context, _ string, payload []byte) error {
			mu.Lock()
			payloads = append(payloads, append([]byte(nil), payload...))
			mu.Unlock()
			return nil
		},
	}
	m := newTestModel(daemon, newRecordingPreview())
	seedModel(m, "agent-1")

	_, _ = m.Update(keyMessage("s"))
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	_, command := m.Update(keyMessage("enter"))
	result := command().(actionResultMsg)
	_, _ = m.Update(result)

	_, _ = m.Update(keyMessage("s"))
	_, command = m.Update(keyMessage("enter"))
	_, _ = m.Update(command().(actionResultMsg))

	mu.Lock()
	gotPayloads := append([][]byte(nil), payloads...)
	mu.Unlock()
	wantPayloads := [][]byte{[]byte("hello\n"), []byte("\n")}
	if !reflect.DeepEqual(gotPayloads, wantPayloads) {
		t.Fatalf("sent payloads = %q, want %q", gotPayloads, wantPayloads)
	}

	_, _ = m.Update(keyMessage("s"))
	m.input.SetValue(strings.Repeat("x", session.MaxInputBytes))
	_, command = m.Update(keyMessage("enter"))
	if command != nil {
		t.Fatal("oversized input returned a send command")
	}
	if !strings.Contains(m.notice, "maximum") || m.focus != focusSend {
		t.Fatalf("oversized input notice=%q focus=%d", m.notice, m.focus)
	}
	if view := m.View(); !strings.Contains(view, "maximum") {
		t.Fatalf("oversized input notice is not visible:\n%s", view)
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.notice != "" {
		t.Fatalf("editing oversized input retained notice %q", m.notice)
	}
	if view := m.View(); !strings.Contains(view, "SEND agent-1") {
		t.Fatalf("send editor did not return after editing:\n%s", view)
	}
}

func TestPendingActionBlocksOtherActionsAndCancellationStopsRequest(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	daemon := &fakeTUIClient{
		sendFn: func(ctx context.Context, _ string, _ []byte) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	var attachCalls atomic.Int32
	m := newModelWithDependencies(context.Background(), modelDependencies{
		daemon:  daemon,
		preview: newRecordingPreview(),
		attach: func(context.Context, string, cliattach.Options) error {
			attachCalls.Add(1)
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
	seedModel(m, "agent-1")
	_, _ = m.Update(keyMessage("s"))
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	_, sendCommand := m.Update(keyMessage("enter"))
	sendResult := make(chan actionResultMsg, 1)
	go func() {
		sendResult <- sendCommand().(actionResultMsg)
	}()
	receivePreviewSignal(t, started)

	for _, key := range []string{"s", "x", "e", "a", "r"} {
		_, command := m.Update(keyMessage(key))
		if command != nil {
			t.Fatalf("busy action accepted %q", key)
		}
	}
	if got := attachCalls.Load(); got != 0 {
		t.Fatalf("busy action started attach %d times", got)
	}

	m.invalidateAction()
	result := receivePreviewValue(t, sendResult)
	_, refresh := m.Update(result)
	if refresh == nil {
		t.Fatal("stale send completion did not refresh the fleet")
	}
	if m.actionBusy {
		t.Fatal("canceled action remained busy")
	}
}

func TestActionStopRequiresConfirmationAndRefreshes(t *testing.T) {
	t.Parallel()

	stopped := make(chan string, 1)
	daemon := &fakeTUIClient{
		stopFn: func(_ context.Context, id string) error {
			stopped <- id
			return nil
		},
	}
	m := newTestModel(daemon, newRecordingPreview())
	seedModel(m, "agent-1")

	_, _ = m.Update(keyMessage("x"))
	_, command := m.Update(keyMessage("n"))
	if command != nil || m.focus != focusFleet {
		t.Fatal("canceling stop started work or kept confirmation focused")
	}
	select {
	case id := <-stopped:
		t.Fatalf("canceling stop called daemon for %q", id)
	default:
	}

	_, _ = m.Update(keyMessage("x"))
	_, command = m.Update(keyMessage("y"))
	result := command().(actionResultMsg)
	if got := receivePreviewValue(t, stopped); got != "agent-1" {
		t.Fatalf("stopped agent = %q, want agent-1", got)
	}
	_, refresh := m.Update(result)
	if refresh == nil || !m.fleetInFlight {
		t.Fatal("successful stop did not request a fleet refresh")
	}
	if m.notice != "stop requested" {
		t.Fatalf("stop notice = %q", m.notice)
	}
}

func TestActionExplainUsesTypedFieldsAndRejectsStaleResult(t *testing.T) {
	t.Parallel()

	explanation := &session.Explanation{
		AgentID:    "agent-1",
		State:      agent.StateBlocked,
		HookStatus: detect.HookActive,
		Attached:   true,
		Events: []session.ExplainEvent{
			{
				Seq:       9,
				Timestamp: time.Date(2026, time.October, 5, 14, 0, 0, 0, time.UTC),
				Type:      event.TypeStateChanged,
				Source:    agent.EvidenceHook,
				From:      agent.StateWorking,
				To:        agent.StateBlocked,
				Reason:    "approval",
			},
		},
	}
	daemon := &fakeTUIClient{
		explainFn: func(context.Context, string, session.ExplainOptions) (*session.Explanation, error) {
			return explanation, nil
		},
	}
	m := newTestModel(daemon, newRecordingPreview())
	seedModel(m, "agent-1", "agent-2")

	_, command := m.Update(keyMessage("e"))
	result := command().(actionResultMsg)
	_, _ = m.Update(result)
	rendered := renderExplanation(m.explanation, m.explainErr)
	for _, want := range []string{
		"state=blocked",
		"hook=hook_active",
		"attached=true",
		"source=\"hook\"",
		"transition=working->blocked",
		"reason=\"approval\"",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("explanation omitted %q:\n%s", want, rendered)
		}
	}

	_, _ = m.Update(keyMessage("esc"))
	_, command = m.Update(keyMessage("e"))
	stale := command().(actionResultMsg)
	m.allRows = []fleetRow{{AgentID: "agent-2", State: agent.StateWorking}}
	m.rebuildVisibleRows()
	_, _ = m.Update(stale)
	if m.focus != focusFleet || m.explanation != nil {
		t.Fatalf("stale explain updated model: focus=%d explanation=%+v", m.focus, m.explanation)
	}
}

func TestAttachUsesExecModesAndPreservesSelection(t *testing.T) {
	t.Parallel()

	var (
		options []cliattach.Options
		stdin   io.Reader
		stdout  io.Writer
		stderr  io.Writer
	)
	attachErr := errors.New("attach failed")
	preview := newRecordingPreview()
	m := newModelWithDependencies(context.Background(), modelDependencies{
		daemon:  &fakeTUIClient{},
		preview: preview,
		attach: func(_ context.Context, id string, option cliattach.Options) error {
			if id != "agent-1" {
				t.Fatalf("attached agent = %q, want agent-1", id)
			}
			options = append(options, option)
			if option.ReadOnly {
				return attachErr
			}
			return nil
		},
		exec: func(command tea.ExecCommand, callback tea.ExecCallback) tea.Cmd {
			command.SetStdin(stdin)
			command.SetStdout(stdout)
			command.SetStderr(stderr)
			return func() tea.Msg {
				return callback(command.Run())
			}
		},
		tick: func(time.Duration, func(time.Time) tea.Msg) tea.Cmd {
			return nil
		},
	})
	seedModel(m, "agent-1", "agent-2")
	initialGeneration := m.previewTarget.Generation

	_, command := m.Update(keyMessage("a"))
	_, refresh := m.Update(command().(attachFinishedMsg))
	if refresh == nil || m.selection.AgentID != "agent-1" {
		t.Fatal("writable attach did not preserve selection and refresh")
	}
	m.fleetInFlight = false

	_, command = m.Update(keyMessage("r"))
	_, _ = m.Update(command().(attachFinishedMsg))
	if got, want := options, []cliattach.Options{{ReadOnly: false}, {ReadOnly: true}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attach options = %+v, want %+v", got, want)
	}
	if m.previewTarget.AgentID != "agent-1" ||
		m.previewTarget.Generation != initialGeneration+2 {
		t.Fatalf("preview target after attach = %+v", m.previewTarget)
	}
	if !strings.Contains(m.notice, attachErr.Error()) {
		t.Fatalf("attach error notice = %q", m.notice)
	}
}

func TestAttachCompletionDoesNotOverrideNewSelection(t *testing.T) {
	t.Parallel()

	preview := newRecordingPreview()
	m := newModelWithDependencies(context.Background(), modelDependencies{
		daemon:  &fakeTUIClient{},
		preview: preview,
		attach: func(context.Context, string, cliattach.Options) error {
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
	_, command := m.Update(keyMessage("a"))
	result := command().(attachFinishedMsg)
	_, _ = m.Update(keyMessage("down"))
	generation := m.previewTarget.Generation

	_, _ = m.Update(result)
	if m.selection.AgentID != "agent-2" {
		t.Fatalf("selection = %q, want agent-2", m.selection.AgentID)
	}
	if m.previewTarget.Generation != generation {
		t.Fatalf(
			"attach completion replaced generation %d with %d",
			generation,
			m.previewTarget.Generation,
		)
	}
}

func TestModelQuitDoesNotStopAgent(t *testing.T) {
	t.Parallel()

	var stopCalls atomic.Int32
	m := newTestModel(&fakeTUIClient{
		stopFn: func(context.Context, string) error {
			stopCalls.Add(1)
			return nil
		},
	}, newRecordingPreview())
	seedModel(m, "agent-1")

	_, command := m.Update(keyMessage("q"))
	if command == nil {
		t.Fatal("q did not return a quit command")
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatalf("q command returned %T, want tea.QuitMsg", command())
	}
	if got := stopCalls.Load(); got != 0 {
		t.Fatalf("quit called stop %d times", got)
	}
}

type fakeTUIClient struct {
	listFn    func(context.Context) ([]*session.Status, error)
	sendFn    func(context.Context, string, []byte) error
	stopFn    func(context.Context, string) error
	explainFn func(context.Context, string, session.ExplainOptions) (*session.Explanation, error)
}

func (f *fakeTUIClient) List(ctx context.Context) ([]*session.Status, error) {
	if f.listFn == nil {
		return nil, nil
	}
	return f.listFn(ctx)
}

func (f *fakeTUIClient) SendInput(ctx context.Context, id string, payload []byte) error {
	if f.sendFn == nil {
		return nil
	}
	return f.sendFn(ctx, id, payload)
}

func (f *fakeTUIClient) Stop(ctx context.Context, id string) error {
	if f.stopFn == nil {
		return nil
	}
	return f.stopFn(ctx, id)
}

func (f *fakeTUIClient) Explain(
	ctx context.Context,
	id string,
	options session.ExplainOptions,
) (*session.Explanation, error) {
	if f.explainFn == nil {
		return &session.Explanation{AgentID: id}, nil
	}
	return f.explainFn(ctx, id, options)
}

type recordingPreview struct {
	events       chan previewEvent
	replacements []previewTarget
	closed       bool
}

func newRecordingPreview() *recordingPreview {
	return &recordingPreview{events: make(chan previewEvent, 16)}
}

func (p *recordingPreview) Replace(target previewTarget) {
	p.replacements = append(p.replacements, target)
}

func (p *recordingPreview) Events() <-chan previewEvent {
	return p.events
}

func (p *recordingPreview) Close() error {
	if !p.closed {
		close(p.events)
		p.closed = true
	}
	return nil
}

func (p *recordingPreview) lastReplacement(t *testing.T) previewTarget {
	t.Helper()
	if len(p.replacements) == 0 {
		t.Fatal("preview had no replacements")
	}
	return p.replacements[len(p.replacements)-1]
}

func (p *recordingPreview) replacementCount() int {
	return len(p.replacements)
}

func newTestModel(daemon tuiClient, preview previewController) *model {
	return newModelWithDependencies(context.Background(), modelDependencies{
		daemon:  daemon,
		preview: preview,
		tick: func(time.Duration, func(time.Time) tea.Msg) tea.Cmd {
			return nil
		},
	})
}

func seedModel(m *model, ids ...string) {
	m.allRows = make([]fleetRow, len(ids))
	for index, id := range ids {
		m.allRows[index] = fleetRow{
			AgentID:    id,
			Name:       id,
			Vendor:     "generic",
			State:      agent.StateWorking,
			HookStatus: detect.HookOff,
			UpdatedAt:  time.Now(),
		}
	}
	m.rebuildVisibleRows()
}

func keyMessage(key string) tea.KeyMsg {
	switch key {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
}
