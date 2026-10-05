//go:build linux

package workspace

import (
	"os"

	"golang.org/x/sys/unix"
)

func renameDirectoryNoReplace(
	directory *os.File,
	_ os.FileInfo,
	sourceName string,
	targetName string,
) (bool, error) {
	fd := int(directory.Fd())
	if err := unix.Renameat2(
		fd,
		sourceName,
		fd,
		targetName,
		unix.RENAME_NOREPLACE,
	); err != nil {
		return false, err
	}
	return true, nil
}
