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

	removal := Removal{Workspace: record.workspace()}
	if record.Removal != nil {
		removal.operationID = record.Removal.OperationID
		state, removalErr := m.completeRemoval(ctx, record)
		return RemovalResult{Removal: removal, State: state}, removalErr
	}

	before, err := m.removalFacts(ctx, record)
	if err != nil {
		return RemovalResult{}, err
	}
	if !force {
		if err := m.validateRemovalSafety(ctx, record, before); err != nil {
			return RemovalResult{}, err
		}
	}

	record.Version = workspaceRecordVersion
	if record.IncludedPaths == nil {
		record.IncludedPaths = []string{}
	}
	record.Removal = &workspaceRemovalRecord{
		OperationID: uuid.NewString(),
		Force:       force,
	}
	if err := m.replaceWorkspaceRecord(record); err != nil {
		return RemovalResult{}, err
	}
	removal.operationID = record.Removal.OperationID

	if !force {
		current, safetyErr := m.removalFacts(ctx, record)
		if safetyErr == nil {
			safetyErr = m.validateRemovalSafety(ctx, record, current)
		}
		if safetyErr != nil {
			state, rollbackErr := m.rollbackRemovalIntent(ctx, record, before)
			return RemovalResult{Removal: removal, State: state}, errors.Join(
				safetyErr,
				rollbackErr,
			)
		}
	}

	state, removalErr := m.completeRemoval(ctx, record)
	if state == RemovalPending && removalErr != nil {
		rollbackState, rollbackErr := m.rollbackRemovalIntent(ctx, record, before)
		if rollbackState == RemovalUnchanged {
			state = RemovalUnchanged
		}
		removalErr = errors.Join(removalErr, rollbackErr)
	}
	return RemovalResult{Removal: removal, State: state}, removalErr
}

// ReconcileRemovals completes durable removal intents left by an earlier process.
func (m *Manager) ReconcileRemovals(ctx context.Context) ([]Removal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	records, err := m.workspaceRecords()
	if err != nil {
		return nil, err
	}
	var completed []Removal
	for _, record := range records {
		if record.Removal == nil {
			continue
		}
		facts, err := m.removalFacts(ctx, record)
		if err != nil {
			return nil, err
		}
		if facts.pathExists && facts.registered && !record.Removal.Force {
			if err := m.validateRemovalSafety(ctx, record, facts); err != nil {
				if !errors.Is(err, ErrDirty) {
					return nil, fmt.Errorf(
						"workspace: revalidate pending removal for agent %q: %w",
						record.AgentID,
						err,
					)
				}
				record.Removal = nil
				if replaceErr := m.replaceWorkspaceRecord(record); replaceErr != nil {
					return nil, errors.Join(err, replaceErr)
				}
				continue
			}
		}
		state, err := m.completeRemoval(ctx, record)
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
			Workspace:   record.workspace(),
			operationID: record.Removal.OperationID,
		})
	}
	return completed, nil
}

// AcknowledgeRemoval removes a sidecar after its session tombstone is durable.
func (m *Manager) AcknowledgeRemoval(removal Removal) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	record, exists, err := m.readWorkspaceRecord(removal.Workspace.Path)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if record.AgentID != removal.Workspace.AgentID ||
		record.Repository != removal.Workspace.Repository ||
		record.Path != removal.Workspace.Path ||
		record.Branch != removal.Workspace.Branch {
		return errors.New("workspace: removal acknowledgement does not match record")
	}
	if record.Removal == nil ||
		record.Removal.OperationID != removal.operationID {
		return errors.New("workspace: removal acknowledgement token does not match")
	}
	inspectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	facts, err := m.removalFacts(inspectCtx, record)
	if err != nil {
		return err
	}
	if facts.pathExists || facts.registered {
		return errors.New("workspace: cannot acknowledge an incomplete removal")
	}
	if err := removeWorkspaceRecord(record.Path); err != nil {
		return err
	}
	if err := removeEmptyDirectory(filepath.Dir(record.Path)); err != nil {
		return err
	}
	return nil
}

type removalFacts struct {
	pathExists bool
	registered bool
}

