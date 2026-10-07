//go:build windows

package workspace

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func unlinkOwnedRecordPath(
	directory *os.File,
	expected *os.File,
	name string,
) error {
	return unlinkRecordPath(directory, expected, name)
}

func syncOwnedRecordDirectoryAfterUnlink(directory *os.File) error {
	err := syncRecordDirectory(directory)
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return nil
	}
	return err
}
