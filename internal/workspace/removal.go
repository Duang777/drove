package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/uuid"
)

// Removal identifies one durable workspace removal operation.
type Removal struct {
	Workspace Workspace

	operationID string
}

// RemovalState reports whether a removal changed physical workspace state.
type RemovalState uint8

const (
	// RemovalUnchanged means the workspace remains intact and has no pending intent.
	RemovalUnchanged RemovalState = iota
	// RemovalPending means a durable removal intent still requires reconciliation.
	RemovalPending
	// RemovalComplete means both the worktree path and Git registration are absent.
	RemovalComplete
)

// RemovalResult reports the physical outcome of one removal attempt.
type RemovalResult struct {
	Removal Removal
	State   RemovalState
}

func workspaceForRemoval(record workspaceRecord) Workspace {
	target := record.workspace()
	if record.Version < workspaceRecordVersion {
		target.repositoryEvidence = nil
	}
	return target
}

// Remove removes one managed worktree and preserves its branch.
func (m *Manager) Remove(
	ctx context.Context,
	agentID string,
	force bool,
) (RemovalResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.remove(ctx, agentID, force)
}

func (m *Manager) remove(
	ctx context.Context,
	agentID string,
	force bool,
) (RemovalResult, error) {
	if err := validateAgentID(agentID); err != nil {
		return RemovalResult{}, err
	}
	record, hasRecord, err := m.findWorkspaceRecord(agentID)
	if err != nil {
		return RemovalResult{}, err
	}
	if !hasRecord {
		target, findErr := m.findUnrecordedWorkspace(ctx, agentID)
		if findErr != nil {
			return RemovalResult{}, findErr
		}
		if !force {
			return RemovalResult{}, fmt.Errorf(
				"%w: workspace protection provenance is unknown",
				ErrDirty,
			)
		}
		record = newWorkspaceRecord(target, nil)
		record.ProtectionKnown = false
		if err := m.installWorkspaceRecord(record, true); err != nil {
			return RemovalResult{}, err
		}
	}

	if record.Removal == nil {
		record, err = m.ensureRemovalRepositoryEvidence(ctx, record)
		if err != nil {
			return RemovalResult{}, err
		}
	}
	removal := Removal{Workspace: workspaceForRemoval(record)}
	if record.Removal != nil {
		removal.operationID = record.Removal.OperationID
		if force && !record.Removal.Force {
			upgraded := *record.Removal
			upgraded.Force = true
			upgradeWorkspaceRecord(&record)
			record.Removal = &upgraded
			if err := m.replaceWorkspaceRecord(record); err != nil {
				pending := RemovalResult{
					Removal: removal,
					State:   RemovalPending,
				}
				return pending, fmt.Errorf(
					"workspace: upgrade removal intent to force: %w",
					err,
				)
			}
		}
		state, removalErr := m.resumePendingRemoval(ctx, record)
		return RemovalResult{Removal: removal, State: state}, removalErr
	}

	facts, err := m.removalFacts(ctx, record)
	if err != nil {
		return RemovalResult{}, err
	}
	if facts.pathExists {
		if err := m.validateRemovalIdentity(
			ctx,
			record,
			record.Path,
		); err != nil {
			return RemovalResult{}, err
		}
	}
	if !force {
		if err := m.validateRemovalSafety(ctx, record, facts); err != nil {
			return RemovalResult{}, err
		}
	}

	upgradeWorkspaceRecord(&record)
	if record.IncludedPaths == nil {
		record.IncludedPaths = []string{}
	}
	record.Removal = &workspaceRemovalRecord{
		OperationID:    uuid.NewString(),
		DirectoryToken: uuid.NewString(),
		Force:          force,
		PathAbsent:     !facts.pathExists,
	}
	removal.operationID = record.Removal.OperationID
	installed, err := m.replaceWorkspaceRecordState(record)
	if err != nil {
		state := RemovalUnchanged
		if installed {
			state = RemovalPending
		}
		return RemovalResult{Removal: removal, State: state}, err
	}

	state, removalErr := m.resumePendingRemoval(ctx, record)
	return RemovalResult{Removal: removal, State: state}, removalErr
}

