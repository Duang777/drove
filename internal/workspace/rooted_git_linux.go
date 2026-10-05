//go:build linux

package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

func rootedGitCommand(
	ctx context.Context,
	git string,
	path string,
	root *os.Root,
	_ string,
	commonRoot *os.Root,
	arguments []string,
) (*exec.Cmd, func() error, error) {
	commandRoot := root
	description := "rooted Git directory"
	if commonRoot != nil {
		commandRoot = commonRoot
		description = "rooted common Git directory"
	}
	directory, err := commandRoot.Open(".")
	if err != nil {
		return nil, nil, fmt.Errorf(
			"workspace: open %s: %w",
			description,
			err,
		)
	}
	command := exec.CommandContext(ctx, git)
	command.ExtraFiles = []*os.File{directory}
	command.Env = rootedGitEnvironment(command.Environ())
	if commonRoot != nil {
		command.Env = boundGitEnvironment(
			command.Env,
			".",
			path,
		)
	}
	command.Args = append(
		command.Args,
		"-c",
		"core.hooksPath=/dev/null",
		"-C",
		"/proc/self/fd/3",
	)
	command.Args = append(command.Args, arguments...)
	return command, directory.Close, nil
}

func rootedPrivateGitCommand(
	ctx context.Context,
	git string,
	worktreePath string,
	_ string,
	gitRoot *os.Root,
	arguments []string,
) (*exec.Cmd, func() error, error) {
	gitDirectory, err := gitRoot.Open(".")
	if err != nil {
		return nil, nil, fmt.Errorf(
			"workspace: open rooted private Git directory: %w",
			err,
		)
	}
	command := exec.CommandContext(ctx, git)
	command.ExtraFiles = []*os.File{gitDirectory}
	command.Env = boundGitEnvironment(
		command.Environ(),
		".",
		worktreePath,
	)
	command.Args = append(
		command.Args,
		"-c",
		"core.hooksPath=/dev/null",
		"-C",
		"/proc/self/fd/3",
	)
	command.Args = append(command.Args, arguments...)
	return command, gitDirectory.Close, nil
}

func rootedWorktreeGitCommand(
	ctx context.Context,
	git string,
	_ string,
	root *os.Root,
	_ string,
	gitRoot *os.Root,
	arguments []string,
) (*exec.Cmd, func() error, error) {
	gitDirectory, err := gitRoot.Open(".")
	if err != nil {
		return nil, nil, fmt.Errorf(
			"workspace: open rooted private Git directory: %w",
			err,
		)
	}
	worktree, err := root.Open(".")
	if err != nil {
		_ = gitDirectory.Close()
		return nil, nil, fmt.Errorf(
			"workspace: open rooted Git worktree: %w",
			err,
		)
	}
	command := exec.CommandContext(ctx, git)
	command.ExtraFiles = []*os.File{gitDirectory, worktree}
	command.Env = boundGitEnvironment(
		command.Environ(),
		".",
		"/proc/self/fd/4",
	)
	command.Args = append(
		command.Args,
		"-c",
		"core.hooksPath=/dev/null",
		"-C",
		"/proc/self/fd/3",
	)
	command.Args = append(command.Args, arguments...)
	cleanup := func() error {
		return errors.Join(worktree.Close(), gitDirectory.Close())
	}
	return command, cleanup, nil
}

func rootedPreparedWorktreeGitCommand(
	ctx context.Context,
	git string,
	repositoryPath string,
	commonPath string,
	commonRoot *os.Root,
	_ string,
	worktreeRoot *os.Root,
	arguments []string,
) (*exec.Cmd, func() error, error) {
	worktree, err := worktreeRoot.Open(".")
	if err != nil {
		return nil, nil, fmt.Errorf(
			"workspace: open prepared worktree target: %w",
			err,
		)
	}
	commonGuard, err := openRepositoryGuard(commonPath, commonRoot)
	if err != nil {
		_ = worktree.Close()
		return nil, nil, err
	}
	command := exec.CommandContext(ctx, git)
	command.ExtraFiles = []*os.File{worktree}
	command.Env = boundGitEnvironment(
		command.Environ(),
		commonPath,
		repositoryPath,
	)
	command.Args = append(
		command.Args,
		"-c",
		"core.hooksPath=/dev/null",
		"-C",
		"/proc/self/fd/3",
	)
	command.Args = append(command.Args, arguments...)
	cleanup := func() error {
		return errors.Join(commonGuard.Close(), worktree.Close())
	}
	return command, cleanup, nil
}
