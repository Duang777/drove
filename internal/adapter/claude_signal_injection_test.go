package adapter

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Duang777/drove/internal/agent"
)

func TestClaudeSignalInjectionPlan(t *testing.T) {
	entry := NewRegistry().For("claude")
	plan, err := entry.InjectSignals(SignalInjectionRequest{
		Mode:        agent.RunModeOneshot,
		BaseArgs:    []string{"--print"},
		RequestArgs: []string{"--model", "sonnet"},
		RelayPath:   "/tmp/drove cli",
		SessionDir:  "/tmp/session",
	})
	if err != nil {
		t.Fatalf("inject signals: %v", err)
	}
	wantArgs := []string{
		"--print",
		"--settings",
		"/tmp/session/claude-settings.json",
		"--model",
		"sonnet",
	}
	if !slices.Equal(plan.Args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", plan.Args, wantArgs)
	}
	if plan.Channel != SignalChannelHook || len(plan.Files) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	file := plan.Files[0]
	if file.Path != claudeSettingsFile || file.Mode != 0o600 {
		t.Fatalf("file = %+v", file)
	}

	var settings map[string]json.RawMessage
	if err := json.Unmarshal(file.Content, &settings); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if len(settings) != 1 || settings["hooks"] == nil {
		t.Fatalf("settings keys = %#v", settings)
	}
	var hooks map[string][]claudeMatcherGroup
	if err := json.Unmarshal(settings["hooks"], &hooks); err != nil {
		t.Fatalf("decode hooks: %v", err)
	}
	if len(hooks) != len(claudeInjectedEvents) {
		t.Fatalf("hook events = %d, want %d", len(hooks), len(claudeInjectedEvents))
	}
	for _, eventName := range claudeInjectedEvents {
		groups := hooks[eventName]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s groups = %+v", eventName, groups)
		}
		handler := groups[0].Hooks[0]
		if handler.Type != "command" ||
			handler.Timeout != 5 ||
			!strings.Contains(handler.Command, "'--managed-by' 'drove/v1'") ||
			strings.Contains(handler.Command, "DROVE_SIGNAL_TOKEN") {
			t.Fatalf("%s handler = %+v", eventName, handler)
		}
		if eventName == "Notification" {
			if groups[0].Matcher != claudeNotificationMatcher {
				t.Fatalf("notification matcher = %q", groups[0].Matcher)
			}
		} else if groups[0].Matcher != "" {
			t.Fatalf("%s matcher = %q, want empty", eventName, groups[0].Matcher)
		}
	}
}

func TestClaudeSignalInjectionRejectsExplicitSettings(t *testing.T) {
	tests := [][]string{
		{"--bare"},
		{"--settings", "/tmp/user.json"},
		{"--settings=/tmp/user.json"},
	}
	for _, args := range tests {
		_, err := NewRegistry().For("claude").InjectSignals(
			SignalInjectionRequest{
				Mode:        agent.RunModeInteractive,
				RequestArgs: args,
				RelayPath:   "/tmp/drove",
				SessionDir:  "/tmp/session",
			},
		)
		if !errors.Is(err, ErrSignalInjectionConflict) {
			t.Fatalf("args %v error = %v", args, err)
		}
	}
}
