package session

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/workspace"
	"github.com/google/uuid"
)

const workspaceRemovedReason = "workspace_removed"

var (
	// ErrWorkspaceUnavailable means the daemon cannot manage Git worktrees.
	ErrWorkspaceUnavailable = errors.New("session: workspace manager unavailable")
	// ErrWorkspaceRequest means a worktree request has an invalid repository or branch.
	ErrWorkspaceRequest = errors.New("session: invalid workspace request")
	// ErrWorkspacePrepare means a requested Git worktree could not be prepared.
	ErrWorkspacePrepare = errors.New("session: prepare workspace")
	// ErrWorkspaceInUse means an active or resuming session still owns the worktree.
	ErrWorkspaceInUse = errors.New("session: workspace is in use")
	// ErrWorkspaceDirty means cleanup requires explicit force.
	ErrWorkspaceDirty = errors.New("session: workspace has uncommitted changes")
	// ErrWorkspaceNotFound means no managed worktree matches the Agent ID.
	ErrWorkspaceNotFound = errors.New("session: workspace not found")
)

// WorktreeRequest asks Start to create an isolated Git worktree.
type WorktreeRequest struct {
	// Branch selects a local branch. Empty creates drove/<agent-id>.
	Branch string `json:"branch,omitempty"`
}

type workspaceMetadata struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Branch     string `json:"branch"`
}

type workspaceRemovedPayload struct {
	Version int `json:"version"`
}

type workspaceLifecycle interface {
	Prepare(
		context.Context,
		string,
		string,
		string,
	) (workspace.Workspace, error)
	Discard(context.Context, workspace.Workspace) error
	Remove(context.Context, string, bool) (workspace.RemovalResult, error)
	ReconcileRemovals(context.Context) ([]workspace.Removal, error)
	AcknowledgeRemoval(workspace.Removal) error
}

// WithWorkspaces enables managed Git worktrees below dataDir.
func WithWorkspaces(dataDir string) ManagerOption {
	return func(manager *Manager) {
		workspaces, err := workspace.New(dataDir)
		if err != nil {
			manager.workspaceErr = err
			return
		}
		manager.workspaces = workspaces
	}
}

func (m *Manager) prepareWorkspace(
	ctx context.Context,
	id string,
	request *WorktreeRequest,
	sourceDir string,
) (*workspace.Workspace, error) {
	if request == nil {
		return nil, nil
	}
	if m.workspaceErr != nil {
		return nil, errors.Join(ErrWorkspaceUnavailable, m.workspaceErr)
	}
	if m.workspaces == nil {
		return nil, ErrWorkspaceUnavailable
	}
	prepared, err := m.workspaces.Prepare(
		ctx,
		sourceDir,
		request.Branch,
		id,
	)
	if err != nil {
		if errors.Is(err, workspace.ErrNotRepository) ||
			errors.Is(err, workspace.ErrInvalidBranch) {
			return nil, errors.Join(ErrWorkspaceRequest, err)
		}
		return nil, errors.Join(ErrWorkspacePrepare, err)
	}
	return &prepared, nil
}

func (m *Manager) discardWorkspace(target *workspace.Workspace) error {
	if target == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.workspaces.Discard(ctx, *target); err != nil {
		return fmt.Errorf("session: discard uncommitted workspace: %w", err)
	}
	return nil
}

// CleanupWorkspace removes a terminal Agent's managed worktree.
func (m *Manager) CleanupWorkspace(
	ctx context.Context,
	id string,
	force bool,
) (workspace.Workspace, error) {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return workspace.Workspace{}, fmt.Errorf(
			"%w: invalid Agent ID %q",
			ErrWorkspaceRequest,
			id,
		)
	}
	if m.workspaceErr != nil {
		return workspace.Workspace{}, errors.Join(
			ErrWorkspaceUnavailable,
			m.workspaceErr,
		)
	}
	if m.workspaces == nil {
		return workspace.Workspace{}, ErrWorkspaceUnavailable
	}
	agentID := agent.ID(id)
	completion, err := m.reserveWorkspaceCleanup(agentID)
	if err != nil {
		return workspace.Workspace{}, err
	}
	defer m.releaseWorkspaceCleanup(agentID, completion)

	result, removalErr := m.workspaces.Remove(ctx, id, force)
	removed := result.Removal.Workspace
	switch result.State {
	case workspace.RemovalUnchanged:
		if removalErr == nil {
			removalErr = errors.New(
				"workspace removal stopped without changing physical state",
			)
		}
		return workspace.Workspace{}, mapWorkspaceRemovalError(removalErr)
	case workspace.RemovalPending:
		if managed, known := m.managed(agentID); known {
			managed.setWorkspaceRemovalPending()
		}
		if removalErr == nil {
			removalErr = errors.New(
				"workspace removal requires restart reconciliation",
			)
		}
		return workspace.Workspace{}, fmt.Errorf(
			"session: cleanup workspace pending: %w",
			removalErr,
		)
	case workspace.RemovalComplete:
	default:
		return workspace.Workspace{}, fmt.Errorf(
			"session: cleanup workspace returned invalid removal state %d",
			result.State,
		)
	}

	managed, known := m.managed(agentID)
	if !known {
		ackErr := m.workspaces.AcknowledgeRemoval(result.Removal)
		if err := errors.Join(removalErr, ackErr); err != nil {
			return workspace.Workspace{}, fmt.Errorf(
				"session: finalize orphan workspace removal: %w",
				err,
			)
		}
		return removed, nil
	}
	if managed.workspaceState().removed {
		ackErr := m.workspaces.AcknowledgeRemoval(result.Removal)
		if err := errors.Join(removalErr, ackErr); err != nil {
			return workspace.Workspace{}, fmt.Errorf(
				"session: acknowledge durable workspace removal: %w",
				err,
			)
		}
		return removed, nil
	}
	managed.setWorkspaceRemovalPending()
	receipt, commitErr := m.committer.CommitWorkspaceRemoved(
		context.Background(),
		managed,
	)
	var acknowledgeErr error
	if receipt.Durable {
		acknowledgeErr = m.workspaces.AcknowledgeRemoval(result.Removal)
	}
	if err := errors.Join(removalErr, commitErr, acknowledgeErr); err != nil {
		return workspace.Workspace{}, fmt.Errorf(
			"session: persist workspace removal: %w",
			err,
		)
	}
	return removed, nil
}

