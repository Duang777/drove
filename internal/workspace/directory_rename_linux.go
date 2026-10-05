//go:build linux

package workspace

import (
	"os"

	"golang.org/x/sys/unix"
)

func renameDirectoryNoReplace(
	directory *os.File,
	expected os.FileInfo,
	sourceName string,
	targetName string,
) (bool, error) {
	return renameBoundDirectoryNoReplace(
		directory,
		expected,
		sourceName,
		targetName,
		func(fd int, source string, target string) error {
			return unix.Renameat2(
				fd,
				source,
				fd,
				target,
				unix.RENAME_NOREPLACE,
			)
		},
	)
}
