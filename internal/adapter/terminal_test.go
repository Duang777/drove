package adapter

import (
	"testing"

	"github.com/Duang777/drove/internal/detect"
)

func TestEntryClassifyUsesSanitizedTerminalText(t *testing.T) {
	entry := NewRegistry().For("claude")
	raw := "Waiting \x1b[2K\x1b[1Gfor your input"

	if _, ok := entry.Heuristic.Classify(raw); ok {
		t.Fatal("raw ANSI-decorated text unexpectedly matched")
	}
	hint, ok := entry.Classify(raw)
	if !ok || hint.Kind != detect.KindHeuristicBlocked {
		t.Fatalf("sanitized hint = %+v, %t; want blocked", hint, ok)
	}
}

func TestGenericEntryClassifyHasNoHeuristic(t *testing.T) {
	if _, ok := NewRegistry().For("generic").Classify("\x1b[31mError\x1b[0m"); ok {
		t.Fatal("generic entry returned a heuristic hint")
	}
}
