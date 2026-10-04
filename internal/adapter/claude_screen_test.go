package adapter

import (
	"bytes"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
)

func TestClaudeScreenFixtures(t *testing.T) {
	approval := loadScreenFixture(t, "claude", "approval")
	idle := loadScreenFixture(t, "claude", "idle_after_approval")
	interrupted := loadScreenFixture(t, "claude", "interrupted")
	if !bytes.HasSuffix(interrupted, []byte("\x1b[20;")) {
		t.Fatal("interrupt fixture must end in a partial CSI frame")
	}

	for _, mode := range screenFeedModes() {
		t.Run(mode.name, func(t *testing.T) {
			classifier, err := NewRegistry().For("claude").NewScreenClassifier()
			if err != nil {
				t.Fatalf("NewScreenClassifier: %v", err)
			}
			controller := newScreenTestController(t)
			defer closeScreenTestController(t, controller)

			writeScreenFixture(t, controller, approval, mode)
			assertScreenHints(t, observeScreen(t, controller, classifier), []ScreenHint{
				{
					Kind:         detect.KindHumanInputRequired,
					Rule:         "claude.approval_prompt",
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
					Rule:         "claude.approval_prompt",
					Edge:         agent.ScreenEdgeCleared,
					Region:       screenRegionBottom,
					Evidence:     "approval prompt",
					Confidence:   1,
					Confirmation: 500 * time.Millisecond,
				},
				{
					Kind:         detect.KindIdlePrompt,
					Rule:         "claude.idle_prompt",
					Edge:         agent.ScreenEdgePresent,
					Region:       screenRegionBottom,
					Evidence:     "idle prompt",
					Confidence:   1,
					Confirmation: time.Second,
				},
			})

			writeScreenFixture(t, controller, interrupted, mode)
			assertScreenHints(t, observeScreen(t, controller, classifier), []ScreenHint{
				{
					Kind:         detect.KindInterrupted,
					Rule:         "claude.interrupted",
					Edge:         agent.ScreenEdgePresent,
					Region:       screenRegionBottom,
					Evidence:     "interrupt result",
					Confidence:   1,
					Confirmation: time.Second,
				},
			})
		})
	}
}

func TestClaudeScreenNearMissAndOrdinaryError(t *testing.T) {
	for _, mode := range screenFeedModes() {
		t.Run(mode.name, func(t *testing.T) {
			classifier, err := NewRegistry().For("claude").NewScreenClassifier()
			if err != nil {
				t.Fatalf("NewScreenClassifier: %v", err)
			}
			controller := newScreenTestController(t)
			defer closeScreenTestController(t, controller)

			writeScreenFixture(
				t,
				controller,
				loadScreenFixture(t, "claude", "near_miss"),
				mode,
			)
			if hints := observeScreen(t, controller, classifier); len(hints) != 0 {
				t.Fatalf("near-miss hints = %+v, want none", hints)
			}
		})
	}
}

func assertScreenHints(t *testing.T, got, want []ScreenHint) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("hints = %+v, want %+v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("hint %d = %+v, want %+v", index, got[index], want[index])
		}
	}
}
