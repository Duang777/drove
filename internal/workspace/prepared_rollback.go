package workspace

import (
	"context"
	"errors"
	"fmt"
)

func (m *Manager) capturePreparedWorktreeIdentity(
	ctx context.Context,
	target Workspace,
	record workspaceRecord,
	repository repositoryCapability,
) (_ workspaceRecord, result error) {
	if (record.GitDirectory == "") != (record.DirectoryIdentity == "") {
		return workspaceRecord{}, errors.New(
			"workspace: prepared worktree identity is incomplete",
		)
	}
	registered, exists, err := repository.worktreeRegistration(
		ctx,
		target.Path,
	)
	if err != nil {
		return workspaceRecord{}, err
	}
	if !exists {
		return workspaceRecord{}, errors.New(
			"workspace: prepared worktree has no Git registration",
		)
	}
	gitDirectory := record.GitDirectory
	if gitDirectory == "" {
		gitDirectory, err = repository.preparedWorktreeGitDirectory(
			ctx,
			target,
			registered,
		)
		if err != nil {
			return workspaceRecord{}, err
		}
	}
	if err := repository.verifyPreparedWorktree(
		ctx,
		target,
		gitDirectory,
		registered,
	); err != nil {
		return workspaceRecord{}, err
	}

	bucket, err := m.openManagedBucketRoot(target)
	if err != nil {
		return workspaceRecord{}, err
	}
	defer func() {
		result = errors.Join(result, bucket.Close())
	}()
	opened, err := openRealRootFromRoot(bucket, target.AgentID)
	if err != nil {
		return workspaceRecord{}, err
	}
	defer func() {
		result = errors.Join(result, opened.Close())
	}()
	directoryIdentity, err := openedDirectoryIdentity(opened)
	if err != nil {
		return workspaceRecord{}, fmt.Errorf(
			"workspace: inspect prepared worktree identity: %w",
			err,
		)
	}
	if record.DirectoryIdentity != "" &&
		record.DirectoryIdentity != directoryIdentity {
		return workspaceRecord{}, errors.New(
			"workspace: prepared worktree directory identity changed",
		)
	}
	if err := verifyRootEntryUnchanged(
		bucket,
		target.AgentID,
		opened,
	); err != nil {
		return workspaceRecord{}, err
	}

	record.GitDirectory = gitDirectory
	record.DirectoryIdentity = directoryIdentity
	if err := m.replaceWorkspaceRecord(record); err != nil {
		return workspaceRecord{}, fmt.Errorf(
			"workspace: persist prepared worktree identity: %w",
			err,
		)
	}
	persisted, exists, err := m.readWorkspaceRecord(target.Path)
	if err != nil {
		return workspaceRecord{}, err
	}
	persistedTarget := target
	persistedTarget.gitDirectory = gitDirectory
	persistedTarget.directoryIdentity = directoryIdentity
	if !exists ||
		persisted.GitDirectory != gitDirectory ||
		persisted.DirectoryIdentity != directoryIdentity ||
		persisted.BranchOperationID != target.branchOperationID ||
		!sameWorkspace(persisted.workspace(), persistedTarget) {
		return workspaceRecord{}, errors.New(
			"workspace: prepared worktree identity was not persisted",
		)
	}
	currentIdentity, err := openedDirectoryIdentity(opened)
	if err != nil {
		return workspaceRecord{}, err
	}
	if currentIdentity != directoryIdentity {
		return workspaceRecord{}, errors.New(
			"workspace: prepared worktree identity changed after persistence",
		)
	}
	if err := verifyRootEntryUnchanged(
		bucket,
		target.AgentID,
		opened,
	); err != nil {
		return workspaceRecord{}, err
	}
	registered, exists, err = repository.worktreeRegistration(
		ctx,
		target.Path,
	)
	if err != nil {
		return workspaceRecord{}, err
	}
	if !exists {
		return workspaceRecord{}, errors.New(
			"workspace: prepared worktree registration disappeared",
		)
	}
	if err := repository.verifyPreparedWorktree(
		ctx,
		target,
		gitDirectory,
		registered,
	); err != nil {
		return workspaceRecord{}, err
	}
	return persisted, nil
}

func (m *Manager) removePreparedWorktreeAtRoot(
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
	openedOwned := true
	defer func() {
		if openedOwned {
			result = errors.Join(result, opened.Close())
		}
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
	openedOwned = false
	return removeOpenedDirectoryFromRoot(
		bucket,
		target.AgentID,
		opened,
	)
}
