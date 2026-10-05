package workspace

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type repositoryCapability struct {
	manager *Manager
	path    string
	root    *os.Root
}

func pathRepositoryCapability(
	manager *Manager,
	path string,
) repositoryCapability {
	return repositoryCapability{
		manager: manager,
		path:    path,
	}
}

func rootedRepositoryCapability(
	manager *Manager,
	path string,
	root *os.Root,
) repositoryCapability {
	return repositoryCapability{
		manager: manager,
		path:    path,
		root:    root,
	}
}

func (r repositoryCapability) run(
	ctx context.Context,
	input string,
	arguments ...string,
) ([]byte, error) {
	if r.root != nil {
		return r.manager.runBoundGitInput(
			ctx,
			r.path,
			r.root,
			input,
			arguments...,
		)
	}
	return r.manager.runInput(
		ctx,
		input,
		append([]string{"-C", r.path}, arguments...)...,
	)
}

func (r repositoryCapability) command(
	ctx context.Context,
	arguments ...string,
) (*exec.Cmd, func() error, error) {
	if r.root != nil {
		return rootedGitCommand(
			ctx,
			r.manager.git,
			r.path,
			r.root,
			arguments,
		)
	}
	command := exec.CommandContext(
		ctx,
		r.manager.git,
		append([]string{"-C", r.path}, arguments...)...,
	)
	return command, func() error { return nil }, nil
}

func (r repositoryCapability) refOID(
	ctx context.Context,
	ref string,
) (string, bool, error) {
	output, err := r.run(
		ctx,
		"",
		"rev-parse",
		"--verify",
		"--quiet",
		ref,
	)
	switch {
	case err == nil:
		oid := strings.TrimSpace(string(output))
		if oid == "" || strings.ContainsAny(oid, " \t\r\n") {
			return "", false, fmt.Errorf(
				"workspace: inspect ref %q returned an invalid object ID",
				ref,
			)
		}
		return oid, true, nil
	case isExitCode(err, 1):
		return "", false, nil
	default:
		return "", false, fmt.Errorf("workspace: inspect ref %q: %w", ref, err)
	}
}

func (r repositoryCapability) registeredWorktrees(
	ctx context.Context,
) ([]registeredWorktree, error) {
	output, err := r.run(
		ctx,
		"",
		"worktree",
		"list",
		"--porcelain",
		"-z",
	)
	if err != nil {
		return nil, fmt.Errorf("workspace: list Git worktrees: %w", err)
	}
	return parseRegisteredWorktrees(output)
}

func (r repositoryCapability) worktreeRegistered(
	ctx context.Context,
	path string,
) (bool, error) {
	_, registered, err := r.worktreeRegistration(ctx, path)
	return registered, err
}

func (r repositoryCapability) worktreeRegistration(
	ctx context.Context,
	path string,
) (registeredWorktree, bool, error) {
	worktrees, err := r.registeredWorktrees(ctx)
	if err != nil {
		return registeredWorktree{}, false, err
	}
	path = filepath.Clean(path)
	for _, registered := range worktrees {
		if registered.path == path {
			return registered, true, nil
		}
	}
	return registeredWorktree{}, false, nil
}

func (r repositoryCapability) pruneWorktrees(
	ctx context.Context,
) error {
	if _, err := r.run(
		ctx,
		"",
		"worktree",
		"prune",
		"--expire",
		"now",
	); err != nil {
		return fmt.Errorf("workspace: prune discarded worktree: %w", err)
	}
	return nil
}

func (r repositoryCapability) preparedWorktreeGitDirectory(
	ctx context.Context,
	agentID string,
) (string, error) {
	output, err := r.run(
		ctx,
		"",
		"rev-parse",
		"--path-format=absolute",
		"--git-path",
		"worktrees/"+agentID,
	)
	if err != nil {
		return "", fmt.Errorf(
			"workspace: resolve prepared worktree Git directory: %w",
			err,
		)
	}
	path := strings.TrimSpace(string(output))
	if !filepath.IsAbs(path) {
		return "", errors.New(
			"workspace: prepared worktree Git directory is not absolute",
		)
	}
	path, err = resolvePath(path)
	if err != nil {
		return "", fmt.Errorf(
			"workspace: resolve prepared worktree Git directory: %w",
			err,
		)
	}
	return path, nil
}