func (m *Manager) removalFacts(
	ctx context.Context,
	record workspaceRecord,
) (removalFacts, error) {
	info, err := os.Lstat(record.Path)
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
	registered, err := m.worktreeRegistered(ctx, record.Repository, record.Path)
	if err != nil {
		return removalFacts{}, err
	}
	return removalFacts{pathExists: pathExists, registered: registered}, nil
}

func (m *Manager) validateRemovalSafety(
	ctx context.Context,
	record workspaceRecord,
	facts removalFacts,
) error {
	if !record.ProtectionKnown {
		return fmt.Errorf(
			"%w: workspace protection provenance is unknown",
			ErrDirty,
		)
	}
	if facts.pathExists && !facts.registered {
		return fmt.Errorf(
			"%w: workspace path is no longer registered",
			ErrDirty,
		)
	}
	if !facts.pathExists {
		return nil
	}
	current, err := m.inspect(ctx, record.Path, &record)
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
		return fmt.Errorf("%w: %s", ErrDirty, record.Path)
	}
	return nil
}

func (m *Manager) completeRemoval(
	ctx context.Context,
	record workspaceRecord,
) (RemovalState, error) {
	facts, err := m.removalFacts(ctx, record)
	if err != nil {
		return RemovalPending, err
	}
	switch {
	case !facts.pathExists && !facts.registered:
		return RemovalComplete, nil
	case facts.pathExists && facts.registered:
		arguments := []string{"-C", record.Repository, "worktree", "remove"}
		if record.Removal.Force {
			arguments = append(arguments, "--force")
		}
		arguments = append(arguments, record.Path)
		if _, err := m.run(ctx, arguments...); err != nil {
			return m.classifyRemovalFailure(
				record,
				fmt.Errorf("workspace: remove worktree: %w", err),
			)
		}
	case !facts.pathExists && facts.registered:
		if _, err := m.run(
			ctx,
			"-C",
			record.Repository,
			"worktree",
			"remove",
			"--force",
			record.Path,
		); err != nil {
			return m.classifyRemovalFailure(
				record,
				fmt.Errorf(
					"workspace: remove missing worktree registration: %w",
					err,
				),
			)
		}
	case facts.pathExists && !facts.registered:
		if err := m.removeManagedPath(record.workspace()); err != nil {
			return m.classifyRemovalFailure(
				record,
				fmt.Errorf(
					"workspace: remove unregistered managed path: %w",
					err,
				),
			)
		}
	}
	after, err := m.removalFacts(ctx, record)
	if err != nil {
		return RemovalPending, err
	}
	if after.pathExists || after.registered {
		return RemovalPending, errors.New(
			"workspace: removal left a path or Git registration",
		)
	}
	return RemovalComplete, nil
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
	if !facts.pathExists && !facts.registered {
		return RemovalComplete, cause
	}
	return RemovalPending, cause
}

func (m *Manager) rollbackRemovalIntent(
	ctx context.Context,
	record workspaceRecord,
	before removalFacts,
) (RemovalState, error) {
	after, err := m.removalFacts(ctx, record)
	if err != nil {
		return RemovalPending, err
	}
	if after != before {
		return RemovalPending, nil
	}
	record.Removal = nil
	if err := m.replaceWorkspaceRecord(record); err != nil {
		return RemovalPending, err
	}
	return RemovalUnchanged, nil
}

func (m *Manager) findWorkspaceRecord(
	agentID string,
) (workspaceRecord, bool, error) {
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

func (m *Manager) workspaceRecords() ([]workspaceRecord, error) {
	info, err := os.Lstat(m.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("workspace: inspect worktree root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("workspace: worktree root %q is not a real directory", m.root)
	}
	buckets, err := os.ReadDir(m.root)
	if err != nil {
		return nil, fmt.Errorf("workspace: read worktree root: %w", err)
	}
	var records []workspaceRecord
	for _, bucket := range buckets {
		if !bucket.IsDir() || !validRepositoryHash(bucket.Name()) {
			continue
		}
		bucketPath := filepath.Join(m.root, bucket.Name())
		entries, err := os.ReadDir(bucketPath)
		if err != nil {
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
				filepath.Join(bucketPath, agentID),
			)
			if err != nil {
				return nil, err
			}
			if exists {
				records = append(records, record)
			}
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
