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
	command := exec.CommandContext(ctx, git)
	command.ExtraFiles = []*os.File{directory}
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
