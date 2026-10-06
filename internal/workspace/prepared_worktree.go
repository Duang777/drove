package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

type preparedWorktreeTarget struct {
	root         *os.Root
	name         string
	path         string
	gitDirectory string
}

func preparedWorktreeStagingName(
	agentID string,
	operationID string,
) (string, error) {
	operation, err := uuid.Parse(operationID)
	if err != nil || operation.String() != operationID {
		return "", errors.New(
			"workspace: preparation operation ID is invalid",
		)
	}
	name := agentID
	for _, value := range operation {
		name += fmt.Sprintf("%03d", value)
	}
	return name, nil
}

func preparedWorktreeIsolationName(stagingName string) string {
	return stagingName + ".rename"
}

func (m *Manager) createPreparedWorktreeTarget(
	target *Workspace,
) (_ *preparedWorktreeTarget, result error) {
	bucket, err := m.openManagedBucketRoot(*target)
	if err != nil {
		return nil, err
	}
	defer func() {
		if bucket != nil {
			result = errors.Join(result, bucket.Close())
		}
	}()
	name, err := preparedWorktreeStagingName(
		target.AgentID,
		target.branchOperationID,
	)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(filepath.Dir(target.Path), name)
	if err := bucket.Mkdir(name, 0o700); err != nil {
		return nil, fmt.Errorf(
			"workspace: create prepared worktree target: %w",
			err,
		)
	}
	opened, err := openRealRootFromRoot(bucket, name)
	if err != nil {
		return nil, fmt.Errorf(
			"workspace: open prepared worktree target: %w",
			err,
		)
	}
	removeOnFailure := true
	defer func() {
		if removeOnFailure {
			result = errors.Join(
				result,
				removeOpenedDirectoryFromRoot(
					bucket,
					name,
					opened,
				),
			)
			opened = nil
		}
		if opened != nil {
			result = errors.Join(result, opened.Close())
		}
	}()

	directoryIdentity, err := openedDirectoryIdentity(opened)
	if err != nil {
		return nil, fmt.Errorf(
			"workspace: inspect prepared worktree target identity: %w",
			err,
		)
	}
	if err := verifyRootEntryUnchanged(
		bucket,
		name,
		opened,
	); err != nil {
		return nil, err
	}
	directory, err := openRecordDirectory(bucket)
	if err != nil {
		return nil, fmt.Errorf(
			"workspace: open prepared worktree parent: %w",
			err,
		)
	}
	syncErr := syncRecordDirectory(directory)
	closeErr := directory.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return nil, fmt.Errorf(
			"workspace: sync prepared worktree parent: %w",
			err,
		)
	}

	target.directoryIdentity = directoryIdentity
	record, exists, err := m.readWorkspaceRecordFromBucket(
		bucket,
		target.AgentID,
		target.Path,
	)
	if err == nil && !exists {
		err = errors.New("workspace: preparation record is missing")
	}
	if err != nil {
		return nil, err
	}
	if target.branchOperationID == "" ||
		record.BranchOperationID != target.branchOperationID ||
		!sameWorkspace(record.workspace(), *target) {
		return nil, errors.New(
			"workspace: preparation record does not match prepared target",
		)
	}
	record.DirectoryIdentity = directoryIdentity
	if err := m.replaceWorkspaceRecord(record); err != nil {
		return nil, fmt.Errorf(
			"workspace: persist prepared worktree target identity: %w",
			err,
		)
	}
	persisted, exists, err := m.readWorkspaceRecordFromBucket(
		bucket,
		target.AgentID,
		target.Path,
	)
	if err != nil {
		return nil, err
	}
	if !exists ||
		persisted.DirectoryIdentity != directoryIdentity ||
		persisted.BranchOperationID != target.branchOperationID ||
		!sameWorkspace(persisted.workspace(), *target) {
		return nil, errors.New(
			"workspace: prepared worktree target identity was not persisted",
		)
	}
	currentIdentity, err := openedDirectoryIdentity(opened)
	if err != nil {
		return nil, err
	}
	if currentIdentity != directoryIdentity {
		return nil, errors.New(
			"workspace: prepared worktree target identity changed after persistence",
		)
	}
	if err := verifyRootEntryUnchanged(
		bucket,
		name,
		opened,
	); err != nil {
		return nil, err
	}
	openedResult := &preparedWorktreeTarget{
		root: opened,
		name: name,
		path: path,
	}
	opened = nil
	removeOnFailure = false
	if err := bucket.Close(); err != nil {
		bucket = nil
		return openedResult, err
	}
	bucket = nil
	return openedResult, nil
}

