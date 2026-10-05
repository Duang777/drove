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
) (string, bool, error) {
	return path, false, nil
}
