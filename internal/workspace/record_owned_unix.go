//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package workspace

import (
	"os"
)

func unlinkOwnedRecordPath(
	directory *os.File,
	expected *os.File,
	name string,
) error {
	return unlinkRecordPath(directory, expected, name)
}
