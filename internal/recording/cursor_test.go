package recording

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestCursorParsesCanonicalDecimalStrings(t *testing.T) {
	cursor, err := ParseCursor("18446744073709551615", "9007199254740993")
	if err != nil {
		t.Fatalf("parse cursor: %v", err)
	}
	if cursor.Seq != Seq(math.MaxUint64) ||
		cursor.NextOffset != OutputOffset(9007199254740993) {
		t.Fatalf("cursor = %+v", cursor)
	}

	encoded, err := json.Marshal(cursor)
	if err != nil {
		t.Fatalf("marshal cursor: %v", err)
	}
	const want = `{"seq":"18446744073709551615","next_offset":"9007199254740993"}`
	if string(encoded) != want {
		t.Fatalf("encoded cursor = %s, want %s", encoded, want)
	}

	var decoded Cursor
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal cursor: %v", err)
	}
	if decoded != cursor {
		t.Fatalf("decoded cursor = %+v, want %+v", decoded, cursor)
	}
}

func TestCanonicalDecimalRejectsAmbiguousForms(t *testing.T) {
	for _, value := range []string{"", "00", "01", "+1", "-1", " 1", "1 ", "1.0", "1e2"} {
		t.Run(value, func(t *testing.T) {
			if _, err := ParseSeq(value); err == nil {
				t.Fatalf("ParseSeq(%q) succeeded", value)
			}
			if _, err := ParseOutputOffset(value); err == nil {
				t.Fatalf("ParseOutputOffset(%q) succeeded", value)
			}
		})
	}
	if _, err := ParseSeq("18446744073709551616"); err == nil {
		t.Fatal("ParseSeq accepted uint64 overflow")
	}

	var sequence Seq
	if err := json.Unmarshal([]byte(`9007199254740993`), &sequence); err == nil {
		t.Fatal("sequence accepted a JSON number")
	}
	if err := json.Unmarshal([]byte(`"01"`), &sequence); err == nil {
		t.Fatal("sequence accepted non-canonical decimal")
	}
	var cursor Cursor
	if err := json.Unmarshal(
		[]byte(`{"seq":"0","next_offset":"1"}`),
		&cursor,
	); err == nil {
		t.Fatal("cursor JSON accepted an impossible origin")
	}
}

func TestCursorRejectsImpossibleOrigin(t *testing.T) {
	if Origin.Seq != 0 || Origin.NextOffset != 0 {
		t.Fatalf("origin = %+v", Origin)
	}
	if _, err := NewCursor(0, 1); err == nil {
		t.Fatal("cursor accepted output after the zero sequence")
	}
	if _, err := NewCursor(1, 0); err != nil {
		t.Fatalf("cursor rejected a pre-output event: %v", err)
	}
}

func TestSelectorRequiresExactlyOneValidatedValue(t *testing.T) {
	seq := Seq(7)
	offset := OutputOffset(12)
	cursor := Cursor{Seq: 8, NextOffset: 12}
	at := time.Date(2026, time.October, 4, 10, 0, 0, 0, time.FixedZone("test", 3600))

	tests := []struct {
		name  string
		input SelectorInput
		kind  SelectorKind
	}{
		{name: "cursor", input: SelectorInput{Cursor: &cursor}, kind: SelectorCursor},
		{name: "sequence", input: SelectorInput{Seq: &seq}, kind: SelectorSequence},
		{name: "time", input: SelectorInput{At: &at}, kind: SelectorTime},
		{name: "offset", input: SelectorInput{Offset: &offset}, kind: SelectorOutputOffset},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selector, err := NewSelector(test.input)
			if err != nil {
				t.Fatalf("new selector: %v", err)
			}
			if selector.Kind() != test.kind {
				t.Fatalf("selector kind = %v, want %v", selector.Kind(), test.kind)
			}
			if selectedAt, ok := selector.Time(); ok && selectedAt.Location() != time.UTC {
				t.Fatalf("selector time location = %v, want UTC", selectedAt.Location())
			}
		})
	}

	if _, err := NewSelector(SelectorInput{}); err == nil {
		t.Fatal("selector accepted no value")
	}
	if _, err := NewSelector(SelectorInput{Seq: &seq, Offset: &offset}); err == nil {
		t.Fatal("selector accepted mixed values")
	}
	invalidCursor := Cursor{NextOffset: 1}
	if _, err := NewSelector(SelectorInput{Cursor: &invalidCursor}); err == nil {
		t.Fatal("selector accepted an impossible cursor")
	}
	zeroTime := time.Time{}
	if _, err := NewSelector(SelectorInput{At: &zeroTime}); err == nil {
		t.Fatal("selector accepted a zero timestamp")
	}
}