func (m *Manager) initializePreparedWorktree(
	ctx context.Context,
	target *Workspace,
	prepared *preparedWorktreeTarget,
	repository repositoryCapability,
	includedPaths []string,
) (result error) {
	root := prepared.root
	directoryIdentity, err := openedDirectoryIdentity(root)
	if err != nil {
		return fmt.Errorf(
			"workspace: inspect prepared worktree identity: %w",
			err,
		)
	}
	if target.directoryIdentity == "" {
		return errors.New(
			"workspace: prepared worktree target identity is unavailable",
		)
	}
	if directoryIdentity != target.directoryIdentity {
		return errors.New(
			"workspace: prepared worktree target identity changed",
		)
	}
	registered, exists, err := repository.worktreeRegistration(
		ctx,
		prepared.path,
	)
	if err != nil {
		return err
	}
	preparedIntent := *target
	preparedIntent.Path = prepared.path
	if !exists {
		return errors.New(
			"workspace: prepared worktree registration is missing",
		)
	}
	if err := repository.verifyPreparedWorktreeRegistration(
		preparedIntent,
		registered,
		target.expectedHeadOID,
	); err != nil {
		return err
	}
	gitDirectory, err := m.worktreeGitDirectoryAtRoot(
		ctx,
		prepared.path,
		root,
	)
	if err != nil {
		return err
	}
	if err := verifyRealPathRoot(prepared.path, root); err != nil {
		return err
	}
	if _, err := repository.boundGitDirectory(
		gitDirectory,
		target.AgentID,
	); err != nil {
		return err
	}
	gitRoot, err := openRealPathRoot(gitDirectory)
	if err != nil {
		return fmt.Errorf(
			"workspace: open prepared private Git directory: %w",
			err,
		)
	}
	defer func() {
		result = errors.Join(result, gitRoot.Close())
	}()
	gitGuard, err := openRepositoryGuard(gitDirectory, gitRoot)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, gitGuard.Close())
	}()
	privateRepository := repository.withPrivateGitBinding(
		gitDirectory,
		gitRoot,
	)
	privateHead, err := privateRepository.runPrivateGit(
		ctx,
		"",
		"rev-parse",
		"--verify",
		"HEAD",
	)
	if err != nil {
		return fmt.Errorf(
			"workspace: inspect prepared private HEAD: %w",
			err,
		)
	}
	if strings.TrimSpace(string(privateHead)) != target.expectedHeadOID {
		return errors.New(
			"workspace: prepared private HEAD does not match intent",
		)
	}
	if err := repository.verifyPreparationRefs(ctx, *target, true); err != nil {
		return err
	}
	if err := checkoutPreparedWorktree(
		ctx,
		privateRepository,
		prepared.path,
		root,
		target.expectedHeadOID,
	); err != nil {
		return fmt.Errorf("workspace: checkout prepared worktree: %w", err)
	}
	expectedTree, err := privateRepository.runPrivateGit(
		ctx,
		"",
		"rev-parse",
		"--verify",
		target.expectedHeadOID+"^{tree}",
	)
	if err != nil {
		return fmt.Errorf(
			"workspace: inspect prepared expected tree: %w",
			err,
		)
	}
	indexTree, err := privateRepository.runPrivateGit(
		ctx,
		"",
		"write-tree",
	)
	if err != nil {
		return fmt.Errorf(
			"workspace: inspect prepared index tree: %w",
			err,
		)
	}
	if strings.TrimSpace(string(indexTree)) !=
		strings.TrimSpace(string(expectedTree)) {
		return errors.New(
			"workspace: prepared index does not match expected tree",
		)
	}
	if len(includedPaths) > 0 {
		target.trackedPaths, err = m.worktreeTrackedPaths(
			ctx,
			prepared.path,
			privateRepository,
		)
		if err != nil {
			return err
		}
	}
	if err := repository.verifyPreparationRefs(ctx, *target, true); err != nil {
		return err
	}
	registered, exists, err = repository.worktreeRegistration(
		ctx,
		prepared.path,
	)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New(
			"workspace: prepared worktree registration disappeared",
		)
	}
	if err := privateRepository.verifyBoundPreparedWorktree(
		ctx,
		preparedIntent,
		registered,
	); err != nil {
		return err
	}
	confirmedGitDirectory, err := m.worktreeGitDirectoryAtRoot(
		ctx,
		prepared.path,
		root,
	)
	if err != nil {
		return err
	}
	if confirmedGitDirectory != gitDirectory {
		return errors.New(
			"workspace: prepared worktree Git pointer changed",
		)
	}
	if err := verifyRealPathRoot(gitDirectory, gitRoot); err != nil {
		return err
	}
	target.gitDirectory = gitDirectory

	record, exists, err := m.readWorkspaceRecord(target.Path)
	if err == nil && !exists {
		err = errors.New("workspace: worktree identity record is missing")
	}
	if err == nil {
		if record.DirectoryIdentity != directoryIdentity {
			err = errors.New(
				"workspace: prepared worktree target identity record changed",
			)
		}
	}
	if err == nil {
		record.GitDirectory = gitDirectory
		err = m.replaceWorkspaceRecord(record)
	}
	if err != nil {
		return fmt.Errorf("workspace: persist worktree identity: %w", err)
	}
	prepared.gitDirectory = gitDirectory
	return nil
}

