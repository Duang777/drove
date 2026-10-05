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
	manager    *Manager
	path       string
	root       *os.Root
	gitPath    string
	gitRoot    *os.Root
	commonPath string
	commonRoot *os.Root
	startOID   string
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

func (r repositoryCapability) withRepositoryBinding(
	gitPath string,
	gitRoot *os.Root,
	commonPath string,
	commonRoot *os.Root,
	startOID string,
) repositoryCapability {
	r.gitPath = gitPath
	r.gitRoot = gitRoot
	r.commonPath = commonPath
	r.commonRoot = commonRoot
	r.startOID = startOID
	return r
}

func (r repositoryCapability) verifyBinding(ctx context.Context) error {
	if r.root == nil ||
		r.gitRoot == nil ||
		r.commonRoot == nil ||
		r.path == "" ||
		r.gitPath == "" ||
		r.commonPath == "" {
		return errors.New("workspace: repository binding is incomplete")
	}
	verifyPaths := func() error {
		if err := verifyRealPathRoot(r.path, r.root); err != nil {
			return fmt.Errorf(
				"workspace: verify source repository binding: %w",
				err,
			)
		}
		if err := verifyRealPathRoot(r.gitPath, r.gitRoot); err != nil {
			return fmt.Errorf(
				"workspace: verify source Git directory binding: %w",
				err,
			)
		}
		if err := verifyRealPathRoot(
			r.commonPath,
			r.commonRoot,
		); err != nil {
			return fmt.Errorf(
				"workspace: verify common Git directory binding: %w",
				err,
			)
		}
		return nil
	}
	if err := verifyPaths(); err != nil {
		return err
	}
	unbound := rootedRepositoryCapability(r.manager, r.path, r.root)
	currentGitPath, currentCommonPath, err := unbound.repositoryDirectories(
		ctx,
	)
	if err != nil {
		return err
	}
	if currentGitPath != r.gitPath {
		return errors.New(
			"workspace: source Git directory binding changed",
		)
	}
	if currentCommonPath != r.commonPath {
		return errors.New(
			"workspace: common Git directory binding changed",
		)
	}
	return verifyPaths()
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
			r.commonPath,
			r.commonRoot,
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

func (r repositoryCapability) runForward(
	ctx context.Context,
	input string,
	arguments ...string,
) ([]byte, error) {
	if r.root != nil {
		return r.manager.runRootedGitInputWithPolicy(
			ctx,
			r.path,
			r.root,
			r.commonPath,
			r.commonRoot,
			input,
			true,
			arguments...,
		)
	}
	return r.run(ctx, input, arguments...)
}

func (r repositoryCapability) runPrivateGit(
	ctx context.Context,
	input string,
	arguments ...string,
) ([]byte, error) {
	command, cleanup, err := r.privateGitCommand(ctx, arguments...)
	if err != nil {
		return nil, err
	}
	output, commandErr := runGitCommand(command, input)
	return output, errors.Join(commandErr, cleanup())
}

func (r repositoryCapability) repositoryDirectories(
	ctx context.Context,
) (string, string, error) {
	output, err := r.runForward(
		ctx,
		"",
		"rev-parse",
		"--path-format=absolute",
		"--absolute-git-dir",
		"--git-common-dir",
	)
	if err != nil {
		return "", "", fmt.Errorf(
			"workspace: inspect Git directories: %w",
			err,
		)
	}
	return parseRepositoryDirectories(output)
}

func parseRepositoryDirectories(output []byte) (string, string, error) {
	raw := trimGitLineTerminator(output)
	type directoryPair struct {
		git    string
		common string
	}
	var candidates []directoryPair
	for index := strings.IndexByte(raw, '\n'); index >= 0; {
		gitPath := raw[:index]
		commonPath := raw[index+1:]
		if filepath.IsAbs(gitPath) && filepath.IsAbs(commonPath) {
			resolvedGit, gitErr := resolveExistingDirectory(gitPath)
			resolvedCommon, commonErr := resolveExistingDirectory(commonPath)
			if gitErr == nil && commonErr == nil {
				candidates = append(candidates, directoryPair{
					git:    resolvedGit,
					common: resolvedCommon,
				})
			}
		}
		next := strings.IndexByte(raw[index+1:], '\n')
		if next < 0 {
			break
		}
		index += next + 1
	}
	if len(candidates) != 1 {
		return "", "", errors.New(
			"workspace: Git directory query returned an invalid response",
		)
	}
	return candidates[0].git, candidates[0].common, nil
}

func resolveExistingDirectory(path string) (string, error) {
	resolved, err := resolvePath(path)
	if err != nil {
		return "", err
	}
	root, err := openRealPathRoot(resolved)
	if err != nil {
		return "", err
	}
	if err := root.Close(); err != nil {
		return "", err
	}
	return resolved, nil
}

func (r repositoryCapability) headOID(
	ctx context.Context,
) (string, error) {
	output, err := r.runPrivateGit(ctx, "", "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", fmt.Errorf("workspace: resolve branch start: %w", err)
	}
	oid := strings.TrimSpace(string(output))
	if oid == "" || strings.ContainsAny(oid, " \t\r\n") {
		return "", errors.New(
			"workspace: source HEAD returned an invalid object ID",
		)
	}
	return oid, nil
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
			r.commonPath,
			r.commonRoot,
			arguments,
		)
	}
	command := exec.CommandContext(
		ctx,
		r.manager.git,
		append([]string{"-C", r.path}, arguments...)...,
	)
	command.Env = rootedGitEnvironment(command.Environ())
	return command, func() error { return nil }, nil
}

