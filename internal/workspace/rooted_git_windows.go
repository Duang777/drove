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
	commonPath string,
	commonRoot *os.Root,
	arguments []string,
) (*exec.Cmd, func() error, error) {
	gitGuard, err := openRepositoryGuard(gitPath, gitRoot)
	if err != nil {
		return nil, nil, err
	}
	commonGuard, err := openRepositoryGuard(commonPath, commonRoot)
	if err != nil {
		return nil, nil, errors.Join(err, gitGuard.Close())
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
	command.Env = boundGitEnvironmentWithCommon(
		command.Environ(),
		gitPath,
		worktreePath,
		commonPath,
	)
	cleanup := func() error {
		return errors.Join(
			commonGuard.Close(),
			gitGuard.Close(),
		)
	}
	return command, cleanup, nil
}

func rootedWorktreeGitCommand(
	ctx context.Context,
	git string,
	path string,
	root *os.Root,
	gitPath string,
	gitRoot *os.Root,
	commonPath string,
	commonRoot *os.Root,
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
	commonGuard, err := openRepositoryGuard(commonPath, commonRoot)
	if err != nil {
		return nil, nil, errors.Join(
			err,
			gitGuard.Close(),
			closeWorktree(),
		)
	}
	command.Env = boundGitEnvironmentWithCommon(
		command.Environ(),
		gitPath,
		path,
		commonPath,
	)
	cleanup := func() error {
		return errors.Join(
			commonGuard.Close(),
			gitGuard.Close(),
			closeWorktree(),
		)
	}
	return command, cleanup, nil
}

func rootedPreparedWorktreeGitCommand(
	ctx context.Context,
	git string,
	repositoryPath string,
	repositoryRoot *os.Root,
	commonPath string,
	commonRoot *os.Root,
	worktreePath string,
	worktreeRoot *os.Root,
	arguments []string,
) (*exec.Cmd, func() error, error) {
	worktreeGuard, err := openRepositoryGuard(worktreePath, worktreeRoot)
	if err != nil {
		return nil, nil, err
	}
	commonGuard, err := openRepositoryGuard(commonPath, commonRoot)
	if err != nil {
		return nil, nil, errors.Join(err, worktreeGuard.Close())
	}
	repositoryGuard, err := openRepositoryGuard(
		repositoryPath,
		repositoryRoot,
	)
	if err != nil {
		return nil, nil, errors.Join(
			err,
			commonGuard.Close(),
			worktreeGuard.Close(),
		)
	}
	command := exec.CommandContext(
		ctx,
		git,
		append(
			[]string{"-c", "core.hooksPath=NUL"},
			arguments...,
		)...,
	)
	command.Dir = worktreePath
	command.Env = boundGitEnvironment(
		command.Environ(),
		commonPath,
		repositoryPath,
	)
	cleanup := func() error {
		return errors.Join(
			repositoryGuard.Close(),
			commonGuard.Close(),
			worktreeGuard.Close(),
		)
	}
	return command, cleanup, nil
}
