package adapter

import (
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
	// 未知厂商回退 generic（无启发式）。
	e := r.For("unknown-vendor")
	if e.Heuristic != nil {
		t.Fatal("unknown vendor should fall back to nil heuristic")
	}
	if e.Runner.Vendor() != "generic" {
		t.Fatalf("fallback vendor = %q, want generic", e.Runner.Vendor())
	}
}

func TestClaudeHeuristic(t *testing.T) {
	h := claudeHeuristic{}
	cases := []struct {
		line  string
		state agent.State
		ok    bool
	}{
		{"Waiting for your input…", agent.StateBlocked, true},
		{"Error: something failed", agent.StateBlocked, true},
		{"Task complete!", agent.StateDone, true},
		{"Processing file foo.go", "", false},
	}
	for _, c := range cases {
		got, ok := h.Classify(c.line)
		if ok != c.ok {
			t.Errorf("Classify(%q) ok = %v, want %v", c.line, ok, c.ok)
			continue
		}
		if ok && got.State != c.state {
			t.Errorf("Classify(%q) state = %s, want %s", c.line, got.State, c.state)
		}
	}
}

func TestCodexHeuristic(t *testing.T) {
	h := codexHeuristic{}
	if got, ok := h.Classify("Waiting for user input"); !ok || got.State != agent.StateBlocked {
		t.Errorf("codex blocked hint failed: %+v, %v", got, ok)
	}
	if _, ok := h.Classify("Running tests…"); ok {
		t.Error("codex heuristic should ignore normal output")
	}
}

func TestGenericCommand(t *testing.T) {
	r := genericRunner{}
	name, _ := r.Command()
	if name != "" {
		t.Fatalf("generic command = %q, want empty", name)
	}
}
