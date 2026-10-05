package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

type preparedWorktreeTarget struct {
	root *os.Root
	name string
	path string
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
	directory, err := bucket.Open(".")
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
	removeOnFailure = false
	if err := bucket.Close(); err != nil {
		bucket = nil
		return nil, err
	}
	bucket = nil
	openedResult := &preparedWorktreeTarget{
		root: opened,
		name: name,
		path: path,
	}
	opened = nil
	return openedResult, nil
}

func (m *Manager) initializePreparedWorktree(
	ctx context.Context,
	target *Workspace,
	prepared *preparedWorktreeTarget,
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
	if _, err := m.runRootedGit(
		ctx,
		prepared.path,
		root,
		"reset",
		"--hard",
	); err != nil {
		return fmt.Errorf("workspace: checkout prepared worktree: %w", err)
	}
	return nil
}

func (m *Manager) promotePreparedWorktreeTarget(
	ctx context.Context,
	target Workspace,
	prepared *preparedWorktreeTarget,
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
	directory, err := bucket.Open(".")
	if err != nil {
		return err
	}
	moved, renameErr := renameDirectoryNoReplace(
		directory,
		opened,
		prepared.name,
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
	if _, err := m.runRootedGit(
		ctx,
		target.Path,
		prepared.root,
		"worktree",
		"repair",
		".",
	); err != nil {
		return fmt.Errorf(
			"workspace: repair promoted worktree registration: %w",
			err,
		)
	}
	return verifyRootEntryUnchanged(
		bucket,
		target.AgentID,
		prepared.root,
	)
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

func (m *Manager) preparedWorktreeStagingPath(
	target Workspace,
	record workspaceRecord,
) (_ string, _ bool, result error) {
	name, err := preparedWorktreeStagingName(
		target.AgentID,
		record.BranchOperationID,
	)
	if err != nil {
		return "", false, err
	}
	bucket, err := m.openManagedBucketRoot(target)
	if err != nil {
		return "", false, err
	}
	defer func() {
		result = errors.Join(result, bucket.Close())
	}()
	info, err := bucket.Lstat(name)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", false, nil
	case err != nil:
		return "", false, err
	case !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
		return "", false, errors.New(
			"workspace: prepared worktree staging path is not a real directory",
		)
	default:
		return filepath.Join(filepath.Dir(target.Path), name), true, nil
	}
}

func (m *Manager) removeStagedPreparedWorktree(
	ctx context.Context,
	target Workspace,
	record workspaceRecord,
	path string,
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
	repository, err := m.repositoryRootAtRoot(ctx, path, opened)
	if err != nil {
		return err
	}
	if repository != record.Repository {
		return errors.New(
			"workspace: prepared worktree staging repository changed",
		)
	}
	gitDirectory, err := m.worktreeGitDirectoryAtRoot(ctx, path, opened)
	if err != nil {
		return err
	}
	if record.GitDirectory == "" ||
		gitDirectory != record.GitDirectory {
		return errors.New(
			"workspace: prepared worktree staging Git directory changed",
		)
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
	if err := repository.verifyPreparedWorktree(
		ctx,
		target,
		record.GitDirectory,
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
