package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func (m *Manager) runRootedGit(
	ctx context.Context,
	path string,
	root *os.Root,
	arguments ...string,
) ([]byte, error) {
	return m.runRootedGitInput(ctx, path, root, "", arguments...)
}

func (m *Manager) runRootedGitInput(
	ctx context.Context,
	path string,
	root *os.Root,
	input string,
	arguments ...string,
) ([]byte, error) {
	if err := verifyRealPathRoot(path, root); err != nil {
		return nil, err
	}
	command, cleanup, err := rootedGitCommand(
		ctx,
		m.git,
		path,
		root,
		arguments,
	)
	if err != nil {
		return nil, err
	}
	output, commandErr := runGitCommand(command, input)
	verifyErr := verifyRealPathRoot(path, root)
	cleanupErr := cleanup()
	return output, errors.Join(commandErr, verifyErr, cleanupErr)
}

func runGitCommand(command *exec.Cmd, input string) ([]byte, error) {
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	if input != "" {
		command.Stdin = strings.NewReader(input)
	}
	output, err := command.Output()
	if err == nil {
		return output, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		detail := strings.TrimSpace(string(exitErr.Stderr))
		if len(detail) > 4096 {
			detail = detail[:4096]
		}
		if detail != "" {
			return nil, fmt.Errorf("%s: %w", detail, err)
		}
	}
	return nil, err
}
