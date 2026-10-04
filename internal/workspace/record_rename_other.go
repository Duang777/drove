//go:build js || plan9 || wasip1

package workspace

import (
	"os"
	"path/filepath"
)

func renameRecordFile(
	directory *os.File,
	_ *os.File,
	temporaryName string,
	recordName string,
) error {
	return os.Rename(
		filepath.Join(directory.Name(), temporaryName),
		filepath.Join(directory.Name(), recordName),
	)
}
