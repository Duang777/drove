//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package workspace

import (
	"errors"
	"os"
)

func unlinkOwnedRecordPath(
	_ *os.File,
	_ *os.File,
	_ string,
) error {
	return errors.New(
		"workspace: owned record removal is unsupported on this platform",
	)
}
