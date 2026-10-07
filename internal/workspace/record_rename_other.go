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

func unlinkRecordPath(
	_ *os.File,
	_ *os.File,
	_ string,
) error {
	return errors.New(
		"identity-preserving record unlink is unsupported",
	)
}

func unlinkRecordPathAfterValidation(
	_ *os.File,
	_ *os.File,
	_ string,
	_ func(),
) error {
	return errors.New(
		"identity-preserving record unlink is unsupported",
	)
}

func openRecordDirectory(root *os.Root) (*os.File, error) {
	return root.Open(".")
}

func syncRecordDirectory(directory *os.File) error {
	return directory.Sync()
}
