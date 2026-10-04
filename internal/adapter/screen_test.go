package adapter

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/term"
)

func TestScreenClassifierEmitsIndependentCopiedEdges(t *testing.T) {
	entry := NewRegistry().For("claude")
	first, err := entry.NewScreenClassifier()
	if err != nil {
		t.Fatalf("NewScreenClassifier first: %v", err)
	}
	second, err := entry.NewScreenClassifier()
	if err != nil {
		t.Fatalf("NewScreenClassifier second: %v", err)
	}

	present := screenTestSnapshot(t,
		"\x1b[2J\x1b[17;1HDo you want to proceed?"+
			"\x1b[19;1H1. Yes"+
			"\x1b[20;1HEsc to cancel",
	)
	hints := first.Observe(present)
	assertScreenHint(t, hints, ScreenHint{
		Kind:         detect.KindHumanInputRequired,
		Rule:         "claude.approval_prompt",
		Edge:         agent.ScreenEdgePresent,
		Region:       screenRegionBottom,
		Evidence:     "approval prompt",
		Confidence:   1,
		Confirmation: 750 * time.Millisecond,
	})
	if repeated := first.Observe(present); len(repeated) != 0 {
		t.Fatalf("repeated hints = %+v, want none", repeated)
	}
	if independent := second.Observe(present); len(independent) != 1 {
		t.Fatalf("second classifier hints = %+v, want one", independent)
	}

	hints[0].Rule = "mutated"
	cleared := screenTestSnapshot(t, "\x1b[2J\x1b[Hordinary output")
	assertScreenHint(t, first.Observe(cleared), ScreenHint{
		Kind:         detect.KindHumanInputResolved,
		Rule:         "claude.approval_prompt",
		Edge:         agent.ScreenEdgeCleared,
		Region:       screenRegionBottom,
		Evidence:     "approval prompt",
		Confidence:   1,
		Confirmation: 500 * time.Millisecond,
	})
	assertScreenHint(t, first.Observe(present), ScreenHint{
		Kind:         detect.KindHumanInputRequired,
		Rule:         "claude.approval_prompt",
		Edge:         agent.ScreenEdgePresent,
		Region:       screenRegionBottom,
		Evidence:     "approval prompt",
		Confidence:   1,
		Confirmation: 750 * time.Millisecond,
	})
}

func TestScreenClassifierRebaselineDoesNotEmit(t *testing.T) {
	classifier, err := NewRegistry().For("claude").NewScreenClassifier()
	if err != nil {
		t.Fatalf("NewScreenClassifier: %v", err)
	}
	present := screenTestSnapshot(t,
		"\x1b[2J\x1b[17;1HDo you want to proceed?"+
			"\x1b[20;1HEsc to cancel",
	)
	classifier.Rebaseline(present)
	if hints := classifier.Observe(present); len(hints) != 0 {
		t.Fatalf("rebaseline emitted hints: %+v", hints)
	}

	cleared := screenTestSnapshot(t, "\x1b[2J\x1b[Hready")
	hints := classifier.Observe(cleared)
	if len(hints) != 1 || hints[0].Edge != agent.ScreenEdgeCleared {
		t.Fatalf("clearance hints = %+v, want one cleared edge", hints)
	}
}

