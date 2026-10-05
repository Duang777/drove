package workspace

import (
	"context"
	"errors"
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
