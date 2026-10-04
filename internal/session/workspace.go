package session

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Duang777/drove/internal/workspace"
)

var (
	// ErrWorkspaceUnavailable means the daemon cannot manage Git worktrees.
	ErrWorkspaceUnavailable = errors.New("session: workspace manager unavailable")
	// ErrWorkspacePrepare means a requested Git worktree could not be prepared.
	ErrWorkspacePrepare = errors.New("session: prepare workspace")
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

func validateWorkspaceMetadata(metadata *workspaceMetadata) error {
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
	return nil
}

func cleanAbsolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}
