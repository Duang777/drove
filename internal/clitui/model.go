package clitui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Duang777/drove/internal/cliattach"
	"github.com/Duang777/drove/internal/session"
)

const fleetRefreshInterval = 500 * time.Millisecond

type tuiClient interface {
	List(context.Context) ([]*session.Status, error)
	SendInput(context.Context, string, []byte) error
	Stop(context.Context, string) error
	Explain(context.Context, string, session.ExplainOptions) (*session.Explanation, error)
}

type attachRunner func(context.Context, string, cliattach.Options) error

type execRunner func(tea.ExecCommand, tea.ExecCallback) tea.Cmd

type tickRunner func(time.Duration, func(time.Time) tea.Msg) tea.Cmd

type modelDependencies struct {
	daemon  tuiClient
	preview previewController
	attach  attachRunner
	exec    execRunner
	tick    tickRunner
}

type focusArea uint8

const (
	focusFleet focusArea = iota
	focusFilter
	focusSend
	focusStop
	focusExplain
)

type actionKind uint8

const (
	actionNone actionKind = iota
	actionSend
	actionStop
	actionExplain
)

type model struct {
	ctx  context.Context
	deps modelDependencies

	allRows   []fleetRow
	rows      []fleetRow
	filter    fleetFilter
	selection selection
	focus     focusArea

	width  int
	height int

	fleetRequestID uint64
	fleetInFlight  bool
	trailingFleet  bool
	fleetErr       error

	previewTarget   previewTarget
	previewSnapshot *clientSnapshot
	previewErr      error
	previewRetryIn  time.Duration

	input        textinput.Model
	actionID     string
	actionKind   actionKind
	actionSerial uint64
	actionBusy   bool
	notice       string

	explanation    *session.Explanation
	explainErr     error
	explainLoading bool
	explainView    viewport.Model

	attachSerial uint64
}

// clientSnapshot keeps the model independent from mutable stream-owned slices.
type clientSnapshot struct {
	Rows       uint16
	Columns    uint16
	Lines      []string
	Truncated  bool
	Restorable bool
	CapturedAt time.Time
}

type fleetTickMsg struct{}

type fleetResultMsg struct {
	RequestID uint64
	Statuses  []*session.Status
	Err       error
}

type previewResultMsg struct {
	Event previewEvent
	OK    bool
}

type actionResultMsg struct {
	Kind        actionKind
	AgentID     string
	Serial      uint64
	Explanation *session.Explanation
	Err         error
}

type attachFinishedMsg struct {
	AgentID string
	Serial  uint64
	Err     error
}

func newModel(
	ctx context.Context,
	daemon tuiClient,
	preview previewController,
	attach attachRunner,
) *model {
	return newModelWithDependencies(ctx, modelDependencies{
		daemon:  daemon,
		preview: preview,
		attach:  attach,
		exec:    tea.Exec,
		tick:    tea.Tick,
	})
}

func newModelWithDependencies(
	ctx context.Context,
	deps modelDependencies,
) *model {
	if ctx == nil {
		ctx = context.Background()
	}
	if deps.exec == nil {
		deps.exec = tea.Exec
	}
	if deps.tick == nil {
		deps.tick = tea.Tick
	}

	input := textinput.New()
	input.Prompt = "> "
	input.Placeholder = "message"
	input.CharLimit = session.MaxInputBytes

	return &model{
		ctx:         ctx,
		deps:        deps,
		filter:      filterAll,
		focus:       focusFleet,
		width:       100,
		height:      30,
		input:       input,
		explainView: viewport.New(96, 22),
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(
		m.startFleetRefresh(),
		m.scheduleFleetTick(),
		m.waitPreviewEvent(),
	)
}

func (m *model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.resize(message.Width, message.Height)
		return m, nil
	case fleetTickMsg:
		return m, tea.Batch(
			m.scheduleFleetTick(),
			m.startFleetRefresh(),
		)
	case fleetResultMsg:
		return m, m.handleFleetResult(message)
	case previewResultMsg:
		return m, m.handlePreviewResult(message)
	case actionResultMsg:
		return m, m.handleActionResult(message)
	case attachFinishedMsg:
		return m, m.handleAttachFinished(message)
	case tea.KeyMsg:
		return m, m.handleKey(message)
	default:
		return m, nil
	}
}

