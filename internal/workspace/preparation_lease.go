package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
)

type preparationLease struct {
	repository  repositoryCapability
	evidence    repositoryEvidence
	root        *os.Root
	guard       *repositoryGuard
	commonRoot  *os.Root
	commonGuard *repositoryGuard

	closeOnce sync.Once
	closeErr  error
}

func newPreparationLease(
	ctx context.Context,
	manager *Manager,
	path string,
	root *os.Root,
) (*preparationLease, error) {
	sourceIdentity, err := openedDirectoryIdentity(root)
	if err != nil {
		return nil, fmt.Errorf(
			"workspace: inspect source repository identity: %w",
			err,
		)
	}
	guard, err := openRepositoryGuard(path, root)
	if err != nil {
		return nil, err
	}
	repository := rootedRepositoryCapability(manager, path, root)
	commonPath, err := repository.commonGitDirectory(ctx)
	if err != nil {
		return nil, errors.Join(err, guard.Close())
	}
	commonRoot, err := openRealPathRoot(commonPath)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf(
				"workspace: open common Git directory: %w",
				err,
			),
			guard.Close(),
		)
	}
	commonIdentity, err := openedDirectoryIdentity(commonRoot)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf(
				"workspace: inspect common Git directory identity: %w",
				err,
			),
			commonRoot.Close(),
			guard.Close(),
		)
	}
	commonGuard, err := openRepositoryGuard(commonPath, commonRoot)
	if err != nil {
		return nil, errors.Join(
			err,
			commonRoot.Close(),
			guard.Close(),
		)
	}
	cleanup := func(result error) error {
		return errors.Join(
			result,
			commonGuard.Close(),
			commonRoot.Close(),
			guard.Close(),
		)
	}
	if err := verifyRealPathRoot(path, root); err != nil {
		return nil, cleanup(
			fmt.Errorf("workspace: verify source repository: %w", err),
		)
	}
	if err := verifyRealPathRoot(commonPath, commonRoot); err != nil {
		return nil, cleanup(
			fmt.Errorf("workspace: verify common Git directory: %w", err),
		)
	}
	confirmedCommonPath, err := repository.commonGitDirectory(ctx)
	if err != nil {
		return nil, cleanup(err)
	}
	if confirmedCommonPath != commonPath {
		return nil, cleanup(
			errors.New(
				"workspace: common Git directory changed while opening",
			),
		)
	}
	confirmedSourceIdentity, err := openedDirectoryIdentity(root)
	if err != nil {
		return nil, cleanup(err)
	}
	if confirmedSourceIdentity != sourceIdentity {
		return nil, cleanup(
			errors.New(
				"workspace: source repository identity changed while opening",
			),
		)
	}
	confirmedCommonIdentity, err := openedDirectoryIdentity(commonRoot)
	if err != nil {
		return nil, cleanup(err)
	}
	if confirmedCommonIdentity != commonIdentity {
		return nil, cleanup(
			errors.New(
				"workspace: common Git directory identity changed while opening",
			),
		)
	}
	return &preparationLease{
		repository: repository,
		evidence: repositoryEvidence{
			SourcePath:                 path,
			SourceDirectoryIdentity:    sourceIdentity,
			CommonGitDirectory:         commonPath,
			CommonGitDirectoryIdentity: commonIdentity,
		},
		root:        root,
		guard:       guard,
		commonRoot:  commonRoot,
		commonGuard: commonGuard,
	}, nil
}

func openRecordedPreparationLease(
	ctx context.Context,
	manager *Manager,
	record workspaceRecord,
) (*preparationLease, error) {
	if record.Version < workspaceRecordVersion ||
		record.RepositoryEvidence == nil {
		return nil, errors.New(
			"workspace: pending preparation has no repository identity evidence",
		)
	}
	evidence := *record.RepositoryEvidence
	if err := validateRepositoryEvidence(evidence); err != nil {
		return nil, err
	}
	root, err := openRealPathRoot(evidence.SourcePath)
	if err != nil {
		return nil, fmt.Errorf(
			"workspace: reopen source repository: %w",
			err,
		)
	}
	sourceIdentity, err := openedDirectoryIdentity(root)
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	if sourceIdentity != evidence.SourceDirectoryIdentity {
		return nil, errors.Join(
			errors.New(
				"workspace: source repository identity changed after restart",
			),
			root.Close(),
		)
	}
	lease, err := newPreparationLease(
		ctx,
		manager,
		evidence.SourcePath,
		root,
	)
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	if !sameRepositoryEvidence(lease.evidence, evidence) {
		return nil, errors.Join(
			errors.New(
				"workspace: repository identity evidence changed after restart",
			),
			lease.Close(),
		)
	}
	repository, err := manager.repositoryRootAtRoot(
		ctx,
		evidence.SourcePath,
		root,
	)
	if err != nil {
		return nil, errors.Join(err, lease.Close())
	}
	if repository != record.Repository {
		return nil, errors.Join(
			errors.New(
				"workspace: source repository root changed after restart",
			),
			lease.Close(),
		)
	}
	return lease, nil
}

func sameRepositoryEvidence(
	left repositoryEvidence,
	right repositoryEvidence,
) bool {
	return left == right
}

func (l *preparationLease) Close() error {
	if l == nil {
		return nil
	}
	l.closeOnce.Do(func() {
		l.closeErr = errors.Join(
			l.commonGuard.Close(),
			l.commonRoot.Close(),
			l.guard.Close(),
			l.root.Close(),
		)
	})
	return l.closeErr
}
