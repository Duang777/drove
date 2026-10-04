package cliattach

import (
	"bytes"
	"errors"
	"unicode/utf8"
)

const (
	stdinChunkBytes = 32 * 1024
	localDetachByte = byte(0x11)
)

var errInvalidInputUTF8 = errors.New("cliattach: terminal input is not valid UTF-8")

type inputFramer struct {
	pending []byte
}

func (f *inputFramer) Push(chunk []byte) ([]byte, bool, error) {
	combined := make([]byte, 0, len(f.pending)+len(chunk))
	combined = append(combined, f.pending...)
	combined = append(combined, chunk...)
	f.pending = nil

	detach := false
	if index := bytes.IndexByte(combined, localDetachByte); index >= 0 {
		combined = combined[:index]
		detach = true
	}
	complete, err := completeUTF8Prefix(combined)
	if err != nil {
		return nil, false, err
	}
	payload := append([]byte(nil), combined[:complete]...)
	if !detach {
		f.pending = append(f.pending, combined[complete:]...)
	}
	return payload, detach, nil
}

func (f *inputFramer) End() error {
	if len(f.pending) != 0 {
		return errInvalidInputUTF8
	}
	return nil
}

func completeUTF8Prefix(data []byte) (int, error) {
	index := 0
	for index < len(data) {
		if data[index] < utf8.RuneSelf {
			index++
			continue
		}
		if !utf8.FullRune(data[index:]) {
			return index, nil
		}
		r, size := utf8.DecodeRune(data[index:])
		if r == utf8.RuneError && size == 1 {
			return 0, errInvalidInputUTF8
		}
		index += size
	}
	return index, nil
}
