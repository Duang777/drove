package adapter

import (
	"testing"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
)

func TestCodexScreenFixtures(t *testing.T) {
	approval := loadScreenFixture(t, "codex", "approval")
	idle := loadScreenFixture(t, "codex", "idle_after_approval")

	for _, mode := range screenFeedModes() {
		t.Run(mode.name, func(t *testing.T) {
			classifier, err := NewRegistry().For("codex").NewScreenClassifier()
			if err != nil {
				t.Fatalf("NewScreenClassifier: %v", err)
			}
			controller := newScreenTestController(t)
			defer closeScreenTestController(t, controller)

			writeScreenFixture(t, controller, approval, mode)
			assertScreenHints(t, observeScreen(t, controller, classifier), []ScreenHint{
				{
					Kind:         detect.KindHumanInputRequired,
					Rule:         "codex.approval_prompt",
					Edge:         agent.ScreenEdgePresent,
					Region:       screenRegionBottom,
					Evidence:     "approval prompt",
					Confidence:   1,
					Confirmation: 750 * time.Millisecond,
				},
			})

			writeScreenFixture(t, controller, idle, mode)
			assertScreenHints(t, observeScreen(t, controller, classifier), []ScreenHint{
				{
					Kind:         detect.KindHumanInputResolved,
					Rule:         "codex.approval_prompt",
					Edge:         agent.ScreenEdgeCleared,
					Region:       screenRegionBottom,
					Evidence:     "approval prompt",
					Confidence:   1,
					Confirmation: 500 * time.Millisecond,
				},
				{
					Kind:         detect.KindIdlePrompt,
					Rule:         "codex.idle_prompt",
					Edge:         agent.ScreenEdgePresent,
					Region:       screenRegionBottom,
					Evidence:     "idle prompt",
					Confidence:   1,
					Confirmation: time.Second,
				},
			})
		})
	}
}

func TestCodexScreenNearMissAndOrdinaryError(t *testing.T) {
	for _, mode := range screenFeedModes() {
		t.Run(mode.name, func(t *testing.T) {
			classifier, err := NewRegistry().For("codex").NewScreenClassifier()
			if err != nil {
				t.Fatalf("NewScreenClassifier: %v", err)
			}
			controller := newScreenTestController(t)
			defer closeScreenTestController(t, controller)

			writeScreenFixture(
				t,
				controller,
				loadScreenFixture(t, "codex", "near_miss"),
				mode,
			)
			if hints := observeScreen(t, controller, classifier); len(hints) != 0 {
				t.Fatalf("near-miss hints = %+v, want none", hints)
			}
		})
	}
}
