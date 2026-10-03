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
	if plan.Channel != SignalChannelNotify || len(plan.Files) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
	if len(plan.Args) != 5 ||
		plan.Args[0] != "-c" ||
		plan.Args[2] != "exec" ||
		!slices.Equal(plan.Args[3:], []string{"--model", "gpt-5"}) {
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

func TestCodexSignalInjectionRejectsExistingNotify(t *testing.T) {
	tests := [][]string{
		{"-c", `notify=["other"]`},
		{"--config", `notify = ["other"]`},
		{`-c=notify=["other"]`},
		{`--config=notify=["other"]`},
	}
	for _, args := range tests {
		_, err := NewRegistry().For("codex").InjectSignals(
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