func (m *Manager) promotePreparedWorktreeTarget(
	ctx context.Context,
	target Workspace,
	prepared *preparedWorktreeTarget,
	repository repositoryCapability,
) (result error) {
	bucket, err := m.openManagedBucketRoot(target)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, bucket.Close())
	}()
	if err := verifyRootEntryUnchanged(
		bucket,
		prepared.name,
		prepared.root,
	); err != nil {
		return err
	}
	if _, err := bucket.Lstat(target.AgentID); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		if err == nil {
			return errors.New(
				"workspace: prepared worktree target already exists",
			)
		}
		return fmt.Errorf(
			"workspace: inspect prepared worktree destination: %w",
			err,
		)
	}
	opened, err := prepared.root.Stat(".")
	if err != nil {
		return err
	}
	directory, err := openRecordDirectory(bucket)
	if err != nil {
		return err
	}
	stagingPath := prepared.path
	moved, renameErr := renameDirectoryNoReplace(
		directory,
		opened,
		prepared.name,
		preparedWorktreeIsolationName(prepared.name),
		target.AgentID,
	)
	if moved {
		prepared.name = target.AgentID
		prepared.path = target.Path
	}
	syncErr := syncRecordDirectory(directory)
	closeErr := directory.Close()
	if err := errors.Join(renameErr, syncErr, closeErr); err != nil {
		return fmt.Errorf(
			"workspace: promote prepared worktree target: %w",
			err,
		)
	}
	if !moved {
		return errors.New("workspace: prepared worktree target was not promoted")
	}
	if err := verifyRootEntryUnchanged(
		bucket,
		target.AgentID,
		prepared.root,
	); err != nil {
		return err
	}
	if err := m.repairPreparedWorktreeRegistration(
		ctx,
		target,
		prepared,
		repository,
		stagingPath,
	); err != nil {
		return err
	}
	return verifyRootEntryUnchanged(
		bucket,
		target.AgentID,
		prepared.root,
	)
}