// ReconcileRemovals completes durable removal intents left by an earlier process.
func (m *Manager) ReconcileRemovals(ctx context.Context) ([]Removal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.recoverRecordAcknowledgements(); err != nil {
		return nil, err
	}
	records, err := m.workspaceRecords()
	if err != nil {
		return nil, err
	}
	var completed []Removal
	for _, record := range records {
		if record.Removal == nil {
			continue
		}
		state, err := m.resumePendingRemoval(ctx, record)
		if state == RemovalUnchanged && errors.Is(err, ErrDirty) {
			continue
		}
		if state != RemovalComplete {
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf(
				"workspace: removal for agent %q did not complete",
				record.AgentID,
			)
		}
		completed = append(completed, Removal{
			Workspace:   workspaceForRemoval(record),
			operationID: record.Removal.OperationID,
		})
	}
	return completed, nil
}

// AcknowledgeRemoval removes a sidecar after its session tombstone is durable.
func (m *Manager) AcknowledgeRemoval(removal Removal) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.validateManagedPath(removal.Workspace); err != nil {
		return err
	}
	operationID, err := uuid.Parse(removal.operationID)
	if err != nil || operationID.String() != removal.operationID {
		return errors.New(
			"workspace: removal acknowledgement token is invalid",
		)
	}
	record := newWorkspaceRecord(removal.Workspace, nil)
	record.Removal = &workspaceRemovalRecord{
		OperationID: removal.operationID,
		Started:     true,
		Quarantined: true,
	}
	bucketAbsent, err := m.removalBucketAbsent(removal.Workspace)
	if err != nil {
		return err
	}
	if bucketAbsent {
		return nil
	}
	inspectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	facts, err := m.removalFacts(inspectCtx, record)
	if err != nil {
		return err
	}
	if facts.pathExists ||
		facts.quarantineName != "" ||
		facts.registered {
		return errors.New("workspace: cannot acknowledge an incomplete removal")
	}
	if err := m.removeAcknowledgedWorkspaceRecord(removal); err != nil {
		return err
	}
	return nil
}

func (m *Manager) removalBucketAbsent(
	target Workspace,
) (_ bool, result error) {
	root, err := m.openWorktreeRoot()
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer func() {
		result = errors.Join(result, root.Close())
	}()
	_, err = root.Lstat(repositoryHash(target.Repository))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("workspace: inspect repository bucket: %w", err)
	default:
		return false, nil
	}
}

type removalFacts struct {
	pathExists     bool
	quarantineName string
	registered     bool
	registration   registeredWorktree
}

func (m *Manager) removalRepository(
	ctx context.Context,
	record workspaceRecord,
) (repositoryCapability, func() error, error) {
	if record.Version < workspaceRecordVersion ||
		record.RepositoryEvidence == nil {
		return repositoryCapability{}, nil, errors.New(
			"workspace: pending removal has no repository identity evidence",
		)
	}
	lease, err := openRecordedPreparationLease(ctx, m, record)
	if err != nil {
		return repositoryCapability{}, nil, err
	}
	return lease.repository, lease.Close, nil
}

func (m *Manager) ensureRemovalRepositoryEvidence(
	ctx context.Context,
	record workspaceRecord,
) (_ workspaceRecord, result error) {
	if record.Version >= workspaceRecordVersion &&
		record.RepositoryEvidence != nil {
		return record, nil
	}
	if record.Removal != nil {
		return workspaceRecord{}, errors.New(
			"workspace: pending removal has no repository identity evidence",
		)
	}
	root, err := openRealPathRoot(record.Repository)
	if err != nil {
		return workspaceRecord{}, fmt.Errorf(
			"workspace: open removal repository: %w",
			err,
		)
	}
	rootOwned := true
	defer func() {
		if rootOwned {
			result = errors.Join(result, root.Close())
		}
	}()
	repository, err := m.repositoryRootAtRoot(
		ctx,
		record.Repository,
		root,
	)
	if err != nil {
		return workspaceRecord{}, err
	}
	if repository != record.Repository {
		return workspaceRecord{}, errors.New(
			"workspace: removal repository changed while opening",
		)
	}
	lease, err := newPreparationLease(
		ctx,
		m,
		record.Repository,
		root,
	)
	if err != nil {
		return workspaceRecord{}, fmt.Errorf(
			"workspace: retain removal repository: %w",
			err,
		)
	}
	rootOwned = false
	defer func() {
		result = errors.Join(result, lease.Close())
	}()

	upgradeWorkspaceRecord(&record)
	record.RepositoryEvidence = cloneRepositoryEvidence(&lease.evidence)
	if record.IncludedPaths == nil {
		record.IncludedPaths = []string{}
	}
	if err := m.replaceWorkspaceRecord(record); err != nil {
		return workspaceRecord{}, fmt.Errorf(
			"workspace: persist removal repository identity: %w",
			err,
		)
	}
	return record, nil
}

