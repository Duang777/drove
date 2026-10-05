//go:build js || plan9 || wasip1

package workspace

import (
	"os"
	"os/exec"
)

func configureIncludeManifestCommand(
	_ *exec.Cmd,
	_ *os.File,
	path string,
) (string, bool, func() error, error) {
	return path, false, noCleanup, nil
}

func noCleanup() error {
	return nil
}