func (m *model) startFleetRefresh() tea.Cmd {
	if m.fleetInFlight {
		m.trailingFleet = true
		return nil
	}
	m.fleetInFlight = true
	m.fleetRequestID++
	requestID := m.fleetRequestID
	return func() tea.Msg {
		if m.deps.daemon == nil {
			return fleetResultMsg{
				RequestID: requestID,
				Err:       errors.New("clitui: client is required"),
			}
		}
		statuses, err := m.deps.daemon.List(m.ctx)
		if err != nil {
			err = fmt.Errorf("clitui: list agents: %w", err)
		}
		return fleetResultMsg{
			RequestID: requestID,
			Statuses:  statuses,
			Err:       err,
		}
	}
}

func (m *model) scheduleFleetTick() tea.Cmd {
	return m.deps.tick(fleetRefreshInterval, func(time.Time) tea.Msg {
		return fleetTickMsg{}
	})
}

func (m *model) handleFleetResult(message fleetResultMsg) tea.Cmd {
	if message.RequestID != m.fleetRequestID || !m.fleetInFlight {
		return nil
	}
	m.fleetInFlight = false
	if message.Err != nil {
		m.fleetErr = message.Err
	} else {
		m.fleetErr = nil
		m.allRows = projectFleet(message.Statuses, filterAll)
		m.rebuildVisibleRows()
	}
	if !m.trailingFleet {
		return nil
	}
	m.trailingFleet = false
	return m.startFleetRefresh()
}

func (m *model) waitPreviewEvent() tea.Cmd {
	if m.deps.preview == nil {
		return nil
	}
	events := m.deps.preview.Events()
	return func() tea.Msg {
		event, ok := <-events
		return previewResultMsg{Event: event, OK: ok}
	}
}

func (m *model) handlePreviewResult(message previewResultMsg) tea.Cmd {
	if !message.OK {
		return nil
	}
	event := message.Event
	if event.Target != m.previewTarget {
		return m.waitPreviewEvent()
	}
	if event.Err != nil {
		m.previewErr = event.Err
		m.previewRetryIn = event.RetryIn
	} else if event.Snapshot != nil {
		m.previewSnapshot = &clientSnapshot{
			Rows:       event.Snapshot.Rows,
			Columns:    event.Snapshot.Columns,
			Lines:      append([]string(nil), event.Snapshot.Lines...),
			Truncated:  event.Snapshot.Truncated,
			Restorable: event.Snapshot.Restorable,
			CapturedAt: event.Snapshot.CapturedAt,
		}
		m.previewErr = nil
		m.previewRetryIn = 0
	}
	return m.waitPreviewEvent()
}

func (m *model) handleKey(message tea.KeyMsg) tea.Cmd {
	switch m.focus {
	case focusFilter:
		return m.handleFilterKey(message)
	case focusSend:
		return m.handleSendKey(message)
	case focusStop:
		return m.handleStopKey(message)
	case focusExplain:
		return m.handleExplainKey(message)
	default:
		return m.handleFleetKey(message)
	}
}

func (m *model) handleFleetKey(message tea.KeyMsg) tea.Cmd {
	switch message.String() {
	case "q", "ctrl+c":
		return tea.Quit
	case "esc":
		m.notice = ""
	case "up", "k":
		m.moveSelection(-1)
	case "down", "j":
		m.moveSelection(1)
	case "f":
		m.focus = focusFilter
	case "s":
		if id, ok := m.selectedAgentID(); ok {
			m.beginAction(actionSend, id)
			m.focus = focusSend
			m.input.Reset()
			return m.input.Focus()
		}
	case "x":
		if id, ok := m.selectedAgentID(); ok {
			m.beginAction(actionStop, id)
			m.focus = focusStop
		}
	case "e":
		if id, ok := m.selectedAgentID(); ok {
			return m.startExplain(id)
		}
	case "a":
		return m.startAttach(false)
	case "r":
		return m.startAttach(true)
	}
	return nil
}

func (m *model) handleFilterKey(message tea.KeyMsg) tea.Cmd {
	switch message.String() {
	case "left":
		m.stepFilter(-1)
	case "right":
		m.stepFilter(1)
	case "enter", "esc":
		m.focus = focusFleet
	}
	return nil
}

