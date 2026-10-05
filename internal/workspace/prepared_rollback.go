package workspace

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
)

func (m *Manager) removePreparedWorktreeAtRoot(
	ctx context.Context,
	target Workspace,
	record workspaceRecord,
	repository repositoryCapability,
	allowIncompleteGitIdentity bool,
) (result error) {
	if record.DirectoryIdentity == "" {
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
	if record.GitDirectory != "" && !exists {
		repaired, repairErr := m.repairPromotedPreparedWorktree(
			ctx,
			target,
			record,
			repository,
		)
		if repairErr != nil {
			return repairErr
		}
		if repaired {
			registered, exists, err = repository.worktreeRegistration(
				ctx,
				target.Path,
			)
			if err != nil {
				return err
			}
		}
	}
	switch {
	case record.GitDirectory != "" && !exists:
		return errors.New(
			"workspace: prepared worktree registration disappeared",
		)
	case record.GitDirectory != "":
		if err := repository.verifyPreparedWorktree(
			ctx,
			target,
			record.GitDirectory,
			registered,
		); err != nil {
			return err
		}
	case !allowIncompleteGitIdentity:
		return errors.New(
			"workspace: prepared worktree identity is incomplete",
		)
	case exists:
		expectedHeadOID := repository.startOID
		if !target.createdBranch {
			var branchExists bool
			expectedHeadOID, branchExists, err = repository.refOID(
				ctx,
				"refs/heads/"+target.Branch,
			)
			if err != nil {
				return err
			}
			if !branchExists {
				return errors.New(
					"workspace: prepared worktree branch disappeared",
				)
			}
		}
		if expectedHeadOID == "" {
			return errors.New(
				"workspace: prepared worktree expected HEAD is unavailable",
			)
		}
		if err := repository.verifyPreparedWorktreeRegistration(
			target,
			registered,
			expectedHeadOID,
		); err != nil {
			return err
		}
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

func (m *Manager) repairPromotedPreparedWorktree(
	ctx context.Context,
	target Workspace,
	record workspaceRecord,
	repository repositoryCapability,
) (result bool, resultErr error) {
	if record.BranchOperationID == "" {
		return false, nil
	}
	stagingName, err := preparedWorktreeStagingName(
		target.AgentID,
		record.BranchOperationID,
	)
	if err != nil {
		return false, err
	}
	stagingPath := filepath.Join(filepath.Dir(target.Path), stagingName)
	stale, exists, err := repository.worktreeRegistration(ctx, stagingPath)
	if err != nil || !exists {
		return false, err
	}
	stagingTarget := target
	stagingTarget.Path = stagingPath
	if err := repository.verifyPreparedWorktree(
		ctx,
		stagingTarget,
		record.GitDirectory,
		stale,
	); err != nil {
		return false, fmt.Errorf(
			"workspace: verify promoted worktree staging registration: %w",
			err,
		)
	}

	bucket, err := m.openManagedBucketRoot(target)
	if err != nil {
		return false, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, bucket.Close())
	}()
	opened, err := openRealRootFromRoot(bucket, target.AgentID)
	if err != nil {
		return false, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, opened.Close())
	}()
	identity, err := openedDirectoryIdentity(opened)
	if err != nil {
		return false, err
	}
	if identity != record.DirectoryIdentity {
		return false, errors.New(
			"workspace: promoted worktree directory identity changed",
		)
	}
	currentRepository, err := m.repositoryRootAtRoot(
		ctx,
		target.Path,
		opened,
	)
	if err != nil {
		return false, err
	}
	if currentRepository != record.Repository {
		return false, errors.New(
			"workspace: promoted worktree repository changed",
		)
	}
	gitDirectory, err := m.worktreeGitDirectoryAtRoot(
		ctx,
		target.Path,
		opened,
	)
	if err != nil {
		return false, err
	}
	if gitDirectory != record.GitDirectory {
		return false, errors.New(
			"workspace: promoted worktree Git directory changed",
		)
	}
	if err := verifyRootEntryUnchanged(
		bucket,
		target.AgentID,
		opened,
	); err != nil {
		return false, err
	}
	if _, err := m.runRootedGit(
		ctx,
		target.Path,
		opened,
		"worktree",
		"repair",
		".",
	); err != nil {
		return false, fmt.Errorf(
			"workspace: repair promoted worktree registration: %w",
			err,
		)
	}
	if err := verifyRootEntryUnchanged(
		bucket,
		target.AgentID,
		opened,
	); err != nil {
		return false, err
	}
	current, exists, err := repository.worktreeRegistration(ctx, target.Path)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, errors.New(
			"workspace: repaired worktree registration is missing",
		)
	}
	if err := repository.verifyPreparedWorktree(
		ctx,
		target,
		record.GitDirectory,
		current,
	); err != nil {
		return false, err
	}
	if _, exists, err := repository.worktreeRegistration(
		ctx,
		stagingPath,
	); err != nil {
		return false, err
	} else if exists {
		return false, errors.New(
			"workspace: stale staging registration remains after repair",
		)
	}
	return true, nil
}
