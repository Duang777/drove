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
	commonPath string,
	_ *os.Root,
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
	command.Env = rootedGitEnvironment(command.Environ())
	if commonPath != "" {
		command.Dir = commonPath
		command.Env = boundGitEnvironment(
			command.Env,
			commonPath,
			path,
		)
	}
	return command, guard.Close, nil
}