func (m *Manager) repairPreparedWorktreeRegistration(
	ctx context.Context,
	target Workspace,
	prepared *preparedWorktreeTarget,
	repository repositoryCapability,
	stalePath string,
) (result error) {
	if prepared.gitDirectory == "" {
		return errors.New(
			"workspace: prepared private Git directory is unavailable",
		)
	}
	gitRoot, err := openRealPathRoot(prepared.gitDirectory)
	if err != nil {
		return fmt.Errorf(
			"workspace: open prepared private Git directory: %w",
			err,
		)
	}
	defer func() {
		result = errors.Join(result, gitRoot.Close())
	}()
	gitGuard, err := openRepositoryGuard(prepared.gitDirectory, gitRoot)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, gitGuard.Close())
	}()
	before, err := repository.registeredWorktrees(ctx)
	if err != nil {
		return err
	}
	privateRepository := repository.withPrivateGitBinding(
		prepared.gitDirectory,
		gitRoot,
	)
	runRepair := func() error {
		command, cleanup, err := privateRepository.worktreeCommandAt(
			ctx,
			target.Path,
			prepared.root,
			"worktree",
			"repair",
			".",
		)
		if err != nil {
			return err
		}
		_, commandErr := runGitCommand(command, "")
		return errors.Join(commandErr, cleanup())
	}
	restore := func() error {
		arguments := []string{"worktree", "repair"}
		for _, registered := range before {
			if sameRegisteredWorktreePath(registered.path, stalePath) {
				continue
			}
			if _, err := os.Lstat(registered.path); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return err
			}
			arguments = append(arguments, registered.path)
		}
		arguments = append(arguments, target.Path)
		_, err := repository.run(ctx, "", arguments...)
		return err
	}
	commandErr := runRepair()
	if err := errors.Join(
		repository.verifyBinding(ctx),
		verifyRealPathRoot(prepared.gitDirectory, gitRoot),
	); err != nil {
		return errors.Join(err, restore())
	}
	after, err := repository.registeredWorktrees(ctx)
	if err != nil {
		return errors.Join(commandErr, err, restore())
	}
	transitionErr := validatePreparedRegistrationTransition(
		before,
		after,
		target,
		stalePath,
		privateRepository,
	)
	if err := errors.Join(commandErr, transitionErr); err != nil {
		restoreErr := restore()
		restored, verifyErr := repository.registeredWorktrees(ctx)
		if verifyErr == nil {
			verifyErr = validatePreparedRegistrationTransition(
				before,
				restored,
				target,
				stalePath,
				privateRepository,
			)
		}
		return fmt.Errorf(
			"workspace: repair promoted worktree registration: %w",
			errors.Join(err, restoreErr, verifyErr),
		)
	}
	return nil
}

func validatePreparedRegistrationTransition(
	before []registeredWorktree,
	after []registeredWorktree,
	target Workspace,
	stalePath string,
	repository repositoryCapability,
) error {
	if len(after) != len(before) {
		return errors.New(
			"workspace: worktree registration set changed during repair",
		)
	}
	var (
		stale        registeredWorktree
		staleFound   bool
		targetRecord registeredWorktree
		targetFound  bool
	)
	for _, registered := range before {
		if sameRegisteredWorktreePath(registered.path, stalePath) {
			stale = registered
			staleFound = true
			break
		}
	}
	if !staleFound {
		return errors.New(
			"workspace: stale prepared worktree registration is missing",
		)
	}
	for _, registered := range after {
		if sameRegisteredWorktreePath(registered.path, stalePath) {
			return errors.New(
				"workspace: stale prepared worktree registration remains after repair",
			)
		}
		if sameRegisteredWorktreePath(registered.path, target.Path) {
			targetRecord = registered
			targetFound = true
		}
	}
	if !targetFound {
		return errors.New(
			"workspace: repaired worktree registration is missing",
		)
	}
	if err := repository.verifyPreparedWorktreeRegistration(
		target,
		targetRecord,
		target.expectedHeadOID,
	); err != nil {
		return err
	}
	for _, expected := range before {
		if sameRegisteredWorktreePath(expected.path, stalePath) {
			continue
		}
		var matched bool
		for _, actual := range after {
			if !sameRegisteredWorktreePath(expected.path, actual.path) {
				continue
			}
			if expected.head != actual.head ||
				expected.branch != actual.branch ||
				expected.detached != actual.detached {
				return errors.New(
					"workspace: unrelated worktree registration changed during repair",
				)
			}
			matched = true
			break
		}
		if !matched {
			return errors.New(
				"workspace: unrelated worktree registration disappeared during repair",
			)
		}
	}
	if stale.head != targetRecord.head ||
		stale.branch != targetRecord.branch ||
		stale.detached != targetRecord.detached {
		return errors.New(
			"workspace: repaired worktree registration changed identity",
		)
	}
	return nil
}

