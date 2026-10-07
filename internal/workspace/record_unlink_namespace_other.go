//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package workspace

import "os"

func cleanupRecordDeletionNamespace(_ *os.Root) error {
	return nil
}
