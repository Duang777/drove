//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package workspace

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func unlinkOwnedRecordPath(
	directory *os.File,
	expected *os.File,
	name string,
) error {
	if err := verifyRecordPathIdentity(directory, expected, name); err != nil {
		return err
	}
	if err := unix.Unlinkat(int(directory.Fd()), name, 0); err != nil {
		return fmt.Errorf("remove owned record path %q: %w", name, err)
	}
	return nil
}