func (m *Manager) cleanupPreparedWorktreeTarget(
	target Workspace,
	prepared *preparedWorktreeTarget,
) (result error) {
	if prepared == nil || prepared.root == nil {
		return nil
	}
	bucket, err := m.openManagedBucketRoot(target)
	if err != nil {
		return errors.Join(err, prepared.root.Close())
	}
	defer func() {
		result = errors.Join(result, bucket.Close())
	}()
	root := prepared.root
	prepared.root = nil
	return removeOpenedDirectoryFromRoot(bucket, prepared.name, root)
}

func (prepared *preparedWorktreeTarget) Close() error {
	if prepared == nil || prepared.root == nil {
		return nil
	}
	err := prepared.root.Close()
	prepared.root = nil
	return err
}

func (m *Manager) preparedWorktreeStagingPaths(
	target Workspace,
	record workspaceRecord,
) (_ string, _ string, _ bool, result error) {
	name, err := preparedWorktreeStagingName(
		target.AgentID,
		record.BranchOperationID,
	)
	if err != nil {
		return "", "", false, err
	}
	bucket, err := m.openManagedBucketRoot(target)
	if err != nil {
		return "", "", false, err
	}
	defer func() {
		result = errors.Join(result, bucket.Close())
	}()
	names := []string{name, preparedWorktreeIsolationName(name)}
	active := ""
	for _, candidate := range names {
		info, err := bucket.Lstat(candidate)
		switch {
		case errors.Is(err, os.ErrNotExist):
			continue
		case err != nil:
			return "", "", false, err
		case !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
			return "", "", false, errors.New(
				"workspace: prepared worktree staging path is not a real directory",
			)
		case active != "":
			return "", "", false, errors.New(
				"workspace: preparation has multiple staging worktree paths",
			)
		default:
			active = candidate
		}
	}
	parent := filepath.Dir(target.Path)
	registrationPath := filepath.Join(parent, name)
	if active == "" {
		return registrationPath, registrationPath, false, nil
	}
	return filepath.Join(parent, active), registrationPath, true, nil
}

func (m *Manager) removeStagedPreparedWorktree(
	ctx context.Context,
	target Workspace,
	record workspaceRecord,
	path string,
	registrationPath string,
	repository repositoryCapability,
) (result error) {
	bucket, err := m.openManagedBucketRoot(target)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, bucket.Close())
	}()
	name := filepath.Base(path)
	opened, err := openRealRootFromRoot(bucket, name)
	if err != nil {
		return err
	}
	openedOwned := true
	defer func() {
		if openedOwned {
			result = errors.Join(result, opened.Close())
		}
	}()
	identity, err := openedDirectoryIdentity(opened)
	if err != nil {
		return err
	}
	if record.DirectoryIdentity == "" ||
		identity != record.DirectoryIdentity {
		return errors.New(
			"workspace: prepared worktree staging identity changed",
		)
	}
	registered, exists, err := repository.worktreeRegistration(
		ctx,
		registrationPath,
	)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New(
			"workspace: prepared worktree staging registration disappeared",
		)
	}
	if record.GitDirectory == "" {
		return errors.New(
			"workspace: prepared worktree staging Git identity is incomplete",
		)
	}
	stagingTarget := target
	stagingTarget.Path = registrationPath
	if err := repository.verifyPreparedWorktree(
		ctx,
		stagingTarget,
		record.GitDirectory,
		registered,
	); err != nil {
		return err
	}
	if err := verifyRootEntryUnchanged(bucket, name, opened); err != nil {
		return err
	}
	openedOwned = false
	return removeOpenedDirectoryFromRoot(bucket, name, opened)
}

