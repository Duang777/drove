//go:build js || plan9 || wasip1

package workspace

import (
	"errors"
	"os"
)

func renameRecordFile(
	_ *os.File,
	_ *os.File,
	_ string,
	_ string,
	_ bool,
) (bool, error) {
	return false, errors.New(
		"identity-preserving record rename is unsupported",
	)
}

func moveRecordFile(
	_ *os.File,
	_ *os.File,
	_ string,
	_ string,
) (bool, error) {
	return false, errors.New(
		"identity-preserving record move is unsupported",
	)
}

func syncRecordDirectory(directory *os.File) error {
	return directory.Sync()
}