func mapWorkspaceRemovalError(err error) error {
	switch {
	case errors.Is(err, workspace.ErrDirty):
		return errors.Join(ErrWorkspaceDirty, err)
	case errors.Is(err, workspace.ErrNotFound):
		return errors.Join(ErrWorkspaceNotFound, err)
	default:
		return fmt.Errorf("session: cleanup workspace: %w", err)
	}
}

func (m *Manager) reserveWorkspaceCleanup(
	id agent.ID,
) (chan struct{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrManagerClosed
	}
	if _, cleaning := m.cleaning[id]; cleaning {
		return nil, fmt.Errorf(
			"%w: agent %q cleanup is already running",
			ErrWorkspaceInUse,
			id,
		)
	}
	if _, creating := m.creating[id]; creating {
		return nil, fmt.Errorf(
			"%w: agent %q is being created",
			ErrWorkspaceInUse,
			id,
		)
	}
	if managed, ok := m.agents[id]; ok {
		_, attached := m.sessions[id]
		_, resuming := m.resuming[id]
		state := managed.agent.State()
		if attached || resuming ||
			(state != agent.StateDone && state != agent.StateStopped) {
			return nil, fmt.Errorf(
				"%w: agent %q is %s",
				ErrWorkspaceInUse,
				id,
				state,
			)
		}
	}
	completion := make(chan struct{})
	m.cleanups.Add(1)
	m.cleaning[id] = completion
	return completion, nil
}

func (m *Manager) releaseWorkspaceCleanup(
	id agent.ID,
	completion chan struct{},
) {
	m.mu.Lock()
	if current, ok := m.cleaning[id]; ok && current == completion {
		delete(m.cleaning, id)
		close(completion)
	}
	m.mu.Unlock()
	m.cleanups.Done()
}

func (m *Manager) reserveWorkspaceCreation(id agent.ID) {
	m.mu.Lock()
	m.creating[id] = struct{}{}
	m.mu.Unlock()
}

func (m *Manager) releaseWorkspaceCreation(id agent.ID) {
	m.mu.Lock()
	delete(m.creating, id)
	m.mu.Unlock()
}

func (m *Manager) reconcileWorkspaceRemovals(
	ctx context.Context,
	metadata map[string]workspaceMetadata,
) error {
	if m.workspaceErr != nil {
		return errors.Join(
			ErrWorkspaceUnavailable,
			fmt.Errorf("session: initialize workspace manager: %w", m.workspaceErr),
		)
	}
	if m.workspaces == nil {
		return nil
	}
	removals, err := m.workspaces.ReconcileRemovals(ctx)
	if err != nil {
		return fmt.Errorf("session: reconcile physical workspace removals: %w", err)
	}
	for _, removal := range removals {
		id := agent.ID(removal.Workspace.AgentID)
		managed, known := m.managed(id)
		if !known {
			if err := m.workspaces.AcknowledgeRemoval(removal); err != nil {
				return fmt.Errorf(
					"session: acknowledge orphan workspace removal %q: %w",
					id,
					err,
				)
			}
			continue
		}
		expected, ok := metadata[string(id)]
		if !ok ||
			expected.Repository != removal.Workspace.Repository ||
			expected.Path != removal.Workspace.Path ||
			expected.Branch != removal.Workspace.Branch {
			return fmt.Errorf(
				"session: workspace removal %q does not match session metadata",
				id,
			)
		}
		if managed.workspaceState().removed {
			if err := m.workspaces.AcknowledgeRemoval(removal); err != nil {
				return fmt.Errorf(
					"session: acknowledge projected workspace removal %q: %w",
					id,
					err,
				)
			}
			continue
		}
		managed.setWorkspaceRemovalPending()
		receipt, commitErr := m.committer.CommitWorkspaceRemoved(ctx, managed)
		var acknowledgeErr error
		if receipt.Durable {
			acknowledgeErr = m.workspaces.AcknowledgeRemoval(removal)
		}
		if err := errors.Join(commitErr, acknowledgeErr); err != nil {
			return fmt.Errorf(
				"session: reconcile workspace removal %q: %w",
				id,
				err,
			)
		}
	}
	return nil
}

func metadataForWorkspace(target *workspace.Workspace) *workspaceMetadata {
	if target == nil {
		return nil
	}
	return &workspaceMetadata{
		Repository: target.Repository,
		Path:       target.Path,
		Branch:     target.Branch,
	}
}

func validateWorkspaceMetadata(
	metadata *workspaceMetadata,
	workingDir string,
) error {
	if metadata == nil {
		return nil
	}
	if !cleanAbsolutePath(metadata.Repository) {
		return errors.New("workspace repository is not a clean absolute path")
	}
	if !cleanAbsolutePath(metadata.Path) {
		return errors.New("workspace path is not a clean absolute path")
	}
	if metadata.Branch == "" {
		return errors.New("workspace branch is empty")
	}
	if metadata.Path != workingDir {
		return errors.New("workspace path does not match working directory")
	}
	return nil
}

func cleanAbsolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}
