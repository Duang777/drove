//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package workspace

import (
	"context"
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
	_ string,
	root *os.Root,
	arguments []string,
) (*exec.Cmd, func() error, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, nil, fmt.Errorf(
			"workspace: open rooted Git directory: %w",
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
	return command, directory.Close, nil
}