func (m *Manager) removalFacts(
	ctx context.Context,
	record workspaceRecord,
) (_ removalFacts, result error) {
	repository, closeRepository, err := m.removalRepository(ctx, record)
	if err != nil {
		return removalFacts{}, err
	}
	defer func() {
		result = errors.Join(result, closeRepository())
	}()
	bucket, err := m.openManagedBucketRoot(record.workspace())
	if err != nil {
		return removalFacts{}, err
	}
	defer func() {
		result = errors.Join(result, bucket.Close())
	}()
	info, err := bucket.Lstat(record.AgentID)
	pathExists := err == nil
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return removalFacts{}, fmt.Errorf(
			"workspace: inspect managed path %q: %w",
			record.Path,
			err,
		)
	case !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
		return removalFacts{}, fmt.Errorf(
			"workspace: managed path %q is not a real directory",
			record.Path,
		)
	}
	var quarantineName string
	if record.Removal != nil {
		quarantineName, _, err = findRemovalQuarantine(bucket, record)
		if err != nil {
			return removalFacts{}, fmt.Errorf(
				"workspace: inspect removal quarantine: %w",
				err,
			)
		}
	}
	registration, registered, err := repository.worktreeRegistration(
		ctx,
		record.Path,
	)
	if err != nil {
		return removalFacts{}, err
	}
	return removalFacts{
		pathExists:     pathExists,
		quarantineName: quarantineName,
		registered:     registered,
		registration:   registration,
	}, nil
}

func (m *Manager) validateRemovalSafety(
	ctx context.Context,
	record workspaceRecord,
	facts removalFacts,
) error {
	path := ""
	if facts.pathExists {
		path = record.Path
	}
	return m.validateRemovalSafetyAt(ctx, record, facts, path)
}

func (m *Manager) validateRemovalSafetyAt(
	ctx context.Context,
	record workspaceRecord,
	facts removalFacts,
	path string,
) error {
	pathExists := path != ""
	if !pathExists && !facts.registered {
		return nil
	}
	if !record.ProtectionKnown {
		return fmt.Errorf(
			"%w: workspace protection provenance is unknown",
			ErrDirty,
		)
	}
	if pathExists && !facts.registered {
		return fmt.Errorf(
			"%w: workspace path is no longer registered",
			ErrDirty,
		)
	}
	if facts.registered && facts.registration.detached {
		return fmt.Errorf(
			"%w: detached HEAD requires force for %s",
			ErrDirty,
			record.Path,
		)
	}
	if !pathExists {
		return nil
	}
	current, err := m.inspect(ctx, path, &record)
	if err != nil {
		return err
	}
	if current.Detached {
		return fmt.Errorf(
			"%w: detached HEAD requires force for %s",
			ErrDirty,
			record.Path,
		)
	}
	if current.Dirty {
		return fmt.Errorf("%w: %s", ErrDirty, path)
	}
	return nil
}

