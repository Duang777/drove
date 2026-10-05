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
	return m.runRootedGitInputWithPolicy(
		ctx,
		path,
		root,
		"",
		nil,
		"",
		true,
		arguments...,
	)
}

func (m *Manager) runRootedGitInput(
	ctx context.Context,
	path string,
	root *os.Root,
	input string,
	arguments ...string,
) ([]byte, error) {
	return m.runRootedGitInputWithPolicy(
		ctx,
		path,
		root,
		"",
		nil,
		input,
		true,
		arguments...,
	)
}

func (m *Manager) runBoundGitInput(
	ctx context.Context,
	path string,
	root *os.Root,
	commonPath string,
	commonRoot *os.Root,
	input string,
	arguments ...string,
) ([]byte, error) {
	return m.runRootedGitInputWithPolicy(
		ctx,
		path,
		root,
		commonPath,
		commonRoot,
		input,
		false,
		arguments...,
	)
}

func (m *Manager) runRootedGitInputWithPolicy(
	ctx context.Context,
	path string,
	root *os.Root,
	commonPath string,
	commonRoot *os.Root,
	input string,
	verifyPath bool,
	arguments ...string,
) ([]byte, error) {
	if verifyPath {
		if err := verifyRealPathRoot(path, root); err != nil {
			return nil, err
		}
	}
	command, cleanup, err := rootedGitCommand(
		ctx,
		m.git,
		path,
		root,
		commonPath,
		commonRoot,
		arguments,
	)
	if err != nil {
		return nil, err
	}
	output, commandErr := runGitCommand(command, input)
	var verifyErr error
	if verifyPath {
		verifyErr = verifyRealPathRoot(path, root)
	}
	cleanupErr := cleanup()
	return output, errors.Join(commandErr, verifyErr, cleanupErr)
}

func runGitCommand(command *exec.Cmd, input string) ([]byte, error) {
	command.Env = append(command.Environ(), "LC_ALL=C", "LANG=C")
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

func rootedGitEnvironment(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if len(name) >= 4 && strings.EqualFold(name[:4], "GIT_") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func boundGitEnvironment(
	environment []string,
	gitDirectory string,
	worktree string,
) []string {
	return append(
		rootedGitEnvironment(environment),
		"GIT_DIR="+gitDirectory,
		"GIT_WORK_TREE="+worktree,
	)
}
