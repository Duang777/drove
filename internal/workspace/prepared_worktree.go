package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (m *Manager) createPreparedWorktreeTarget(
	target *Workspace,
) (result error) {
	bucket, err := m.openManagedBucketRoot(*target)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, bucket.Close())
	}()
	if err := bucket.Mkdir(target.AgentID, 0o700); err != nil {
		return fmt.Errorf("workspace: create prepared worktree target: %w", err)
	}
	opened, err := openRealRootFromRoot(bucket, target.AgentID)
	if err != nil {
		return fmt.Errorf("workspace: open prepared worktree target: %w", err)
	}
	removeOnFailure := true
	defer func() {
		if removeOnFailure {
			result = errors.Join(
				result,
				removeOpenedDirectoryFromRoot(
					bucket,
					target.AgentID,
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
		return fmt.Errorf(
			"workspace: inspect prepared worktree target identity: %w",
			err,
		)
	}
	if err := verifyRootEntryUnchanged(
		bucket,
		target.AgentID,
		opened,
	); err != nil {
		return err
	}
	directory, err := bucket.Open(".")
	if err != nil {
		return fmt.Errorf(
			"workspace: open prepared worktree parent: %w",
			err,
		)
	}
	syncErr := syncRecordDirectory(directory)
	closeErr := directory.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf(
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
		return err
	}
	if target.branchOperationID == "" ||
		record.BranchOperationID != target.branchOperationID ||
		!sameWorkspace(record.workspace(), *target) {
		return errors.New(
			"workspace: preparation record does not match prepared target",
		)
	}
	record.DirectoryIdentity = directoryIdentity
	if err := m.replaceWorkspaceRecord(record); err != nil {
		return fmt.Errorf(
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
		return err
	}
	if !exists ||
		persisted.DirectoryIdentity != directoryIdentity ||
		persisted.BranchOperationID != target.branchOperationID ||
		!sameWorkspace(persisted.workspace(), *target) {
		return errors.New(
			"workspace: prepared worktree target identity was not persisted",
		)
	}
	currentIdentity, err := openedDirectoryIdentity(opened)
	if err != nil {
		return err
	}
	if currentIdentity != directoryIdentity {
		return errors.New(
			"workspace: prepared worktree target identity changed after persistence",
		)
	}
	if err := verifyRootEntryUnchanged(
		bucket,
		target.AgentID,
		opened,
	); err != nil {
		return err
	}
	removeOnFailure = false
	return nil
}

func (m *Manager) initializePreparedWorktree(
	ctx context.Context,
	target *Workspace,
) (result error) {
	root, err := openRealPathRoot(target.Path)
	if err != nil {
		return fmt.Errorf("workspace: open prepared worktree: %w", err)
	}
	defer func() {
		result = errors.Join(result, root.Close())
	}()

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
		target.Path,
		root,
	)
	if err != nil {
		return err
	}
	if err := verifyRealPathRoot(target.Path, root); err != nil {
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
		target.Path,
		root,
		"reset",
		"--hard",
	); err != nil {
		return fmt.Errorf("workspace: checkout prepared worktree: %w", err)
	}
	return nil
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
	return verifyRootEntryUnchanged(
		bucket,
		target.AgentID,
		opened,
	)
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
	gitDirectory := strings.TrimSpace(string(output))
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
