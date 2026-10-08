package session

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// StateSeq identifies the committed event that established the current state.
type StateSeq uint64

// String returns the canonical decimal representation.
func (s StateSeq) String() string {
	return strconv.FormatUint(uint64(s), 10)
}

// MarshalJSON encodes state sequences as decimal strings for JavaScript clients.
func (s StateSeq) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(s.String())), nil
}

// UnmarshalJSON decodes one canonical uint64 decimal string.
func (s *StateSeq) UnmarshalJSON(data []byte) error {
	var encoded string
	if err := json.Unmarshal(data, &encoded); err != nil {
		return fmt.Errorf("session: decode state sequence: %w", err)
	}
	parsed, err := strconv.ParseUint(encoded, 10, 64)
	if err != nil || strconv.FormatUint(parsed, 10) != encoded {
		return fmt.Errorf(
			"session: state sequence %q is not a canonical uint64",
			encoded,
		)
	}
	*s = StateSeq(parsed)
	return nil
}
