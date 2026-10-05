//go:build windows

package workspace

import (
	"context"
	"os"
	"os/exec"
)

func rootedGitCommand(
	ctx context.Context,
	git string,
	path string,
	root *os.Root,
	arguments []string,
) (*exec.Cmd, func() error, error) {
	guard, err := openRepositoryGuard(path, root)
	if err != nil {
		return nil, nil, err
	}
	command := exec.CommandContext(
		ctx,
		git,
		append(
			[]string{"-c", "core.hooksPath=NUL", "-C", path},
			arguments...,
		)...,
	)
	return command, guard.Close, nil
}
