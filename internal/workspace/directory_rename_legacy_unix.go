//go:build aix || dragonfly || freebsd || illumos || netbsd || openbsd || solaris

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
	if err := unix.Renameat(fd, sourceName, fd, targetName); err != nil {
		return false, err
	}
	return true, nil
}