func (r repositoryCapability) worktreeCommand(
	ctx context.Context,
	arguments ...string,
) (*exec.Cmd, func() error, error) {
	if r.root != nil && r.gitRoot != nil {
		return rootedWorktreeGitCommand(
			ctx,
			r.manager.git,
			r.path,
			r.root,
			r.gitPath,
			r.gitRoot,
			arguments,
		)
	}
	return r.command(ctx, arguments...)
}

func (r repositoryCapability) privateGitCommand(
	ctx context.Context,
	arguments ...string,
) (*exec.Cmd, func() error, error) {
	if r.root != nil && r.gitRoot != nil {
		return rootedPrivateGitCommand(
			ctx,
			r.manager.git,
			r.path,
			r.gitPath,
			r.gitRoot,
			arguments,
		)
	}
	return r.command(ctx, arguments...)
}

func (r repositoryCapability) branchExists(
	ctx context.Context,
	branch string,
) (bool, error) {
	_, err := r.runForward(
		ctx,
		"",
		"show-ref",
		"--verify",
		"--quiet",
		"refs/heads/"+branch,
	)
	if err == nil {
		return true, nil
	}
	if isExitCode(err, 1) {
		return false, nil
	}
	return false, fmt.Errorf("workspace: inspect branch %q: %w", branch, err)
}

func (r repositoryCapability) createOwnedBranch(
	ctx context.Context,
	branch string,
	operationID string,
) error {
	if r.startOID == "" {
		return errors.New("workspace: source branch start is unavailable")
	}
	input := fmt.Sprintf(
		"start\ncreate refs/heads/%s %s\ncreate %s %s\nprepare\ncommit\n",
		branch,
		r.startOID,
		branchOwnershipRef(operationID),
		r.startOID,
	)
	if _, err := r.runForward(
		ctx,
		input,
		"update-ref",
		"--create-reflog",
		"-m",
		branchOwnershipLogMessage(operationID),
		"--stdin",
	); err != nil {
		return fmt.Errorf("workspace: create owned branch transaction: %w", err)
	}
	return nil
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

func (r repositoryCapability) verifyPreparedWorktree(
	ctx context.Context,
	target Workspace,
	gitDirectory string,
	registered registeredWorktree,
) error {
	if err := r.verifyPreparedWorktreeRegistration(
		target,
		registered,
		"",
	); err != nil {
		return err
	}
	gitDirectory, err := r.boundGitDirectory(
		gitDirectory,
		target.AgentID,
	)
	if err != nil {
		return err
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

func (repositoryCapability) verifyPreparedWorktreeRegistration(
	target Workspace,
	registered registeredWorktree,
	expectedHeadOID string,
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
	if expectedHeadOID != "" && registered.head != expectedHeadOID {
		return errors.New(
			"workspace: prepared worktree HEAD does not match intent",
		)
	}
	return nil
}

func (r repositoryCapability) boundGitDirectory(
	path string,
	agentID string,
) (string, error) {
	if r.commonRoot == nil {
		return path, nil
	}
	worktreesPath := filepath.Join(r.commonPath, "worktrees")
	name, err := filepath.Rel(worktreesPath, path)
	validName := name == agentID
	if strings.HasPrefix(name, agentID) && len(name) > len(agentID) {
		validName = strings.Trim(name[len(agentID):], "0123456789") == ""
	}
	if err != nil ||
		!filepath.IsAbs(path) ||
		filepath.Dir(name) != "." ||
		!validName {
		return "", errors.New(
			"workspace: prepared Git directory does not match the managed worktree",
		)
	}
	return filepath.Join("worktrees", name), nil
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
		"--",
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

func (r repositoryCapability) removeOwnershipMarker(
	ctx context.Context,
	operationID string,
) error {
	if operationID == "" {
		return nil
	}
	markerRef := branchOwnershipRef(operationID)
	markerOID, exists, err := r.refOID(ctx, markerRef)
	if err != nil || !exists {
		return err
	}
	if _, err := r.run(
		ctx,
		"",
		"update-ref",
		"-d",
		markerRef,
		markerOID,
	); err != nil {
		return fmt.Errorf(
			"workspace: remove branch ownership marker: %w",
			err,
		)
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
	command.Env = append(command.Environ(), "LC_ALL=C", "LANG=C")
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
