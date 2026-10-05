package clitui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/session"
)

func TestViewWideLayoutShowsFleetAndSelectedSnapshot(t *testing.T) {
	t.Parallel()

	m := newTestModel(&fakeTUIClient{}, newRecordingPreview())
	m.resize(130, 24)
	m.allRows = []fleetRow{
		{
			AgentID:          "agent-blocked",
			Name:             "release-check",
			Vendor:           "codex",
			State:            agent.StateBlocked,
			HookStatus:       detect.HookActive,
			TransitionEvent:  "permission_requested",
			TransitionSource: agent.EvidenceHook,
			UpdatedAt:        time.Now().Add(-3 * time.Second),
		},
		{
			AgentID:    "agent-working",
			Name:       "implementation",
			Vendor:     "claude",
			State:      agent.StateWorking,
			HookStatus: detect.HookFallback,
			UpdatedAt:  time.Now(),
		},
	}
	m.rebuildVisibleRows()
	m.previewSnapshot = &clientSnapshot{
		Rows:       40,
		Columns:    120,
		Lines:      []string{"building package", "waiting for approval"},
		Truncated:  true,
		Restorable: true,
		CapturedAt: time.Now(),
	}

	view := ansi.Strip(m.View())
	for _, want := range []string{
		"DROVE",
		"visible 2/2",
		"blocked 1",
		"FLEET",
		"SNAPSHOT",
		"> blocked",
		"release-check",
		"codex",
		"hook_active",
		"permission_requested/hook",
		"120x40",
		"truncated=true",
		"waiting for approval",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("wide view omitted %q:\n%s", want, view)
		}
	}
	assertViewDimensions(t, m.View(), 130, 24)
}

func TestViewNarrowLayoutStacksPanesWithoutOverflow(t *testing.T) {
	t.Parallel()

	m := newTestModel(&fakeTUIClient{}, newRecordingPreview())
	m.resize(70, 28)
	m.allRows = []fleetRow{
		{
			AgentID:          "agent-1",
			Name:             "a-long-but-readable-session-name",
			Vendor:           "generic",
			State:            agent.StateWorking,
			HookStatus:       detect.HookOff,
			TransitionEvent:  "session_start",
			TransitionSource: agent.EvidenceSession,
			UpdatedAt:        time.Now(),
		},
	}
	m.rebuildVisibleRows()
	m.previewSnapshot = &clientSnapshot{
		Rows:       24,
		Columns:    80,
		Lines:      []string{"first line", "second line"},
		CapturedAt: time.Now(),
	}

	view := ansi.Strip(m.View())
	fleetAt := strings.Index(view, "FLEET")
	snapshotAt := strings.Index(view, "SNAPSHOT")
	if fleetAt < 0 || snapshotAt <= fleetAt {
		t.Fatalf("narrow panes are not stacked:\n%s", view)
	}
	for _, want := range []string{
		"> working",
		"generic | off | session_start/session",
		"second line",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("narrow view omitted %q:\n%s", want, view)
		}
	}
	assertViewDimensions(t, m.View(), 70, 28)
}

func TestViewShowsCurrentFocusAndKeyboardHelp(t *testing.T) {
	t.Parallel()

	m := newTestModel(&fakeTUIClient{}, newRecordingPreview())
	m.resize(90, 22)
	seedModel(m, "agent-1")

	m.focus = focusFilter
	if view := ansi.Strip(m.View()); !strings.Contains(view, "< all >") ||
		!strings.Contains(view, "left/right filter") {
		t.Fatalf("filter focus not visible:\n%s", view)
	}

	m.focus = focusSend
	m.actionID = "agent-1"
	m.input.SetValue("approve")
	if view := ansi.Strip(m.View()); !strings.Contains(view, "SEND agent-1") ||
		!strings.Contains(view, "enter send") {
		t.Fatalf("send focus not visible:\n%s", view)
	}

	m.focus = focusStop
	if view := ansi.Strip(m.View()); !strings.Contains(view, "STOP agent-1? [y/N]") ||
		!strings.Contains(view, "y stop") {
		t.Fatalf("stop focus not visible:\n%s", view)
	}

	m.focus = focusExplain
	m.explainLoading = false
	m.explanation = &session.Explanation{
		AgentID:    "agent-1",
		State:      agent.StateBlocked,
		HookStatus: detect.HookActive,
		Attached:   true,
		Events: []session.ExplainEvent{
			{Seq: 4, Type: event.TypeStateChanged, Source: agent.EvidenceHook},
		},
	}
	m.explainView.SetContent(renderExplanation(m.explanation, nil))
	if view := ansi.Strip(m.View()); !strings.Contains(view, "EXPLAIN agent-1") ||
		!strings.Contains(view, "state=blocked") ||
		!strings.Contains(view, "pgup/pgdown") {
		t.Fatalf("explain focus not visible:\n%s", view)
	}
}

func TestViewDistinguishesPreviewStates(t *testing.T) {
	t.Parallel()

	m := newTestModel(&fakeTUIClient{}, newRecordingPreview())
	m.resize(80, 24)
	m.allRows = []fleetRow{{
		AgentID:   "agent-1",
		Name:      "agent-1",
		State:     agent.StateWorking,
		UpdatedAt: time.Now(),
	}}
	m.rebuildVisibleRows()

	if view := ansi.Strip(m.View()); !strings.Contains(view, "Loading snapshot") {
		t.Fatalf("loading state missing:\n%s", view)
	}
	m.previewErr = errors.New("not attached")
	m.previewRetryIn = 2 * time.Second
	if view := ansi.Strip(m.View()); !strings.Contains(view, "Snapshot unavailable") ||
		!strings.Contains(view, "Retrying in 2s") {
		t.Fatalf("retry state missing:\n%s", view)
	}
	m.previewErr = nil
	m.previewSnapshot = &clientSnapshot{Rows: 24, Columns: 80}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "Snapshot is empty") {
		t.Fatalf("empty state missing:\n%s", view)
	}
	m.previewSnapshot = nil
	m.allRows[0].State = agent.StateStopped
	m.rebuildVisibleRows()
	if view := ansi.Strip(m.View()); !strings.Contains(view, "No live snapshot") {
		t.Fatalf("stopped state missing:\n%s", view)
	}
}

func assertViewDimensions(t *testing.T, view string, width, height int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) != height {
		t.Fatalf("view lines = %d, want %d", len(lines), height)
	}
	for index, line := range lines {
		if got := ansi.StringWidth(line); got != width {
			t.Fatalf("line %d width = %d, want %d: %q", index, got, width, ansi.Strip(line))
		}
	}
}