func (m *model) handleSendKey(message tea.KeyMsg) tea.Cmd {
	switch message.String() {
	case "esc":
		m.input.Blur()
		m.focus = focusFleet
		m.actionID = ""
		m.actionKind = actionNone
		return nil
	case "enter":
		payload := []byte(m.input.Value() + "\n")
		if len(payload) > session.MaxInputBytes {
			m.notice = fmt.Sprintf(
				"input is %d bytes; maximum is %d",
				len(payload),
				session.MaxInputBytes,
			)
			return nil
		}
		id := m.actionID
		m.input.Blur()
		m.focus = focusFleet
		return m.startSend(id, payload)
	default:
		var command tea.Cmd
		m.input, command = m.input.Update(message)
		return command
	}
}

func (m *model) handleStopKey(message tea.KeyMsg) tea.Cmd {
	switch message.String() {
	case "y":
		id := m.actionID
		m.focus = focusFleet
		return m.startStop(id)
	case "n", "esc":
		m.focus = focusFleet
		m.actionID = ""
		m.actionKind = actionNone
	}
	return nil
}

func (m *model) handleExplainKey(message tea.KeyMsg) tea.Cmd {
	if message.String() == "esc" {
		m.focus = focusFleet
		m.invalidateAction()
		return nil
	}
	var command tea.Cmd
	m.explainView, command = m.explainView.Update(message)
	return command
}

func (m *model) moveSelection(delta int) {
	previous := m.selection.AgentID
	m.selection = moveSelection(m.selection, m.rows, delta)
	if m.selection.AgentID != previous {
		m.replacePreview(m.selection.AgentID)
	}
}

func (m *model) stepFilter(delta int) {
	index := 0
	for candidate := range fleetFilters {
		if fleetFilters[candidate] == m.filter {
			index = candidate
			break
		}
	}
	index = (index + delta + len(fleetFilters)) % len(fleetFilters)
	m.filter = fleetFilters[index]
	m.rebuildVisibleRows()
}

func (m *model) rebuildVisibleRows() {
	previous := m.selection.AgentID
	m.rows = filterProjectedFleet(m.allRows, m.filter)
	m.selection = reconcileSelection(m.selection, m.rows)
	if m.selection.AgentID == previous {
		return
	}
	if m.focus == focusSend || m.focus == focusStop || m.focus == focusExplain {
		m.input.Blur()
		m.focus = focusFleet
		m.invalidateAction()
	}
	m.replacePreview(m.selection.AgentID)
}

func filterProjectedFleet(rows []fleetRow, filter fleetFilter) []fleetRow {
	visible := make([]fleetRow, 0, len(rows))
	for _, row := range rows {
		if filter.matches(row.State) {
			visible = append(visible, row)
		}
	}
	return visible
}

func (m *model) replacePreview(agentID string) {
	m.previewTarget.Generation++
	m.previewTarget.AgentID = agentID
	m.previewSnapshot = nil
	m.previewErr = nil
	m.previewRetryIn = 0
	if m.deps.preview != nil {
		m.deps.preview.Replace(m.previewTarget)
	}
}

func (m *model) selectedAgentID() (string, bool) {
	if m.selection.AgentID == "" {
		return "", false
	}
	for _, row := range m.rows {
		if row.AgentID == m.selection.AgentID {
			return row.AgentID, true
		}
	}
	return "", false
}

func (m *model) selectedRow() (fleetRow, bool) {
	id, ok := m.selectedAgentID()
	if !ok {
		return fleetRow{}, false
	}
	for _, row := range m.rows {
		if row.AgentID == id {
			return row, true
		}
	}
	return fleetRow{}, false
}

func (m *model) beginAction(kind actionKind, agentID string) {
	m.notice = ""
	m.actionID = agentID
	m.actionKind = kind
}

func (m *model) nextAction(kind actionKind, agentID string) uint64 {
	m.beginAction(kind, agentID)
	m.actionSerial++
	m.actionBusy = true
	return m.actionSerial
}

func (m *model) invalidateAction() {
	m.actionSerial++
	m.actionBusy = false
	m.actionID = ""
	m.actionKind = actionNone
	m.explainLoading = false
}

func (m *model) startSend(agentID string, payload []byte) tea.Cmd {
	serial := m.nextAction(actionSend, agentID)
	return func() tea.Msg {
		var err error
		if m.deps.daemon == nil {
			err = errors.New("clitui: client is required")
		} else if sendErr := m.deps.daemon.SendInput(m.ctx, agentID, payload); sendErr != nil {
			err = fmt.Errorf("clitui: send input: %w", sendErr)
		}
		return actionResultMsg{
			Kind:    actionSend,
			AgentID: agentID,
			Serial:  serial,
			Err:     err,
		}
	}
}

