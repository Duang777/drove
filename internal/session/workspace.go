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

type workspaceLifecycle interface {
	Prepare(
		context.Context,
		string,
		string,
		string,
	) (workspace.Workspace, error)
	Discard(context.Context, workspace.Workspace) error
	Cleanup(context.Context, string, bool) (workspace.Workspace, error)
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
	if err := m.reserveWorkspaceCleanup(agentID); err != nil {
		return workspace.Workspace{}, err
	}
	defer m.releaseWorkspaceCleanup(agentID)

	removed, err := m.workspaces.Cleanup(ctx, id, force)
	switch {
	case errors.Is(err, workspace.ErrDirty):
		return workspace.Workspace{}, errors.Join(ErrWorkspaceDirty, err)
	case errors.Is(err, workspace.ErrNotFound):
		return workspace.Workspace{}, errors.Join(ErrWorkspaceNotFound, err)
	case err != nil:
		return workspace.Workspace{}, fmt.Errorf(
			"session: cleanup workspace: %w",
			err,
		)
	default:
		return removed, nil
	}
}

func (m *Manager) reserveWorkspaceCleanup(id agent.ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrManagerClosed
	}
	if _, cleaning := m.cleaning[id]; cleaning {
		return fmt.Errorf("%w: agent %q cleanup is already running", ErrWorkspaceInUse, id)
	}
	if managed, ok := m.agents[id]; ok {
		_, attached := m.sessions[id]
		_, resuming := m.resuming[id]
		state := managed.agent.State()
		if attached || resuming ||
			(state != agent.StateDone && state != agent.StateStopped) {
			return fmt.Errorf(
				"%w: agent %q is %s",
				ErrWorkspaceInUse,
				id,
				state,
			)
		}
	}
	m.cleaning[id] = struct{}{}
	return nil
}

func (m *Manager) releaseWorkspaceCleanup(id agent.ID) {
	m.mu.Lock()
	delete(m.cleaning, id)
	m.mu.Unlock()
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
