//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package workspace

import "os"

func recoverRecordRenameDebris(_ *os.Root) error {
	return nil
}
