//go:build aix || darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || solaris

package workspace

import (
	"os"

	"golang.org/x/sys/unix"
)

func renameRecordFile(
	directory *os.File,
	_ *os.File,
	temporaryName string,
	recordName string,
) error {
	fd := int(directory.Fd())
	return unix.Renameat(fd, temporaryName, fd, recordName)
}
