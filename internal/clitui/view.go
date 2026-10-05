package clitui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/session"
)

const wideLayoutWidth = 110

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("39"))
	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245"))
	focusStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("229")).
			Background(lipgloss.Color("24"))
	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("203"))
	blockedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("214"))
	doneStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("42"))
	selectedStyle = lipgloss.NewStyle().
			Bold(true)
)

func (m *model) View() string {
	width := max(1, m.width)
	header := m.renderHeader(width)
	footer := fitLine(m.renderFooter(), width)
	status := fitLine(m.renderStatus(), width)
	bodyHeight := max(1, m.height-4)

	var body string
	if m.focus == focusExplain {
		body = m.renderExplainPane(width, bodyHeight)
	} else if width >= wideLayoutWidth {
		leftWidth := (width - 3) / 2
		rightWidth := width - leftWidth - 3
		body = lipgloss.JoinHorizontal(
			lipgloss.Top,
			m.renderFleetPane(leftWidth, bodyHeight),
			dimStyle.Render(" | "),
			m.renderPreviewPane(rightWidth, bodyHeight),
		)
	} else {
		if bodyHeight < 3 {
			body = m.renderFleetPane(width, bodyHeight)
		} else {
			fleetHeight := max(1, (bodyHeight-1)/2)
			previewHeight := bodyHeight - fleetHeight - 1
			body = m.renderFleetPane(width, fleetHeight) + "\n" +
				strings.Repeat(" ", width) + "\n" +
				m.renderPreviewPane(width, previewHeight)
		}
	}

	return strings.Join([]string{
		header,
		fitLine(strings.Repeat("-", width), width),
		body,
		status,
		footer,
	}, "\n")
}

func (m *model) renderHeader(width int) string {
	filter := string(m.filter)
	if m.focus == focusFilter {
		filter = focusStyle.Render("< " + filter + " >")
	}
	blocked := 0
	for _, row := range m.allRows {
		if row.State == agent.StateBlocked {
			blocked++
		}
	}
	header := fmt.Sprintf(
		"DROVE  filter %s  visible %d/%d  blocked %d",
		filter,
		len(m.rows),
		len(m.allRows),
		blocked,
	)
	return fitLine(titleStyle.Render(header), width)
}

func (m *model) renderFleetPane(width, height int) string {
	lines := []string{titleStyle.Render("FLEET")}
	if len(m.rows) == 0 {
		lines = append(lines, dimStyle.Render("No sessions match this filter."))
		return renderLines(lines, width, height)
	}

	rowHeight := 1
	if width < 88 {
		rowHeight = 2
	}
	availableRows := max(1, (height-1)/rowHeight)
	start := 0
	if m.selection.FallbackIndex >= availableRows {
		start = m.selection.FallbackIndex - availableRows + 1
	}
	end := min(len(m.rows), start+availableRows)
	for index := start; index < end; index++ {
		row := m.rows[index]
		marker := "  "
		if row.AgentID == m.selection.AgentID {
			marker = "> "
		}
		name := row.Name
		if name == "" {
			name = row.AgentID
		}
		state := renderState(row.State)
		age := formatAge(time.Since(row.UpdatedAt))
		transition := valueOrDash(row.TransitionEvent)
		if row.TransitionSource != "" {
			transition += "/" + string(row.TransitionSource)
		}

		if rowHeight == 1 {
			line := marker +
				column(state, 10) +
				column(name, 18) +
				column(valueOrDash(row.Vendor), 10) +
				column(valueOrDash(string(row.HookStatus)), 17) +
				column(transition, max(8, width-65)) +
				age
			if marker == "> " {
				line = selectedStyle.Render(line)
			}
			lines = append(lines, line)
			continue
		}

		first := marker + state + "  " + name + "  " + dimStyle.Render(age)
		second := "    " +
			valueOrDash(row.Vendor) + " | " +
			valueOrDash(string(row.HookStatus)) + " | " +
			transition
		if marker == "> " {
			first = selectedStyle.Render(first)
		}
		lines = append(lines, first, dimStyle.Render(second))
	}
	if end < len(m.rows) {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("... %d more", len(m.rows)-end)))
	}
	return renderLines(lines, width, height)
}

func (m *model) renderPreviewPane(width, height int) string {
	lines := []string{titleStyle.Render("SNAPSHOT")}
	row, selected := m.selectedRow()
	switch {
	case !selected:
		lines = append(lines, dimStyle.Render("No session selected."))
	case row.State == agent.StateStopped:
		lines = append(lines, dimStyle.Render("No live snapshot for a stopped session."))
	case m.previewErr != nil:
		lines = append(lines, errorStyle.Render("Snapshot unavailable."))
		lines = append(lines, m.previewErr.Error())
		if m.previewRetryIn > 0 {
			lines = append(
				lines,
				dimStyle.Render("Retrying in "+m.previewRetryIn.String()),
			)
		}
	case m.previewSnapshot == nil:
		lines = append(lines, dimStyle.Render("Loading snapshot..."))
	default:
		snapshot := m.previewSnapshot
		metadata := fmt.Sprintf(
			"%dx%d  captured %s  truncated=%t",
			snapshot.Columns,
			snapshot.Rows,
			snapshot.CapturedAt.Local().Format("15:04:05"),
			snapshot.Truncated,
		)
		lines = append(lines, dimStyle.Render(metadata))
		if len(snapshot.Lines) == 0 {
			lines = append(lines, dimStyle.Render("Snapshot is empty."))
			break
		}
		available := max(1, height-len(lines))
		start := max(0, len(snapshot.Lines)-available)
		lines = append(lines, snapshot.Lines[start:]...)
	}
	return renderLines(lines, width, height)
}

