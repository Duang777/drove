//go:build windows

package workspace

import (
	"context"
	"errors"
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

func rootedPrivateGitCommand(
	ctx context.Context,
	git string,
	worktreePath string,
	gitPath string,
	gitRoot *os.Root,
	arguments []string,
) (*exec.Cmd, func() error, error) {
	guard, err := openRepositoryGuard(gitPath, gitRoot)
	if err != nil {
		return nil, nil, err
	}
	command := exec.CommandContext(
		ctx,
		git,
		append(
			[]string{"-c", "core.hooksPath=NUL"},
			arguments...,
		)...,
	)
	command.Dir = gitPath
	command.Env = boundGitEnvironment(
		command.Environ(),
		gitPath,
		worktreePath,
	)
	return command, guard.Close, nil
}

func rootedWorktreeGitCommand(
	ctx context.Context,
	git string,
	path string,
	root *os.Root,
	gitPath string,
	gitRoot *os.Root,
	arguments []string,
) (*exec.Cmd, func() error, error) {
	command, closeWorktree, err := rootedGitCommand(
		ctx,
		git,
		path,
		root,
		gitPath,
		gitRoot,
		arguments,
	)
	if err != nil {
		return nil, nil, err
	}
	gitGuard, err := openRepositoryGuard(gitPath, gitRoot)
	if err != nil {
		return nil, nil, errors.Join(err, closeWorktree())
	}
	cleanup := func() error {
		return errors.Join(gitGuard.Close(), closeWorktree())
	}
	return command, cleanup, nil
}
