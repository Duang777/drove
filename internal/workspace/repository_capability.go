package workspace

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const maxPrivateGitCommonDirectorySize = 64 * 1024

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

func (r repositoryCapability) withPrivateGitBinding(
	gitPath string,
	gitRoot *os.Root,
) repositoryCapability {
	r.gitPath = gitPath
	r.gitRoot = gitRoot
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

func (r repositoryCapability) addPreparedWorktree(
	ctx context.Context,
	target Workspace,
	prepared *preparedWorktreeTarget,
	branch string,
	expectedHeadOID string,
) ([]byte, error) {
	path := prepared.path
	root := prepared.root
	verifyBranch := func() error {
		oid, exists, err := r.refOID(ctx, "refs/heads/"+branch)
		if err != nil {
			return err
		}
		if !exists || oid != expectedHeadOID {
			return errors.New(
				"workspace: prepared branch moved from its expected HEAD",
			)
		}
		return nil
	}
	if err := errors.Join(r.verifyBinding(ctx), verifyBranch()); err != nil {
		return nil, err
	}
	stagePrepared, err := preparePreparedWorktreeAdd(
		target,
		prepared,
		r,
	)
	if err != nil {
		return nil, err
	}
	command, cleanup, err := rootedPreparedWorktreeGitCommand(
		ctx,
		r.manager.git,
		r.path,
		r.root,
		r.commonPath,
		r.commonRoot,
		path,
		root,
		prepared.registrationStageRoot,
		[]string{
			"worktree",
			"add",
			"--quiet",
			"--no-checkout",
			".",
			branch,
		},
	)
	if err != nil {
		return nil, err
	}
	output, commandErr := runGitCommand(command, "")
	finalizeErr := finalizePreparedWorktreeAdd(
		ctx,
		r,
		target,
		prepared,
		commandErr == nil,
	)
	var clearStageErr error
	if stagePrepared && finalizeErr == nil {
		clearStageErr = r.clearPreparedWorktreeStageIdentity(target)
	}
	verifyErr := errors.Join(
		r.verifyBinding(ctx),
		verifyRealPathRoot(path, root),
		verifyBranch(),
	)
	return output, errors.Join(
		commandErr,
		finalizeErr,
		clearStageErr,
		verifyErr,
		cleanup(),
	)
}

func (r repositoryCapability) clearPreparedWorktreeStageIdentity(
	target Workspace,
) error {
	record, exists, err := r.manager.readWorkspaceRecord(target.Path)
	if err == nil && !exists {
		err = errors.New("workspace: preparation record is missing")
	}
	if err != nil {
		return err
	}
	if record.PreparedStageDirectoryIdentity == "" {
		return errors.New(
			"workspace: prepared stage directory identity is missing",
		)
	}
	if target.branchOperationID == "" ||
		record.BranchOperationID != target.branchOperationID ||
		!sameWorkspace(record.workspace(), target) {
		return errors.New(
			"workspace: preparation record changed while clearing stage identity",
		)
	}
	record.PreparedStageDirectoryIdentity = ""
	if err := r.manager.replaceWorkspaceRecord(record); err != nil {
		return fmt.Errorf(
			"workspace: clear prepared stage directory identity: %w",
			err,
		)
	}
	persisted, exists, err := r.manager.readWorkspaceRecord(target.Path)
	if err != nil {
		return err
	}
	if !exists || persisted.PreparedStageDirectoryIdentity != "" {
		return errors.New(
			"workspace: prepared stage directory identity was not cleared",
		)
	}
	return nil
}

func (r repositoryCapability) runPrivateGit(
	ctx context.Context,
	input string,
	arguments ...string,
) ([]byte, error) {
	return r.runPrivateGitAt(ctx, r.path, input, arguments...)
}

func (r repositoryCapability) runPrivateGitAt(
	ctx context.Context,
	worktreePath string,
	input string,
	arguments ...string,
) ([]byte, error) {
	return r.runPrivateGitAtWithOutputPolicy(
		ctx,
		worktreePath,
		input,
		false,
		arguments...,
	)
}

func (r repositoryCapability) runPrivateGitAtKeepingOutput(
	ctx context.Context,
	worktreePath string,
	input string,
	arguments ...string,
) ([]byte, error) {
	return r.runPrivateGitAtWithOutputPolicy(
		ctx,
		worktreePath,
		input,
		true,
		arguments...,
	)
}

func (r repositoryCapability) runPrivateGitAtWithOutputPolicy(
	ctx context.Context,
	worktreePath string,
	input string,
	keepOutputOnError bool,
	arguments ...string,
) ([]byte, error) {
	command, cleanup, err := r.privateGitCommandAt(
		ctx,
		worktreePath,
		arguments...,
	)
	if err != nil {
		return nil, err
	}
	run := runGitCommand
	if keepOutputOnError {
		run = runGitCommandKeepingOutput
	}
	output, commandErr := run(command, input)
	return output, errors.Join(commandErr, cleanup())
}

func (r repositoryCapability) runWorktreeAt(
	ctx context.Context,
	worktreePath string,
	worktreeRoot *os.Root,
	input string,
	arguments ...string,
) ([]byte, error) {
	command, cleanup, err := r.worktreeCommandAt(
		ctx,
		worktreePath,
		worktreeRoot,
		arguments...,
	)
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
	if sameRegisteredWorktreePath(r.gitPath, r.commonPath) {
		output, err := r.run(ctx, "", "rev-parse", "--verify", "HEAD")
		if err != nil {
			return "", fmt.Errorf("workspace: resolve branch start: %w", err)
		}
		return validateHeadOID(strings.TrimSpace(string(output)))
	}
	registered, exists, err := r.worktreeRegistration(ctx, r.path)
	if err != nil {
		return "", fmt.Errorf("workspace: resolve branch start: %w", err)
	}
	if !exists {
		return "", errors.New(
			"workspace: source worktree registration is missing",
		)
	}
	return validateHeadOID(registered.head)
}

func validateHeadOID(oid string) (string, error) {
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
	return r.worktreeCommandAt(ctx, r.path, r.root, arguments...)
}

func (r repositoryCapability) worktreeCommandAt(
	ctx context.Context,
	worktreePath string,
	worktreeRoot *os.Root,
	arguments ...string,
) (*exec.Cmd, func() error, error) {
	if r.root != nil && r.gitRoot != nil {
		return rootedWorktreeGitCommand(
			ctx,
			r.manager.git,
			worktreePath,
			worktreeRoot,
			r.gitPath,
			r.gitRoot,
			r.commonPath,
			r.commonRoot,
			arguments,
		)
	}
	return r.command(ctx, arguments...)
}

func (r repositoryCapability) privateGitCommand(
	ctx context.Context,
	arguments ...string,
) (*exec.Cmd, func() error, error) {
	return r.privateGitCommandAt(ctx, r.path, arguments...)
}

func (r repositoryCapability) privateGitCommandAt(
	ctx context.Context,
	worktreePath string,
	arguments ...string,
) (*exec.Cmd, func() error, error) {
	if r.root != nil && r.gitRoot != nil {
		if r.commonPath == "" || r.commonRoot == nil {
			return nil, nil, errors.New(
				"workspace: private Git binding has no common directory",
			)
		}
		return rootedPrivateGitCommand(
			ctx,
			r.manager.git,
			worktreePath,
			r.gitPath,
			r.gitRoot,
			r.commonPath,
			r.commonRoot,
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
		if sameRegisteredWorktreePath(registered.path, path) {
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
	_ context.Context,
	target Workspace,
	gitDirectory string,
	registered registeredWorktree,
) error {
	gitDirectory, err := r.boundGitDirectory(
		gitDirectory,
		target.AgentID,
	)
	if err != nil {
		return err
	}
	if err := r.verifyPreparedWorktreeRegistration(
		target,
		registered,
		target.expectedHeadOID,
	); err != nil {
		return err
	}
	return nil
}

func (r repositoryCapability) verifyBoundPreparedWorktree(
	_ context.Context,
	target Workspace,
	registered registeredWorktree,
) error {
	if err := r.verifyPrivateGitCommonDirectory(); err != nil {
		return err
	}
	if err := r.verifyPreparedWorktreeRegistration(
		target,
		registered,
		target.expectedHeadOID,
	); err != nil {
		return err
	}
	return r.verifyPrivateGitCommonDirectory()
}

func (r repositoryCapability) verifyPrivateGitCommonDirectory() (
	result error,
) {
	if r.gitRoot == nil ||
		r.commonRoot == nil ||
		r.gitPath == "" ||
		r.commonPath == "" {
		return errors.New(
			"workspace: private Git common directory binding is incomplete",
		)
	}
	if err := verifyRealPathRoot(r.gitPath, r.gitRoot); err != nil {
		return err
	}
	if err := verifyRealPathRoot(r.commonPath, r.commonRoot); err != nil {
		return err
	}

	const name = "commondir"
	pathInfo, err := r.gitRoot.Lstat(name)
	if err != nil {
		return fmt.Errorf(
			"workspace: inspect private Git common directory pointer: %w",
			err,
		)
	}
	if !pathInfo.Mode().IsRegular() ||
		pathInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New(
			"workspace: private Git common directory pointer is not a regular file",
		)
	}
	file, err := r.gitRoot.Open(name)
	if err != nil {
		return fmt.Errorf(
			"workspace: open private Git common directory pointer: %w",
			err,
		)
	}
	defer func() {
		result = errors.Join(result, file.Close())
	}()
	before, err := file.Stat()
	if err != nil {
		return fmt.Errorf(
			"workspace: inspect opened private Git common directory pointer: %w",
			err,
		)
	}
	if !before.Mode().IsRegular() ||
		before.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(pathInfo, before) {
		return errors.New(
			"workspace: private Git common directory pointer changed while opening",
		)
	}
	raw, err := io.ReadAll(
		io.LimitReader(file, maxPrivateGitCommonDirectorySize+1),
	)
	if err != nil {
		return fmt.Errorf(
			"workspace: read private Git common directory pointer: %w",
			err,
		)
	}
	if len(raw) > maxPrivateGitCommonDirectorySize {
		return errors.New(
			"workspace: private Git common directory pointer is too large",
		)
	}
	after, err := file.Stat()
	if err != nil {
		return fmt.Errorf(
			"workspace: reinspect private Git common directory pointer: %w",
			err,
		)
	}
	current, err := r.gitRoot.Lstat(name)
	if err != nil {
		return fmt.Errorf(
			"workspace: reinspect private Git common directory pointer path: %w",
			err,
		)
	}
	if !after.Mode().IsRegular() ||
		after.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(before, after) ||
		before.Size() != after.Size() ||
		before.Mode() != after.Mode() ||
		!before.ModTime().Equal(after.ModTime()) ||
		!current.Mode().IsRegular() ||
		current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(before, current) {
		return errors.New(
			"workspace: private Git common directory pointer changed while reading",
		)
	}

	value := strings.TrimSuffix(string(raw), "\n")
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return errors.New(
			"workspace: private Git common directory pointer is invalid",
		)
	}
	commonPath := value
	if !filepath.IsAbs(commonPath) {
		commonPath = filepath.Join(r.gitPath, commonPath)
	}
	commonPath, err = resolvePath(commonPath)
	if err != nil {
		return fmt.Errorf(
			"workspace: resolve private Git common directory pointer: %w",
			err,
		)
	}
	if commonPath != r.commonPath {
		return errors.New(
			"workspace: private Git common directory binding changed",
		)
	}
	commonRoot, err := openRealPathRoot(commonPath)
	if err != nil {
		return fmt.Errorf(
			"workspace: open private Git common directory target: %w",
			err,
		)
	}
	defer func() {
		result = errors.Join(result, commonRoot.Close())
	}()
	expectedIdentity, err := openedDirectoryIdentity(r.commonRoot)
	if err != nil {
		return err
	}
	currentIdentity, err := openedDirectoryIdentity(commonRoot)
	if err != nil {
		return err
	}
	if currentIdentity != expectedIdentity {
		return errors.New(
			"workspace: private Git common directory identity changed",
		)
	}
	return errors.Join(
		verifyRealPathRoot(r.gitPath, r.gitRoot),
		verifyRealPathRoot(r.commonPath, r.commonRoot),
		verifyRealPathRoot(commonPath, commonRoot),
	)
}

func (r repositoryCapability) derivePreparedWorktreeExpectedHead(
	ctx context.Context,
	target Workspace,
) (string, error) {
	if err := r.verifyBinding(ctx); err != nil {
		return "", err
	}
	registered, exists, err := r.worktreeRegistration(ctx, target.Path)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", errors.New(
			"workspace: prepared worktree registration disappeared",
		)
	}
	if err := r.verifyPreparedWorktreeRegistration(
		target,
		registered,
		"",
	); err != nil {
		return "", err
	}
	branchOID, exists, err := r.refOID(
		ctx,
		"refs/heads/"+target.Branch,
	)
	if err != nil {
		return "", err
	}
	if !exists || branchOID != registered.head {
		return "", errors.New(
			"workspace: prepared branch does not match worktree registration",
		)
	}
	if err := r.verifyBinding(ctx); err != nil {
		return "", err
	}
	return branchOID, nil
}