func (m *Manager) completeRemoval(
	ctx context.Context,
	record workspaceRecord,
) (RemovalState, error) {
	if record.Removal == nil || !record.Removal.Started {
		return RemovalPending, errors.New(
			"workspace: physical removal has not been started",
		)
	}
	facts, err := m.removalFacts(ctx, record)
	if err != nil {
		return RemovalPending, err
	}
	if err := validateRemovalPathState(record, facts); err != nil {
		return RemovalPending, err
	}
	if err := validateRemovalRecordIdentity(record, facts); err != nil {
		return RemovalPending, err
	}
	if !facts.pathExists &&
		facts.quarantineName == "" &&
		!facts.registered {
		return RemovalComplete, nil
	}
	if facts.quarantineName != "" {
		if err := m.removeRemovalQuarantine(
			ctx,
			record,
			facts.quarantineName,
		); err != nil {
			return m.classifyRemovalFailure(
				record,
				fmt.Errorf(
					"workspace: remove quarantined worktree: %w",
					err,
				),
			)
		}
	}
	if facts.registered {
		repository, closeRepository, err := m.removalRepository(ctx, record)
		if err != nil {
			return RemovalPending, err
		}
		pruneErr := repository.pruneWorktrees(ctx)
		closeErr := closeRepository()
		if err := errors.Join(pruneErr, closeErr); err != nil {
			return m.classifyRemovalFailure(
				record,
				fmt.Errorf(
					"workspace: prune worktree registration: %w",
					err,
				),
			)
		}
	}
	after, err := m.removalFacts(ctx, record)
	if err != nil {
		return RemovalPending, err
	}
	if after.pathExists ||
		after.quarantineName != "" ||
		after.registered {
		return RemovalPending, errors.New(
			"workspace: removal left a path, quarantine, or Git registration",
		)
	}
	return RemovalComplete, nil
}

func (m *Manager) resumePendingRemoval(
	ctx context.Context,
	record workspaceRecord,
) (RemovalState, error) {
	if record.Removal == nil {
		return RemovalPending, errors.New(
			"workspace: removal intent is missing",
		)
	}
	facts, err := m.removalFacts(ctx, record)
	if err != nil {
		return RemovalPending, err
	}
	if err := validateRemovalPathState(record, facts); err != nil {
		return RemovalPending, err
	}

	if facts.quarantineName != "" {
		bucket, openErr := m.openManagedBucketRoot(record.workspace())
		if openErr != nil {
			return RemovalPending, openErr
		}
		quarantined, openErr := openRealRootFromRoot(
			bucket,
			facts.quarantineName,
		)
		if openErr != nil {
			_ = bucket.Close()
			return RemovalPending, openErr
		}
		quarantinePath := filepath.Join(
			filepath.Dir(record.Path),
			facts.quarantineName,
		)
		identityErr := m.validateRemovalIdentity(
			ctx,
			record,
			quarantinePath,
		)
		var markerErr error
		if identityErr == nil {
			markerErr = validateRemovalMarkerPhase(
				quarantined,
				record,
			)
		}
		verifyErr := verifyRootEntryUnchanged(
			bucket,
			facts.quarantineName,
			quarantined,
		)
		var safetyErr error
		if identityErr == nil &&
			markerErr == nil &&
			verifyErr == nil &&
			!record.Removal.Force &&
			!record.Removal.Started {
			safetyErr = m.validateRemovalSafetyAt(
				ctx,
				record,
				facts,
				quarantinePath,
			)
		}
		closeErr := errors.Join(
			quarantined.Close(),
			bucket.Close(),
		)
		if err := errors.Join(
			identityErr,
			markerErr,
			verifyErr,
			safetyErr,
			closeErr,
		); err != nil {
			if safetyErr != nil &&
				errors.Is(safetyErr, ErrDirty) &&
				identityErr == nil &&
				markerErr == nil &&
				verifyErr == nil &&
				closeErr == nil {
				return m.rejectQuarantinedRemoval(
					ctx,
					record,
					facts.quarantineName,
					safetyErr,
				)
			}
			return RemovalPending, err
		}
		record, err = m.persistRemovalStart(record, true)
		if err != nil {
			return RemovalPending, err
		}
		return m.completeRemoval(ctx, record)
	}

	if !facts.pathExists {
		if !record.Removal.Force && !record.Removal.Started {
			if safetyErr := m.validateRemovalSafety(
				ctx,
				record,
				facts,
			); safetyErr != nil {
				return m.rejectPendingRemoval(record, facts, safetyErr)
			}
		}
		record, err = m.persistRemovalStart(record, false)
		if err != nil {
			return RemovalPending, err
		}
		return m.completeRemoval(ctx, record)
	}

	bucket, err := m.openManagedBucketRoot(record.workspace())
	if err != nil {
		return RemovalPending, err
	}
	opened, err := openRealRootFromRoot(bucket, record.AgentID)
	if err != nil {
		_ = bucket.Close()
		return RemovalPending, err
	}
	identityErr := m.validateRemovalIdentity(
		ctx,
		record,
		record.Path,
	)
	verifyErr := verifyRootEntryUnchanged(
		bucket,
		record.AgentID,
		opened,
	)
	var safetyErr error
	if !record.Removal.Force && !record.Removal.Started {
		safetyErr = m.validateRemovalSafetyAt(
			ctx,
			record,
			facts,
			record.Path,
		)
	}
	if err := errors.Join(identityErr, verifyErr, safetyErr); err != nil {
		_ = opened.Close()
		_ = bucket.Close()
		if safetyErr != nil &&
			errors.Is(safetyErr, ErrDirty) &&
			identityErr == nil &&
			verifyErr == nil {
			return m.rejectPendingRemoval(record, facts, safetyErr)
		}
		return RemovalPending, err
	}
	markerErr := validateRemovalMarkerPhase(opened, record)
	verifyErr = verifyRootEntryUnchanged(
		bucket,
		record.AgentID,
		opened,
	)
	if err := errors.Join(markerErr, verifyErr); err != nil {
		_ = opened.Close()
		_ = bucket.Close()
		return RemovalPending, err
	}
	quarantineName, _, quarantineErr := quarantineManagedPath(
		bucket,
		record,
		opened,
	)
	closeErr := errors.Join(opened.Close(), bucket.Close())
	if err := errors.Join(quarantineErr, closeErr); err != nil {
		return RemovalPending, fmt.Errorf(
			"workspace: quarantine managed worktree: %w",
			err,
		)
	}
	if quarantineName == "" {
		return RemovalPending, errors.New(
			"workspace: managed worktree was not quarantined",
		)
	}
	return m.resumePendingRemoval(ctx, record)
}

