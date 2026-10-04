package adapter

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Duang777/drove/internal/agent"
)

func TestRegistryDefaults(t *testing.T) {
	r := NewRegistry()
	for _, v := range []string{"claude", "codex", "generic"} {
		found := false
		for _, got := range r.Vendors() {
			if got == v {
				found = true
			}
		}
		if !found {
			t.Fatalf("vendor %q not registered", v)
		}
	}
	// 未知厂商回退 generic。
	e := r.For("unknown-vendor")
	if e.Runner.Vendor() != "generic" {
		t.Fatalf("fallback vendor = %q, want generic", e.Runner.Vendor())
	}
}

func TestRunnerCommands(t *testing.T) {
	tests := []struct {
		name     string
		runner   Runner
		mode     agent.RunMode
		wantName string
		wantArgs []string
	}{
		{name: "claude interactive", runner: claudeRunner{}, mode: agent.RunModeInteractive, wantName: "claude"},
		{name: "claude oneshot", runner: claudeRunner{}, mode: agent.RunModeOneshot, wantName: "claude", wantArgs: []string{"--print"}},
		{name: "codex interactive", runner: codexRunner{}, mode: agent.RunModeInteractive, wantName: "codex"},
		{name: "codex oneshot", runner: codexRunner{}, mode: agent.RunModeOneshot, wantName: "codex", wantArgs: []string{"exec"}},
		{name: "generic interactive", runner: genericRunner{}, mode: agent.RunModeInteractive},
		{name: "generic oneshot", runner: genericRunner{}, mode: agent.RunModeOneshot},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			name, args := test.runner.Command(test.mode)
			if name != test.wantName {
				t.Fatalf("command name = %q, want %q", name, test.wantName)
			}
			if len(args) != len(test.wantArgs) {
				t.Fatalf("command args = %v, want %v", args, test.wantArgs)
			}
			for i := range test.wantArgs {
				if args[i] != test.wantArgs[i] {
					t.Fatalf("command args = %v, want %v", args, test.wantArgs)
				}
			}
		})
	}
}

func TestResumeCommands(t *testing.T) {
	registry := NewRegistry()
	tests := []struct {
		vendor string
		want   Command
	}{
		{
			vendor: "claude",
			want:   Command{Name: "claude", Args: []string{"--resume", "session-ref"}},
		},
		{
			vendor: "codex",
			want:   Command{Name: "codex", Args: []string{"resume", "session-ref"}},
		},
	}
	for _, test := range tests {
		t.Run(test.vendor, func(t *testing.T) {
			entry, ok := registry.Lookup(test.vendor)
			if !ok || !entry.SupportsResume() {
				t.Fatalf("adapter %q does not advertise resume", test.vendor)
			}
			command, err := entry.ResumeCommand(
				CreationMeta{Mode: agent.RunModeInteractive},
				"session-ref",
			)
			if err != nil {
				t.Fatalf("resume command: %v", err)
			}
			if !reflect.DeepEqual(command, test.want) {
				t.Fatalf("resume command = %+v, want %+v", command, test.want)
			}
		})
	}
}

func TestGenericAndUnknownVendorsDoNotResume(t *testing.T) {
	registry := NewRegistry()
	for _, vendor := range []string{"generic", "unknown"} {
		entry := registry.For(vendor)
		if entry.SupportsResume() {
			t.Fatalf("adapter %q advertises resume", vendor)
		}
		if _, err := entry.ResumeCommand(
			CreationMeta{Mode: agent.RunModeInteractive},
			"session-ref",
		); !errors.Is(err, ErrUnsupportedResume) {
			t.Fatalf("adapter %q resume error = %v", vendor, err)
		}
	}
}

func TestResumeCommandRejectsInvalidReferences(t *testing.T) {
	entry := NewRegistry().For("claude")
	for _, ref := range []string{
		"",
		" leading",
		"trailing ",
		"line\nbreak",
		"non-ascii-\u4f1a\u8bdd",
		string(make([]byte, 257)),
	} {
		if _, err := entry.ResumeCommand(
			CreationMeta{Mode: agent.RunModeInteractive},
			ref,
		); err == nil {
			t.Fatalf("accepted invalid session reference %q", ref)
		}
	}
}
