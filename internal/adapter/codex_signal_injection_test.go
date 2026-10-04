package adapter

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Duang777/drove/internal/agent"
)

func TestCodexSignalInjectionPlan(t *testing.T) {
	plan, err := NewRegistry().For("codex").InjectSignals(
		SignalInjectionRequest{
			Mode:        agent.RunModeOneshot,
			BaseArgs:    []string{"exec"},
			RequestArgs: []string{"--model", "gpt-5"},
			RelayPath:   `/tmp/drove "cli"`,
			SessionDir:  "/tmp/session",
		},
	)
	if err != nil {
		t.Fatalf("inject signals: %v", err)
	}
	if plan.Channel != SignalChannelNotify ||
		!plan.TerminalNotifications ||
		len(plan.Files) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
	if len(plan.Args) != 11 ||
		plan.Args[0] != "-c" ||
		plan.Args[2] != "-c" ||
		plan.Args[3] != `tui.notifications=["approval-requested"]` ||
		plan.Args[4] != "-c" ||
		plan.Args[5] != `tui.notification_method="osc9"` ||
		plan.Args[6] != "-c" ||
		plan.Args[7] != `tui.notification_condition="always"` ||
		plan.Args[8] != "exec" ||
		!slices.Equal(plan.Args[9:], []string{"--model", "gpt-5"}) {
		t.Fatalf("args = %#v", plan.Args)
	}
	override := plan.Args[1]
	for _, want := range []string{
		`notify=[`,
		`/tmp/drove \"cli\"`,
		`"hook"`,
		`"--vendor"`,
		`"codex"`,
		`"--managed-by"`,
		`"drove/v1"`,
		`"--payload-argv"`,
	} {
		if !strings.Contains(override, want) {
			t.Fatalf("notify override %q does not contain %q", override, want)
		}
	}
}

func TestCodexSignalInjectionRejectsManagedKeys(t *testing.T) {
	keys := []string{
		"notify",
		"tui.notifications",
		"tui.notification_method",
		"tui.notification_condition",
	}
	for _, key := range keys {
		forms := [][]string{
			{"-c", key + `="other"`},
			{"--config", key + ` = "other"`},
			{"-c=" + key + `="other"`},
			{"--config=" + key + `="other"`},
		}
		for _, args := range forms {
			for _, field := range []string{"base", "request"} {
				t.Run(key+"/"+field+"/"+args[0], func(t *testing.T) {
					request := SignalInjectionRequest{
						Mode:       agent.RunModeInteractive,
						RelayPath:  "/tmp/drove",
						SessionDir: "/tmp/session",
					}
					if field == "base" {
						request.BaseArgs = args
					} else {
						request.RequestArgs = args
					}
					_, err := NewRegistry().For("codex").InjectSignals(request)
					if !errors.Is(err, ErrSignalInjectionConflict) {
						t.Fatalf("args %v error = %v", args, err)
					}
				})
			}
		}
	}
}

func TestCodexSignalInjectionPreservesNonNotifyOverrides(t *testing.T) {
	plan, err := NewRegistry().For("codex").InjectSignals(
		SignalInjectionRequest{
			Mode:        agent.RunModeInteractive,
			RequestArgs: []string{"-c", "model_reasoning_effort=high"},
			RelayPath:   "/tmp/drove",
			SessionDir:  "/tmp/session",
		},
	)
	if err != nil {
		t.Fatalf("inject signals: %v", err)
	}
	wantTail := []string{"-c", "model_reasoning_effort=high"}
	if !slices.Equal(plan.Args[len(plan.Args)-len(wantTail):], wantTail) {
		t.Fatalf("args = %#v", plan.Args)
	}
}