func (r repositoryCapability) verifyPreparationRefs(
	ctx context.Context,
	target Workspace,
	requireOwnershipMarker bool,
) error {
	if target.expectedHeadOID == "" {
		return errors.New(
			"workspace: prepared worktree expected HEAD is unavailable",
		)
	}
	branchOID, exists, err := r.refOID(
		ctx,
		"refs/heads/"+target.Branch,
	)
	if err != nil {
		return err
	}
	if !exists || branchOID != target.expectedHeadOID {
		return errors.New(
			"workspace: prepared branch moved from its expected HEAD",
		)
	}
	if !target.createdBranch || !requireOwnershipMarker {
		return nil
	}
	markerOID, exists, err := r.refOID(
		ctx,
		branchOwnershipRef(target.branchOperationID),
	)
	if err != nil {
		return err
	}
	if !exists || markerOID != target.expectedHeadOID {
		return errors.New(
			"workspace: prepared branch ownership marker changed",
		)
	}
	return nil
}

func (repositoryCapability) verifyPreparedWorktreeRegistration(
	target Workspace,
	registered registeredWorktree,
	expectedHeadOID string,
) error {
	if !sameRegisteredWorktreePath(registered.path, target.Path) ||
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

func sameRegisteredWorktreePath(left string, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if left == right {
		return true
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
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
	if name == "-" ||
		(strings.HasPrefix(name, "-") &&
			strings.Trim(name[1:], "0123456789") == "") {
		validName = true
	}
	if err != nil ||
		!filepath.IsAbs(path) ||
		filepath.Dir(name) != "." ||
		!validName {
		return "", fmt.Errorf(
			"workspace: prepared Git directory %q does not match agent %q",
			path,
			agentID,
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
