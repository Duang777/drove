//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

const rootedGitHelperArgument = "__drove_rooted_git__"

func init() {
	if len(os.Args) < 3 || os.Args[1] != rootedGitHelperArgument {
		return
	}
	if err := syscall.Fchdir(3); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "workspace: enter rooted Git directory: %v\n", err)
		os.Exit(126)
	}
	if err := syscall.Close(3); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "workspace: close rooted Git directory: %v\n", err)
		os.Exit(126)
	}
	if err := syscall.Exec(os.Args[2], os.Args[2:], os.Environ()); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "workspace: execute rooted Git: %v\n", err)
		os.Exit(126)
	}
}

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
	executable, err := os.Executable()
	if err != nil {
		_ = directory.Close()
		return nil, nil, fmt.Errorf(
			"workspace: locate rooted Git helper: %w",
			err,
		)
	}
	git, err = exec.LookPath(git)
	if err != nil {
		_ = directory.Close()
		return nil, nil, err
	}
	helperArguments := []string{
		rootedGitHelperArgument,
		git,
		"-c",
		"core.hooksPath=/dev/null",
	}
	helperArguments = append(helperArguments, arguments...)
	command := exec.CommandContext(ctx, executable, helperArguments...)
	command.ExtraFiles = []*os.File{directory}
	command.Env = rootedGitEnvironment(command.Environ())
	if commonRoot != nil {
		command.Env = boundGitEnvironment(
			command.Env,
			".",
			path,
		)
	}
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
	directory, err := gitRoot.Open(".")
	if err != nil {
		return nil, nil, fmt.Errorf(
			"workspace: open rooted private Git directory: %w",
			err,
		)
	}
	executable, err := os.Executable()
	if err != nil {
		_ = directory.Close()
		return nil, nil, fmt.Errorf(
			"workspace: locate rooted Git helper: %w",
			err,
		)
	}
	git, err = exec.LookPath(git)
	if err != nil {
		_ = directory.Close()
		return nil, nil, err
	}
	helperArguments := []string{
		rootedGitHelperArgument,
		git,
		"-c",
		"core.hooksPath=/dev/null",
	}
	helperArguments = append(helperArguments, arguments...)
	command := exec.CommandContext(ctx, executable, helperArguments...)
	command.ExtraFiles = []*os.File{directory}
	command.Env = boundGitEnvironment(
		command.Environ(),
		".",
		worktreePath,
	)
	return command, directory.Close, nil
}

func rootedWorktreeGitCommand(
	ctx context.Context,
	git string,
	_ string,
	root *os.Root,
	gitPath string,
	gitRoot *os.Root,
	arguments []string,
) (*exec.Cmd, func() error, error) {
	if err := verifyRealPathRoot(gitPath, gitRoot); err != nil {
		return nil, nil, fmt.Errorf(
			"workspace: verify rooted private Git directory: %w",
			err,
		)
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, nil, fmt.Errorf(
			"workspace: open rooted Git worktree: %w",
			err,
		)
	}
	executable, err := os.Executable()
	if err != nil {
		_ = directory.Close()
		return nil, nil, fmt.Errorf(
			"workspace: locate rooted Git helper: %w",
			err,
		)
	}
	git, err = exec.LookPath(git)
	if err != nil {
		_ = directory.Close()
		return nil, nil, err
	}
	helperArguments := []string{
		rootedGitHelperArgument,
		git,
		"-c",
		"core.hooksPath=/dev/null",
	}
	helperArguments = append(helperArguments, arguments...)
	command := exec.CommandContext(ctx, executable, helperArguments...)
	command.ExtraFiles = []*os.File{directory}
	command.Env = boundGitEnvironment(
		command.Environ(),
		gitPath,
		".",
	)
	cleanup := func() error {
		return errors.Join(
			verifyRealPathRoot(gitPath, gitRoot),
			directory.Close(),
		)
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
	directory, err := worktreeRoot.Open(".")
	if err != nil {
		return nil, nil, fmt.Errorf(
			"workspace: open prepared worktree target: %w",
			err,
		)
	}
	commonGuard, err := openRepositoryGuard(commonPath, commonRoot)
	if err != nil {
		_ = directory.Close()
		return nil, nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		_ = commonGuard.Close()
		_ = directory.Close()
		return nil, nil, fmt.Errorf(
			"workspace: locate rooted Git helper: %w",
			err,
		)
	}
	git, err = exec.LookPath(git)
	if err != nil {
		_ = commonGuard.Close()
		_ = directory.Close()
		return nil, nil, err
	}
	helperArguments := []string{
		rootedGitHelperArgument,
		git,
		"-c",
		"core.hooksPath=/dev/null",
	}
	helperArguments = append(helperArguments, arguments...)
	command := exec.CommandContext(ctx, executable, helperArguments...)
	command.ExtraFiles = []*os.File{directory}
	command.Env = boundGitEnvironment(
		command.Environ(),
		commonPath,
		repositoryPath,
	)
	cleanup := func() error {
		return errors.Join(commonGuard.Close(), directory.Close())
	}
	return command, cleanup, nil
}
