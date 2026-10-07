//go:build aix || dragonfly || freebsd || illumos || netbsd || openbsd || solaris

package workspace

import (
	"errors"
	"os"
)

func renameDirectoryNoReplace(
	_ *os.File,
	_ os.FileInfo,
	_ string,
	_ string,
	_ string,
) (bool, error) {
	return false, errors.New(
		"atomic no-replace directory rename is unsupported",
	)
}