func validateRemovalPathState(
	record workspaceRecord,
	facts removalFacts,
) error {
	if record.Removal == nil ||
		!record.Removal.PathAbsent ||
		(!facts.pathExists && facts.quarantineName == "") {
		return nil
	}
	return fmt.Errorf(
		"workspace: managed path %q appeared after removal began",
		record.Path,
	)
}

func validateRemovalRecordIdentity(
	record workspaceRecord,
	facts removalFacts,
) error {
	if !facts.pathExists && facts.quarantineName == "" {
		return nil
	}
	if record.GitDirectory == "" {
		return errors.New("workspace: removal record has no Git directory identity")
	}
	if record.DirectoryIdentity == "" {
		return errors.New(
			"workspace: removal record has no worktree directory identity",
		)
	}
	if record.Removal == nil || record.Removal.DirectoryToken == "" {
		return errors.New("workspace: removal record has no directory token")
	}
	return nil
}

func (m *Manager) persistRemovalStart(
	record workspaceRecord,
	quarantined bool,
) (workspaceRecord, error) {
	if record.Removal == nil {
		return workspaceRecord{}, errors.New(
			"workspace: removal intent is missing",
		)
	}
	if record.Removal.Started &&
		(!quarantined || record.Removal.Quarantined) {
		return record, nil
	}
	started := *record.Removal
	started.Started = true
	started.Quarantined = started.Quarantined || quarantined
	upgradeWorkspaceRecord(&record)
	record.Removal = &started
	_, err := m.replaceWorkspaceRecordState(record)
	if err != nil {
		return workspaceRecord{}, fmt.Errorf(
			"workspace: persist physical removal phase: %w",
			err,
		)
	}
	return record, nil
}

