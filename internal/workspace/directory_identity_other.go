//go:build aix || illumos || js || plan9 || solaris || wasip1

package workspace

import (
	"errors"
	"os"
)

func openedDirectoryIdentity(*os.Root) (string, error) {
	return "", errors.New(
		"workspace directory identity is unsupported",
	)
}
