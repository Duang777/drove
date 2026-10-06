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
	_ string,
	_ *os.Root,
	_ []string,
) (*exec.Cmd, func() error, error) {
	return nil, nil, errors.New(
		"workspace: rooted Git commands are unsupported on this platform",
	)
}

func rootedPrivateGitCommand(
	context.Context,
	string,
	string,
	string,
	*os.Root,
	[]string,
) (*exec.Cmd, func() error, error) {
	return nil, nil, errors.New(
		"workspace: rooted private Git execution is unsupported on this platform",
	)
}

func rootedWorktreeGitCommand(
	_ context.Context,
	_ string,
	_ string,
	_ *os.Root,
	_ string,
	_ *os.Root,
	_ string,
	_ *os.Root,
	_ []string,
) (*exec.Cmd, func() error, error) {
	return nil, nil, errors.New(
		"workspace: rooted worktree Git commands are unsupported on this platform",
	)
}

func rootedPreparedWorktreeGitCommand(
	_ context.Context,
	_ string,
	_ string,
	_ *os.Root,
	_ string,
	_ *os.Root,
	_ string,
	_ *os.Root,
	_ []string,
) (*exec.Cmd, func() error, error) {
	return nil, nil, errors.New(
		"workspace: rooted prepared worktree Git commands are unsupported on this platform",
	)
}
