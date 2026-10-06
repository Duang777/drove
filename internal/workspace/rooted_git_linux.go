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
	_ string,
	commonRoot *os.Root,
	arguments []string,
) (*exec.Cmd, func() error, error) {
	gitDirectory, err := gitRoot.Open(".")
	if err != nil {
		return nil, nil, fmt.Errorf(
			"workspace: open rooted private Git directory: %w",
			err,
		)
	}
	commonDirectory, err := commonRoot.Open(".")
	if err != nil {
		_ = gitDirectory.Close()
		return nil, nil, fmt.Errorf(
			"workspace: open rooted common Git directory: %w",
			err,
		)
	}
	command := exec.CommandContext(ctx, git)
	command.ExtraFiles = []*os.File{gitDirectory, commonDirectory}
	command.Env = boundGitEnvironmentWithCommon(
		command.Environ(),
		"/proc/self/fd/3",
		worktreePath,
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
		return errors.Join(
			commonDirectory.Close(),
			gitDirectory.Close(),
		)
	}
	return command, cleanup, nil
}

func rootedWorktreeGitCommand(
	ctx context.Context,
	git string,
	_ string,
	root *os.Root,
	_ string,
	gitRoot *os.Root,
	_ string,
	commonRoot *os.Root,
	arguments []string,
) (*exec.Cmd, func() error, error) {
	worktree, err := root.Open(".")
	if err != nil {
		return nil, nil, fmt.Errorf(
			"workspace: open rooted Git worktree: %w",
			err,
		)
	}
	gitDirectory, err := gitRoot.Open(".")
	if err != nil {
		_ = worktree.Close()
		return nil, nil, fmt.Errorf(
			"workspace: open rooted private Git directory: %w",
			err,
		)
	}
	commonDirectory, err := commonRoot.Open(".")
	if err != nil {
		_ = worktree.Close()
		_ = gitDirectory.Close()
		return nil, nil, fmt.Errorf(
			"workspace: open rooted common Git directory: %w",
			err,
		)
	}
	command := exec.CommandContext(ctx, git)
	command.ExtraFiles = []*os.File{worktree, gitDirectory, commonDirectory}
	command.Env = boundGitEnvironmentWithCommon(
		command.Environ(),
		"/proc/self/fd/4",
		"/proc/self/fd/3",
		"/proc/self/fd/5",
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
		return errors.Join(
			commonDirectory.Close(),
			gitDirectory.Close(),
			worktree.Close(),
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
	commonDirectory, err := commonRoot.Open(".")
	if err != nil {
		_ = worktree.Close()
		return nil, nil, fmt.Errorf(
			"workspace: open rooted common Git directory: %w",
			err,
		)
	}
	repository, err := repositoryRoot.Open(".")
	if err != nil {
		_ = commonDirectory.Close()
		_ = worktree.Close()
		return nil, nil, fmt.Errorf(
			"workspace: open rooted source repository: %w",
			err,
		)
	}
	command := exec.CommandContext(ctx, git)
	command.ExtraFiles = []*os.File{worktree, commonDirectory, repository}
	command.Env = boundGitEnvironment(
		command.Environ(),
		"/proc/self/fd/4",
		"/proc/self/fd/5",
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
		return errors.Join(
			verifyRealPathRoot(repositoryPath, repositoryRoot),
			verifyRealPathRoot(commonPath, commonRoot),
			repository.Close(),
			commonDirectory.Close(),
			worktree.Close(),
		)
	}
	return command, cleanup, nil
}