func (m *model) renderExplainPane(width, height int) string {
	content := "Loading explanation..."
	if !m.explainLoading {
		content = m.explainView.View()
	}
	lines := []string{titleStyle.Render("EXPLAIN " + valueOrDash(m.actionID))}
	lines = append(lines, strings.Split(content, "\n")...)
	return renderLines(lines, width, height)
}

func (m *model) renderStatus() string {
	switch m.focus {
	case focusSend:
		return fmt.Sprintf("SEND %s  %s", m.actionID, m.input.View())
	case focusStop:
		return fmt.Sprintf("STOP %s? [y/N]", m.actionID)
	}
	if m.notice != "" {
		return m.notice
	}
	if m.fleetErr != nil {
		return errorStyle.Render(m.fleetErr.Error())
	}
	if m.actionBusy {
		switch m.actionKind {
		case actionSend:
			return dimStyle.Render("Sending input...")
		case actionStop:
			return dimStyle.Render("Requesting stop...")
		case actionExplain:
			return dimStyle.Render("Loading explanation...")
		}
	}
	return ""
}

func (m *model) renderFooter() string {
	switch m.focus {
	case focusFilter:
		return "left/right filter  enter/esc done"
	case focusSend:
		return "enter send  esc cancel"
	case focusStop:
		return "y stop  n/esc cancel"
	case focusExplain:
		return "up/down scroll  pgup/pgdown page  esc close"
	default:
		return "up/down select  f filter  a attach  r read-only  s send  x stop  e explain  q quit"
	}
}

func renderExplanation(explanation *session.Explanation, err error) string {
	if err != nil {
		return "Explanation unavailable.\n" + err.Error()
	}
	if explanation == nil {
		return "Explanation unavailable."
	}

	var output strings.Builder
	fmt.Fprintf(
		&output,
		"state=%s hook=%s attached=%t\n",
		explanation.State,
		explanation.HookStatus,
		explanation.Attached,
	)
	for _, event := range explanation.Events {
		fmt.Fprintf(
			&output,
			"%d %s %s",
			event.Seq,
			event.Timestamp.Local().Format(time.RFC3339),
			event.Type,
		)
		appendExplainField(&output, "source", string(event.Source))
		appendExplainField(&output, "kind", string(event.Kind))
		appendExplainField(&output, "outcome", string(event.Outcome))
		appendExplainField(&output, "rule", event.Rule)
		appendExplainField(&output, "edge", string(event.Edge))
		appendExplainField(&output, "region", event.Region)
		appendExplainField(&output, "protocol", event.Protocol)
		if event.OutputOffset != 0 {
			fmt.Fprintf(&output, " output_offset=%d", event.OutputOffset)
		}
		if event.LastOutputSeq != 0 {
			fmt.Fprintf(&output, " output_seq=%d", event.LastOutputSeq)
		}
		appendExplainField(&output, "evidence", event.Evidence)
		appendExplainField(&output, "suppressed", event.SuppressionReason)
		if event.From != "" || event.To != "" {
			fmt.Fprintf(&output, " transition=%s->%s", event.From, event.To)
		}
		appendExplainField(&output, "reason", event.Reason)
		if event.UnsupportedVersion != nil {
			fmt.Fprintf(&output, " unsupported_version=%d", *event.UnsupportedVersion)
		}
		output.WriteByte('\n')
	}
	if explanation.Screen != nil {
		fmt.Fprintf(
			&output,
			"screen captured=%s truncated=%t\n",
			explanation.Screen.CapturedAt.Local().Format(time.RFC3339),
			explanation.Screen.Truncated,
		)
		for _, row := range explanation.Screen.Rows {
			output.WriteString(row)
			output.WriteByte('\n')
		}
	}
	return strings.TrimRight(output.String(), "\n")
}

func appendExplainField(output *strings.Builder, name, value string) {
	if value != "" {
		fmt.Fprintf(output, " %s=%q", name, value)
	}
}

func renderState(state agent.State) string {
	switch state {
	case agent.StateBlocked:
		return blockedStyle.Render(string(state))
	case agent.StateDone:
		return doneStyle.Render(string(state))
	default:
		return string(state)
	}
}

func formatAge(age time.Duration) string {
	if age < 0 {
		age = 0
	}
	switch {
	case age < time.Second:
		return "now"
	case age < time.Minute:
		return fmt.Sprintf("%ds", int(age/time.Second))
	case age < time.Hour:
		return fmt.Sprintf("%dm", int(age/time.Minute))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh", int(age/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(age/(24*time.Hour)))
	}
}

func renderLines(lines []string, width, height int) string {
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for index := range lines {
		lines[index] = fitLine(lines[index], width)
	}
	return strings.Join(lines, "\n")
}

func fitLine(line string, width int) string {
	if width <= 0 {
		return ""
	}
	line = ansi.Truncate(line, width, "")
	padding := width - lipgloss.Width(line)
	if padding > 0 {
		line += strings.Repeat(" ", padding)
	}
	return line
}

func column(value string, width int) string {
	if width <= 0 {
		return ""
	}
	return fitLine(value, width)
}

func valueOrDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
