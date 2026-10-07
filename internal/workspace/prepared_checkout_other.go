//go:build aix || illumos || js || plan9 || solaris || wasip1

package workspace

import (
	"context"
	"errors"
	"os"
)

func checkoutPreparedWorktree(
	context.Context,
	repositoryCapability,
	string,
	*os.Root,
	string,
) error {
	return errors.New(
		"workspace: prepared worktree checkout is unsupported on this platform",
	)
}