func TestScreenClassifierRejectsInvalidDefinitions(t *testing.T) {
	valid := claudeScreenRules()[0]
	tests := []struct {
		name   string
		change func(*screenRuleDefinition)
	}{
		{
			name: "vendor",
			change: func(rule *screenRuleDefinition) {
				rule.name = "codex.approval_prompt"
			},
		},
		{
			name: "region",
			change: func(rule *screenRuleDefinition) {
				rule.region = screenRegion{}
			},
		},
		{
			name: "evidence",
			change: func(rule *screenRuleDefinition) {
				rule.evidence = "dynamic\ntext"
			},
		},
		{
			name: "confidence",
			change: func(rule *screenRuleDefinition) {
				rule.confidence = 2
			},
		},
		{
			name: "confirmation",
			change: func(rule *screenRuleDefinition) {
				rule.present.confirmation = time.Millisecond
			},
		},
		{
			name: "pattern",
			change: func(rule *screenRuleDefinition) {
				rule.pattern = "["
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rule := valid
			test.change(&rule)
			if _, err := newScreenClassifier(
				"claude",
				[]screenRuleDefinition{rule},
			); err == nil {
				t.Fatal("invalid screen rule was accepted")
			}
		})
	}

	if _, err := newScreenClassifier(
		"claude",
		[]screenRuleDefinition{valid, valid},
	); err == nil {
		t.Fatal("duplicate screen rules were accepted")
	}
}

func TestGenericScreenClassifierIsEmpty(t *testing.T) {
	classifier, err := NewRegistry().For("generic").NewScreenClassifier()
	if err != nil {
		t.Fatalf("NewScreenClassifier: %v", err)
	}
	snapshot := screenTestSnapshot(t, "\x1b[2J\x1b[HError: approval required")
	classifier.Rebaseline(snapshot)
	if hints := classifier.Observe(snapshot); len(hints) != 0 {
		t.Fatalf("generic hints = %+v, want none", hints)
	}
}

func assertScreenHint(t *testing.T, hints []ScreenHint, want ScreenHint) {
	t.Helper()
	if len(hints) != 1 {
		t.Fatalf("hints = %+v, want one", hints)
	}
	if hints[0] != want {
		t.Fatalf("hint = %+v, want %+v", hints[0], want)
	}
}

type screenFeedMode struct {
	name  string
	sizes []int
}

func screenFeedModes() []screenFeedMode {
	return []screenFeedMode{
		{name: "single-byte", sizes: []int{1}},
		{name: "control-boundaries", sizes: []int{1, 2, 5, 1, 3, 8}},
		{name: "realistic-chunks", sizes: []int{31, 64, 7, 128}},
	}
}

func loadScreenFixture(t *testing.T, vendor, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", vendor, name+".bin"))
	if err != nil {
		t.Fatalf("ReadFile fixture: %v", err)
	}
	return data
}

func writeScreenFixture(
	t *testing.T,
	controller *term.Controller,
	data []byte,
	mode screenFeedMode,
) {
	t.Helper()
	for offset, sizeIndex := 0, 0; offset < len(data); sizeIndex++ {
		size := mode.sizes[sizeIndex%len(mode.sizes)]
		end := min(len(data), offset+size)
		if err := controller.Write(data[offset:end]); err != nil {
			t.Fatalf("Write fixture at %d: %v", offset, err)
		}
		offset = end
	}
}

func observeScreen(
	t *testing.T,
	controller *term.Controller,
	classifier *ScreenClassifier,
) []ScreenHint {
	t.Helper()
	snapshot, err := controller.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return classifier.Observe(snapshot)
}

func newScreenTestController(t *testing.T) *term.Controller {
	t.Helper()
	size, err := term.NewSize(20, 100)
	if err != nil {
		t.Fatalf("NewSize: %v", err)
	}
	controller, err := term.NewController(size, func([]byte) error { return nil })
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	return controller
}

func closeScreenTestController(t *testing.T, controller *term.Controller) {
	t.Helper()
	if err := controller.Close(); err != nil {
		t.Fatalf("Close controller: %v", err)
	}
}

func screenTestSnapshot(t *testing.T, output string) term.Snapshot {
	t.Helper()
	controller := newScreenTestController(t)
	t.Cleanup(func() {
		if closeErr := controller.Close(); closeErr != nil {
			t.Errorf("Close controller: %v", closeErr)
		}
	})
	if err := controller.Write([]byte(output)); err != nil {
		t.Fatalf("Write controller: %v", err)
	}
	snapshot, err := controller.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return snapshot
}
