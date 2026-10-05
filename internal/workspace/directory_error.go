//go:build !plan9

package workspace

import (
	"errors"
	"io/fs"
	"syscall"
)

func isDirectoryNotEmptyError(err error) bool {
	return errors.Is(err, syscall.ENOTEMPTY) ||
		errors.Is(err, fs.ErrExist)
}
