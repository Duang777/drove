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
	commonPath string,
	commonRoot *os.Root,
	arguments []string,
) (*exec.Cmd, func() error, error) {
	if err := verifyRealPathRoot(commonPath, commonRoot); err != nil {
		return nil, nil, fmt.Errorf(
			"workspace: verify rooted common Git directory: %w",
			err,
		)
	}
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
	cleanup := func() error {
		return errors.Join(
			verifyRealPathRoot(commonPath, commonRoot),
			directory.Close(),
		)
	}
	return command, cleanup, nil
}

func rootedWorktreeGitCommand(
	ctx context.Context,
	git string,
	_ string,
	root *os.Root,
	gitPath string,
	gitRoot *os.Root,
	commonPath string,
	commonRoot *os.Root,
	arguments []string,
) (*exec.Cmd, func() error, error) {
	if err := verifyRealPathRoot(gitPath, gitRoot); err != nil {
		return nil, nil, fmt.Errorf(
			"workspace: verify rooted private Git directory: %w",
			err,
		)
	}
	if err := verifyRealPathRoot(commonPath, commonRoot); err != nil {
		return nil, nil, fmt.Errorf(
			"workspace: verify rooted common Git directory: %w",
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
			verifyRealPathRoot(commonPath, commonRoot),
			directory.Close(),
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
	if err := verifyRealPathRoot(repositoryPath, repositoryRoot); err != nil {
		return nil, nil, fmt.Errorf(
			"workspace: verify rooted source repository: %w",
			err,
		)
	}
	if err := verifyRealPathRoot(worktreePath, worktreeRoot); err != nil {
		return nil, nil, fmt.Errorf(
			"workspace: verify prepared worktree target: %w",
			err,
		)
	}
	stageName, err := bsdPreparedWorktreeStageName(worktreePath)
	if err != nil {
		return nil, nil, err
	}
	if err := validateBSDPreparedWorktreeNamesAvailable(
		commonRoot,
		stageName,
	); err != nil {
		return nil, nil, err
	}
	if len(arguments) < 2 || arguments[len(arguments)-2] != "." {
		return nil, nil, errors.New(
			"workspace: prepared worktree command has an unexpected target",
		)
	}
	arguments = append([]string(nil), arguments...)
	arguments[len(arguments)-2] = "./" + stageName
	directory, err := commonRoot.Open(".")
	if err != nil {
		return nil, nil, fmt.Errorf(
			"workspace: open rooted common Git directory: %w",
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
		".",
		".",
	)
	cleanup := func() error {
		return errors.Join(
			verifyRealPathRoot(repositoryPath, repositoryRoot),
			verifyRealPathRoot(commonPath, commonRoot),
			verifyRealPathRoot(worktreePath, worktreeRoot),
			commonGuard.Close(),
			directory.Close(),
		)
	}
	return command, cleanup, nil
}

func rootedRepairWorktreeGitCommand(
	ctx context.Context,
	git string,
	worktreePath string,
	_ *os.Root,
	gitPath string,
	gitRoot *os.Root,
	commonPath string,
	commonRoot *os.Root,
) (*exec.Cmd, func() error, error) {
	return rootedPrivateGitCommand(
		ctx,
		git,
		worktreePath,
		gitPath,
		gitRoot,
		commonPath,
		commonRoot,
		[]string{"worktree", "repair", worktreePath},
	)
}
