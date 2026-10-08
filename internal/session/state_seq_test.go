package session

import (
	"encoding/json"
	"testing"
)

func TestStateSeqUsesCanonicalDecimalJSON(t *testing.T) {
	const sequence StateSeq = 9_007_199_254_740_993
	encoded, err := json.Marshal(sequence)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got, want := string(encoded), `"9007199254740993"`; got != want {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
	if got, want := sequence.String(), "9007199254740993"; got != want {
		t.Fatalf("String = %q, want %q", got, want)
	}

	var decoded StateSeq
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded != sequence {
		t.Fatalf("decoded = %d, want %d", decoded, sequence)
	}
}

func TestStateSeqRejectsNoncanonicalJSON(t *testing.T) {
	for _, raw := range []string{`42`, `"042"`, `"-1"`, `""`, `null`} {
		var decoded StateSeq
		if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
			t.Fatalf("Unmarshal accepted %s", raw)
		}
	}
}