func (m *Manager) verifyPreparedWorktreeForAcknowledgement(
	ctx context.Context,
	target Workspace,
	record workspaceRecord,
	repository repositoryCapability,
) (result error) {
	if record.GitDirectory == "" || record.DirectoryIdentity == "" {
		return errors.New(
			"workspace: prepared worktree identity is incomplete",
		)
	}
	if err := repository.verifyBinding(ctx); err != nil {
		return err
	}
	bucket, err := m.openManagedBucketRoot(target)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, bucket.Close())
	}()
	opened, err := openRealRootFromRoot(bucket, target.AgentID)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, opened.Close())
	}()
	directoryIdentity, err := openedDirectoryIdentity(opened)
	if err != nil {
		return err
	}
	if directoryIdentity != record.DirectoryIdentity {
		return errors.New(
			"workspace: prepared worktree directory identity changed",
		)
	}
	if err := verifyRootEntryUnchanged(
		bucket,
		target.AgentID,
		opened,
	); err != nil {
		return err
	}
	gitDirectory, err := m.worktreeGitDirectoryAtRoot(
		ctx,
		target.Path,
		opened,
	)
	if err != nil {
		return err
	}
	if gitDirectory != record.GitDirectory {
		return errors.New(
			"workspace: prepared worktree Git directory changed",
		)
	}
	if target.expectedHeadOID == "" ||
		target.expectedHeadOID != record.ExpectedHeadOID {
		return errors.New(
			"workspace: prepared worktree expected HEAD changed",
		)
	}
	if _, err := repository.boundGitDirectory(
		record.GitDirectory,
		target.AgentID,
	); err != nil {
		return err
	}
	gitRoot, err := openRealPathRoot(record.GitDirectory)
	if err != nil {
		return fmt.Errorf(
			"workspace: open prepared private Git directory: %w",
			err,
		)
	}
	defer func() {
		result = errors.Join(result, gitRoot.Close())
	}()
	gitGuard, err := openRepositoryGuard(record.GitDirectory, gitRoot)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, gitGuard.Close())
	}()
	if err := repository.verifyPreparationRefs(
		ctx,
		target,
		!record.PreparationCommitted,
	); err != nil {
		return err
	}
	registered, exists, err := repository.worktreeRegistration(
		ctx,
		target.Path,
	)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New(
			"workspace: prepared worktree registration disappeared",
		)
	}
	privateRepository := repository.withPrivateGitBinding(
		record.GitDirectory,
		gitRoot,
	)
	if err := privateRepository.verifyBoundPreparedWorktree(
		ctx,
		target,
		registered,
	); err != nil {
		return err
	}
	currentIdentity, err := openedDirectoryIdentity(opened)
	if err != nil {
		return err
	}
	if currentIdentity != record.DirectoryIdentity {
		return errors.New(
			"workspace: prepared worktree directory identity changed while acknowledging",
		)
	}
	if err := verifyRootEntryUnchanged(
		bucket,
		target.AgentID,
		opened,
	); err != nil {
		return err
	}
	if err := verifyRealPathRoot(record.GitDirectory, gitRoot); err != nil {
		return err
	}
	if err := repository.verifyPreparationRefs(
		ctx,
		target,
		!record.PreparationCommitted,
	); err != nil {
		return err
	}
	return repository.verifyBinding(ctx)
}

func (m *Manager) worktreeGitDirectoryAtRoot(
	ctx context.Context,
	path string,
	root *os.Root,
) (string, error) {
	output, err := m.runRootedGit(
		ctx,
		path,
		root,
		"rev-parse",
		"--absolute-git-dir",
	)
	if err != nil {
		return "", fmt.Errorf(
			"workspace: inspect Git directory for %q: %w",
			path,
			err,
		)
	}
	gitDirectory := trimGitLineTerminator(output)
	if !filepath.IsAbs(gitDirectory) {
		return "", fmt.Errorf(
			"workspace: Git directory for %q is not absolute",
			path,
		)
	}
	gitDirectory, err = resolvePath(gitDirectory)
	if err != nil {
		return "", fmt.Errorf(
			"workspace: resolve Git directory for %q: %w",
			path,
			err,
		)
	}
	return gitDirectory, nil
}
