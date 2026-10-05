//go:build plan9

package workspace

import (
	"errors"
	"io/fs"
)

func isDirectoryNotEmptyError(err error) bool {
	return errors.Is(err, fs.ErrExist)
}