func (m *Manager) validateRemovalIdentity(
	ctx context.Context,
	record workspaceRecord,
	path string,
) error {
	directoryIdentity, err := worktreeDirectoryIdentity(path)
	if err != nil {
		return err
	}
	if record.DirectoryIdentity == "" {
		return errors.New(
			"workspace: removal record has no worktree directory identity",
		)
	}
	if directoryIdentity != record.DirectoryIdentity {
		return fmt.Errorf(
			"workspace: directory identity mismatch for removal path %q",
			path,
		)
	}
	repository, err := m.repositoryRoot(ctx, path)
	if err != nil {
		if record.Removal != nil &&
			record.Removal.DirectoryToken != "" {
			return validateRemovalMarkerPath(path, record)
		}
		return err
	}
	if repository != record.Repository {
		return fmt.Errorf(
			"workspace: repository mismatch for quarantined path %q",
			path,
		)
	}
	gitDirectory, err := m.worktreeGitDirectory(ctx, path)
	if err != nil {
		return err
	}
	if record.GitDirectory == "" {
		return errors.New("workspace: removal record has no Git directory identity")
	}
	if gitDirectory != record.GitDirectory {
		return fmt.Errorf(
			"workspace: Git directory mismatch for removal path %q",
			path,
		)
	}
	return nil
}

func validateRemovalMarkerPath(
	path string,
	record workspaceRecord,
) (result error) {
	root, err := openRealPathRoot(path)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, root.Close())
	}()
	return verifyRemovalMarker(root, record)
}

func (m *Manager) removeRemovalQuarantine(
	ctx context.Context,
	record workspaceRecord,
	name string,
) (result error) {
	bucket, err := m.openManagedBucketRoot(record.workspace())
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, bucket.Close())
	}()
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
	path := filepath.Join(filepath.Dir(record.Path), name)
	if err := errors.Join(
		m.validateRemovalIdentity(ctx, record, path),
		verifyRemovalMarker(opened, record),
		verifyRootEntryUnchanged(bucket, name, opened),
	); err != nil {
		return err
	}
	openedOwned = false
	return removeOpenedDirectoryFromRoot(bucket, name, opened)
}

func (m *Manager) rejectQuarantinedRemoval(
	ctx context.Context,
	record workspaceRecord,
	quarantineName string,
	safetyErr error,
) (RemovalState, error) {
	bucket, err := m.openManagedBucketRoot(record.workspace())
	if err != nil {
		return RemovalPending, errors.Join(safetyErr, err)
	}
	restoreErr := restoreManagedQuarantine(
		bucket,
		record,
		quarantineName,
	)
	closeErr := bucket.Close()
	if err := errors.Join(restoreErr, closeErr); err != nil {
		return RemovalPending, errors.Join(safetyErr, err)
	}
	facts, err := m.removalFacts(ctx, record)
	if err != nil {
		return RemovalPending, errors.Join(safetyErr, err)
	}
	return m.rejectPendingRemoval(record, facts, safetyErr)
}

func (m *Manager) clearPendingRemovalMarker(
	record workspaceRecord,
) (result error) {
	if record.Removal == nil {
		return errors.New("workspace: removal intent is missing")
	}
	bucket, err := m.openManagedBucketRoot(record.workspace())
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, bucket.Close())
	}()
	opened, err := openRealRootFromRoot(bucket, record.AgentID)
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
	if record.DirectoryIdentity == "" ||
		directoryIdentity != record.DirectoryIdentity {
		return errors.New(
			"workspace: pending removal directory identity changed",
		)
	}
	if err := verifyRootEntryUnchanged(
		bucket,
		record.AgentID,
		opened,
	); err != nil {
		return err
	}
	if err := removeRemovalMarkerIfPresent(opened, record); err != nil {
		return err
	}
	return verifyRootEntryUnchanged(
		bucket,
		record.AgentID,
		opened,
	)
}

func (m *Manager) rejectPendingRemoval(
	record workspaceRecord,
	facts removalFacts,
	safetyErr error,
) (RemovalState, error) {
	wrapped := fmt.Errorf(
		"workspace: revalidate pending removal for agent %q: %w",
		record.AgentID,
		safetyErr,
	)
	if record.Removal != nil && record.Removal.Started {
		return RemovalPending, wrapped
	}
	if !errors.Is(safetyErr, ErrDirty) ||
		!facts.pathExists ||
		!facts.registered {
		return RemovalPending, wrapped
	}
	if err := m.clearPendingRemovalMarker(record); err != nil {
		return RemovalPending, errors.Join(wrapped, err)
	}
	record.Removal = nil
	if err := m.replaceWorkspaceRecord(record); err != nil {
		return RemovalPending, errors.Join(wrapped, err)
	}
	return RemovalUnchanged, wrapped
}

