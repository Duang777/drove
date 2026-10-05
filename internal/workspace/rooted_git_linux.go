//go:build linux

package workspace

import (
	"context"
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
