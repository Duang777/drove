//go:build aix || illumos || solaris

package workspace

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func renameRecordFile(
	directory *os.File,
	_ *os.File,
	temporaryName string,
	recordName string,
	replace bool,
) (bool, error) {
	if !replace {
		return false, errors.New(
			"atomic no-replace record installation is unsupported",
		)
	}
	fd := int(directory.Fd())
	if err := unix.Renameat(fd, temporaryName, fd, recordName); err != nil {
		return false, err
	}
	return true, nil
}

func moveRecordFile(
	directory *os.File,
	_ *os.File,
	sourceName string,
	targetName string,
) (bool, error) {
	fd := int(directory.Fd())
	if err := unix.Renameat(fd, sourceName, fd, targetName); err != nil {
		return false, err
	}
	return true, nil
}

func syncRecordDirectory(directory *os.File) error {
	return directory.Sync()
}
