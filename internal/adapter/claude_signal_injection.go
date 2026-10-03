package adapter

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

const claudeSettingsFile = "claude-settings.json"

var claudeInjectedEvents = []string{
	"SessionStart",
	"UserPromptSubmit",
	"PreToolUse",
	"PostToolUse",
	"PostToolUseFailure",
	"PostToolBatch",
	"PermissionRequest",
	"PermissionDenied",
	"Elicitation",
	"ElicitationResult",
	"Notification",
	"Stop",
	"StopFailure",
	"SubagentStart",
	"SubagentStop",
	"TaskCompleted",
	"SessionEnd",
}

const claudeNotificationMatcher = "permission_prompt|elicitation_dialog|" +
	"elicitation_url_dialog|agent_needs_input|idle_prompt|quota_auto_resume_stale"

type claudeSignalInjector struct{}

type claudeInjectedSettings struct {
	Hooks map[string][]claudeMatcherGroup `json:"hooks"`
}

type claudeMatcherGroup struct {
	Matcher string          `json:"matcher,omitempty"`
	Hooks   []claudeHandler `json:"hooks"`
}

type claudeHandler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

func (claudeSignalInjector) InjectSignals(
	request SignalInjectionRequest,
) (SignalInjectionPlan, error) {
	if conflict := claudeInjectionConflict(request.RequestArgs); conflict != "" {
		return SignalInjectionPlan{}, fmt.Errorf(
			"%w: %s",
			ErrSignalInjectionConflict,
			conflict,
		)
	}

	command := shellCommand(
		request.RelayPath,
		managedRelayArgs("claude", false),
	)
	hooks := make(map[string][]claudeMatcherGroup, len(claudeInjectedEvents))
	for _, eventName := range claudeInjectedEvents {
		group := claudeMatcherGroup{
			Hooks: []claudeHandler{{
				Type:    "command",
				Command: command,
				Timeout: 5,
			}},
		}
		if eventName == "Notification" {
			group.Matcher = claudeNotificationMatcher
		}
		hooks[eventName] = []claudeMatcherGroup{group}
	}
	content, err := json.MarshalIndent(
		claudeInjectedSettings{Hooks: hooks},
		"",
		"  ",
	)
	if err != nil {
		return SignalInjectionPlan{}, fmt.Errorf(
			"adapter: encode Claude injected settings: %w",
			err,
		)
	}
	content = append(content, '\n')

	args := make([]string, 0, len(request.BaseArgs)+len(request.RequestArgs)+2)
	args = append(args, request.BaseArgs...)
	args = append(
		args,
		"--settings",
		filepath.Join(request.SessionDir, claudeSettingsFile),
	)
	args = append(args, request.RequestArgs...)
	return SignalInjectionPlan{
		Args: args,
		Files: []SignalInjectionFile{{
			Path:    claudeSettingsFile,
			Content: content,
			Mode:    0o600,
		}},
		Channel: SignalChannelHook,
	}, nil
}

func claudeInjectionConflict(args []string) string {
	for _, arg := range args {
		switch {
		case arg == "--bare":
			return "--bare disables injected settings"
		case arg == "--settings", strings.HasPrefix(arg, "--settings="):
			return "--settings is already set"
		}
	}
	return ""
}
