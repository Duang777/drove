//go:build aix || illumos || js || plan9 || solaris || wasip1

package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

func rootedGitCommand(
	_ context.Context,
	_ string,
	_ string,
	_ *os.Root,
	_ []string,
) (*exec.Cmd, func() error, error) {
	return nil, nil, errors.New(
		"workspace: rooted Git commands are unsupported on this platform",
	)
}