func (m *model) startStop(agentID string) tea.Cmd {
	serial := m.nextAction(actionStop, agentID)
	return func() tea.Msg {
		var err error
		if m.deps.daemon == nil {
			err = errors.New("clitui: client is required")
		} else if stopErr := m.deps.daemon.Stop(m.ctx, agentID); stopErr != nil {
			err = fmt.Errorf("clitui: stop agent: %w", stopErr)
		}
		return actionResultMsg{
			Kind:    actionStop,
			AgentID: agentID,
			Serial:  serial,
			Err:     err,
		}
	}
}

func (m *model) startExplain(agentID string) tea.Cmd {
	serial := m.nextAction(actionExplain, agentID)
	m.focus = focusExplain
	m.explanation = nil
	m.explainErr = nil
	m.explainLoading = true
	m.explainView.SetContent("")
	m.explainView.GotoTop()
	return func() tea.Msg {
		var (
			explanation *session.Explanation
			err         error
		)
		if m.deps.daemon == nil {
			err = errors.New("clitui: client is required")
		} else {
			explanation, err = m.deps.daemon.Explain(
				m.ctx,
				agentID,
				session.ExplainOptions{},
			)
			if err != nil {
				err = fmt.Errorf("clitui: explain agent: %w", err)
			}
		}
		return actionResultMsg{
			Kind:        actionExplain,
			AgentID:     agentID,
			Serial:      serial,
			Explanation: explanation,
			Err:         err,
		}
	}
}

func (m *model) handleActionResult(message actionResultMsg) tea.Cmd {
	if message.Serial != m.actionSerial ||
		message.Kind != m.actionKind ||
		message.AgentID != m.actionID {
		return nil
	}
	m.actionBusy = false

	relevant := m.selection.AgentID == message.AgentID
	switch message.Kind {
	case actionSend:
		if relevant {
			if message.Err != nil {
				m.notice = message.Err.Error()
			} else {
				m.notice = "input sent"
			}
		}
		return m.startFleetRefresh()
	case actionStop:
		if relevant {
			if message.Err != nil {
				m.notice = message.Err.Error()
			} else {
				m.notice = "stop requested"
			}
		}
		return m.startFleetRefresh()
	case actionExplain:
		if !relevant || m.focus != focusExplain {
			return nil
		}
		m.explainLoading = false
		m.explainErr = message.Err
		m.explanation = message.Explanation
		m.explainView.SetContent(renderExplanation(message.Explanation, message.Err))
		m.explainView.GotoTop()
	}
	return nil
}

func (m *model) startAttach(readOnly bool) tea.Cmd {
	agentID, ok := m.selectedAgentID()
	if !ok || m.deps.attach == nil {
		return nil
	}
	m.notice = ""
	m.attachSerial++
	serial := m.attachSerial
	command := &attachCommand{
		ctx:     m.ctx,
		run:     m.deps.attach,
		agentID: agentID,
		options: cliattach.Options{ReadOnly: readOnly},
	}
	return m.deps.exec(command, func(err error) tea.Msg {
		return attachFinishedMsg{
			AgentID: agentID,
			Serial:  serial,
			Err:     err,
		}
	})
}

func (m *model) handleAttachFinished(message attachFinishedMsg) tea.Cmd {
	if message.Serial != m.attachSerial {
		return nil
	}
	m.selection.AgentID = message.AgentID
	m.selection = reconcileSelection(m.selection, m.rows)
	m.replacePreview(m.selection.AgentID)
	if message.Err != nil && !errors.Is(message.Err, context.Canceled) {
		m.notice = message.Err.Error()
	}
	return m.startFleetRefresh()
}

func (m *model) resize(width, height int) {
	m.width = max(1, width)
	m.height = max(1, height)
	m.input.Width = max(1, m.width-16)
	m.explainView.Width = max(1, m.width-4)
	m.explainView.Height = max(1, m.height-7)
}

type attachCommand struct {
	ctx     context.Context
	run     attachRunner
	agentID string
	options cliattach.Options
}

func (c *attachCommand) Run() error {
	return c.run(c.ctx, c.agentID, c.options)
}

func (*attachCommand) SetStdin(io.Reader) {}

func (*attachCommand) SetStdout(io.Writer) {}

func (*attachCommand) SetStderr(io.Writer) {}