func (m *Manager) classifyRemovalFailure(
	record workspaceRecord,
	cause error,
) (RemovalState, error) {
	inspectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	facts, err := m.removalFacts(inspectCtx, record)
	if err != nil {
		return RemovalPending, errors.Join(cause, err)
	}
	if !facts.pathExists &&
		facts.quarantineName == "" &&
		!facts.registered {
		return RemovalComplete, cause
	}
	return RemovalPending, cause
}

func (m *Manager) findWorkspaceRecord(
	agentID string,
) (workspaceRecord, bool, error) {
	if err := m.recoverRecordAcknowledgements(); err != nil {
		return workspaceRecord{}, false, err
	}
	records, err := m.workspaceRecords()
	if err != nil {
		return workspaceRecord{}, false, err
	}
	var matches []workspaceRecord
	for _, record := range records {
		if record.AgentID == agentID {
			matches = append(matches, record)
		}
	}
	switch len(matches) {
	case 0:
		return workspaceRecord{}, false, nil
	case 1:
		return matches[0], true, nil
	default:
		return workspaceRecord{}, false, fmt.Errorf(
			"workspace: agent %q has %d managed records",
			agentID,
			len(matches),
		)
	}
}

func (m *Manager) workspaceRecords() (records []workspaceRecord, resultErr error) {
	root, err := m.openWorktreeRoot()
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	buckets, err := readRootDirectory(root)
	if err != nil {
		return nil, fmt.Errorf("workspace: read worktree root: %w", err)
	}
	for _, bucket := range buckets {
		if !validRepositoryHash(bucket.Name()) {
			continue
		}
		bucketRoot, err := openRealRootFromRoot(root, bucket.Name())
		if err != nil {
			return nil, fmt.Errorf(
				"workspace: open repository bucket %q: %w",
				bucket.Name(),
				err,
			)
		}
		if err := m.verifyRepositoryBucket(bucket.Name(), bucketRoot); err != nil {
			_ = bucketRoot.Close()
			return nil, err
		}
		entries, err := readRootDirectory(bucketRoot)
		if err != nil {
			_ = bucketRoot.Close()
			return nil, fmt.Errorf(
				"workspace: read repository bucket %q: %w",
				bucket.Name(),
				err,
			)
		}
		for _, entry := range entries {
			agentID, ok := workspaceRecordAgentID(entry.Name())
			if !ok {
				continue
			}
			record, exists, err := m.readWorkspaceRecord(
				filepath.Join(m.root, bucket.Name(), agentID),
			)
			if err != nil {
				_ = bucketRoot.Close()
				return nil, err
			}
			if exists {
				records = append(records, record)
			}
		}
		if err := bucketRoot.Close(); err != nil {
			return nil, fmt.Errorf(
				"workspace: close repository bucket %q: %w",
				bucket.Name(),
				err,
			)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Repository == records[j].Repository {
			return records[i].AgentID < records[j].AgentID
		}
		return records[i].Repository < records[j].Repository
	})
	return records, nil
}

func (m *Manager) findUnrecordedWorkspace(
	ctx context.Context,
	agentID string,
) (Workspace, error) {
	all, err := m.list(ctx)
	if err != nil {
		return Workspace{}, err
	}
	var matches []Workspace
	for _, current := range all {
		if current.AgentID == agentID {
			matches = append(matches, current)
		}
	}
	switch len(matches) {
	case 0:
		return Workspace{}, fmt.Errorf("%w: agent %q", ErrNotFound, agentID)
	case 1:
		return matches[0], nil
	default:
		return Workspace{}, fmt.Errorf(
			"workspace: agent %q has %d managed worktrees",
			agentID,
			len(matches),
		)
	}
}