func (r repositoryCapability) verifyPreparedWorktree(
	ctx context.Context,
	target Workspace,
	gitDirectory string,
	registered registeredWorktree,
) error {
	if registered.path != target.Path ||
		registered.detached ||
		registered.branch != "refs/heads/"+target.Branch ||
		registered.head == "" ||
		strings.ContainsAny(registered.head, " \t\r\n") {
		return errors.New(
			"workspace: prepared worktree registration does not match intent",
		)
	}
	head, err := r.run(
		ctx,
		"",
		"--git-dir="+gitDirectory,
		"rev-parse",
		"--verify",
		"HEAD",
	)
	if err != nil {
		return fmt.Errorf(
			"workspace: inspect prepared worktree HEAD: %w",
			err,
		)
	}
	if strings.TrimSpace(string(head)) != registered.head {
		return errors.New(
			"workspace: prepared worktree HEAD does not match registration",
		)
	}
	branch, err := r.run(
		ctx,
		"",
		"--git-dir="+gitDirectory,
		"symbolic-ref",
		"--quiet",
		"HEAD",
	)
	if err != nil {
		return fmt.Errorf(
			"workspace: inspect prepared worktree branch: %w",
			err,
		)
	}
	if strings.TrimSpace(string(branch)) != registered.branch {
		return errors.New(
			"workspace: prepared worktree branch does not match registration",
		)
	}
	return nil
}

func (r repositoryCapability) refLogSubject(
	ctx context.Context,
	ref string,
) (string, bool, error) {
	output, err := r.run(
		ctx,
		"",
		"reflog",
		"show",
		"--format=%gs",
		"-n",
		"1",
		ref,
	)
	if err != nil {
		return "", false, fmt.Errorf(
			"workspace: inspect reflog for %q: %w",
			ref,
			err,
		)
	}
	subject := strings.TrimSpace(string(output))
	if subject == "" {
		return "", false, nil
	}
	if strings.ContainsAny(subject, "\r\n") {
		return "", false, fmt.Errorf(
			"workspace: reflog for %q returned multiple entries",
			ref,
		)
	}
	return subject, true, nil
}

func (r repositoryCapability) cleanupOwnedBranch(
	ctx context.Context,
	branch string,
	operationID string,
) error {
	if operationID == "" {
		return nil
	}
	if err := r.manager.validateBranch(ctx, branch); err != nil {
		return err
	}
	markerRef := branchOwnershipRef(operationID)
	markerOID, markerExists, err := r.refOID(ctx, markerRef)
	if err != nil || !markerExists {
		return err
	}
	branchRef := "refs/heads/" + branch
	branchOID, branchExists, err := r.refOID(ctx, branchRef)
	if err != nil {
		return err
	}
	if branchExists && branchOID == markerOID {
		committed, err := r.runPreparedRefTransaction(
			ctx,
			[]string{
				fmt.Sprintf("delete %s %s", branchRef, branchOID),
				fmt.Sprintf("delete %s %s", markerRef, markerOID),
			},
			func() (bool, error) {
				subject, exists, err := r.refLogSubject(ctx, branchRef)
				if err != nil {
					return false, err
				}
				return exists &&
					subject == branchOwnershipLogMessage(operationID), nil
			},
		)
		if err != nil {
			return err
		}
		if committed {
			return nil
		}
	}
	if _, err := r.run(
		ctx,
		fmt.Sprintf("delete %s %s\n", markerRef, markerOID),
		"update-ref",
		"--stdin",
	); err != nil {
		return fmt.Errorf("workspace: discard owned branch transaction: %w", err)
	}
	return nil
}

func (r repositoryCapability) runPreparedRefTransaction(
	ctx context.Context,
	commands []string,
	validate func() (bool, error),
) (_ bool, result error) {
	command, cleanup, err := r.command(ctx, "update-ref", "--stdin")
	if err != nil {
		return false, err
	}
	defer func() {
		result = errors.Join(result, cleanup())
	}()
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	stdin, err := command.StdinPipe()
	if err != nil {
		return false, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return false, err
	}
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return false, err
	}
	finished := false
	defer func() {
		if finished {
			return
		}
		_ = stdin.Close()
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		result = errors.Join(result, command.Wait())
	}()

	writer := bufio.NewWriter(stdin)
	reader := bufio.NewReader(stdout)
	send := func(input string) error {
		if _, err := writer.WriteString(input + "\n"); err != nil {
			return err
		}
		return writer.Flush()
	}
	expect := func(want string) error {
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		if line != want+"\n" {
			return fmt.Errorf(
				"workspace: unexpected update-ref response %q, want %q",
				strings.TrimSpace(line),
				want,
			)
		}
		return nil
	}
	if err := send("start"); err != nil {
		return false, err
	}
	if err := expect("start: ok"); err != nil {
		return false, err
	}
	for _, update := range commands {
		if err := send(update); err != nil {
			return false, err
		}
	}
	if err := send("prepare"); err != nil {
		return false, err
	}
	if err := expect("prepare: ok"); err != nil {
		return false, err
	}

	commit, validationErr := validate()
	action := "abort"
	if validationErr == nil && commit {
		action = "commit"
	}
	actionErr := send(action)
	if actionErr == nil {
		actionErr = expect(action + ": ok")
	}
	closeErr := stdin.Close()
	waitErr := command.Wait()
	finished = true
	if err := errors.Join(
		validationErr,
		actionErr,
		closeErr,
		updateRefError(waitErr, stderr.String()),
	); err != nil {
		return false, err
	}
	return commit, nil
}
