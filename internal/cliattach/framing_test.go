package cliattach

import (
	"bytes"
	"errors"
	"testing"
)

func TestInputFramerKeepsIncompleteUTF8AcrossReads(t *testing.T) {
	var framer inputFramer
	runeBytes := []byte("界")
	first, detach, err := framer.Push(append([]byte("a"), runeBytes[:2]...))
	if err != nil {
		t.Fatalf("push first fragment: %v", err)
	}
	if detach || !bytes.Equal(first, []byte("a")) {
		t.Fatalf("first frame = %q, detach=%v", first, detach)
	}
	second, detach, err := framer.Push(append(runeBytes[2:], 'b'))
	if err != nil {
		t.Fatalf("push second fragment: %v", err)
	}
	if detach || !bytes.Equal(second, []byte("界b")) {
		t.Fatalf("second frame = %q, detach=%v", second, detach)
	}
	if err := framer.End(); err != nil {
		t.Fatalf("end complete stream: %v", err)
	}
}

func TestInputFramerDetachConsumesCtrlQLocally(t *testing.T) {
	var framer inputFramer
	payload, detach, err := framer.Push([]byte{'a', 0x03, localDetachByte, 'b'})
	if err != nil {
		t.Fatalf("push control bytes: %v", err)
	}
	if !detach {
		t.Fatal("Ctrl-Q did not request local detach")
	}
	if !bytes.Equal(payload, []byte{'a', 0x03}) {
		t.Fatalf("payload = %v, want Ctrl-C without Ctrl-Q suffix", payload)
	}
	if bytes.Contains(payload, []byte{localDetachByte}) {
		t.Fatal("payload retained Ctrl-Q")
	}
}

func TestInputFramerRejectsInvalidOrTruncatedUTF8(t *testing.T) {
	var invalid inputFramer
	if _, _, err := invalid.Push([]byte{0xff}); !errors.Is(err, errInvalidInputUTF8) {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}

	var truncated inputFramer
	if _, _, err := truncated.Push([]byte{0xe7, 0x95}); err != nil {
		t.Fatalf("push incomplete UTF-8: %v", err)
	}
	if err := truncated.End(); !errors.Is(err, errInvalidInputUTF8) {
		t.Fatalf("truncated UTF-8 error = %v", err)
	}
}
