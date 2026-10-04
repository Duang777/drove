package session

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/pty"
	"github.com/Duang777/drove/internal/store"
	"github.com/Duang777/drove/internal/workspace"
)

func TestStartPreparesWorkspaceAndPersistsPrivateMetadata(t *testing.T) {
	manager, store := newTestManager(t)
	source := t.TempDir()
	repository := filepath.Join(t.TempDir(), "repository")
	worktree := filepath.Join(t.TempDir(), "worktree")
	workspaces := &fakeWorkspaceLifecycle{
		prepared: workspace.Workspace{
			Repository: repository,
			Path:       worktree,
			Branch:     "feature/isolated",
		},
	}
	manager.workspaces = workspaces
	var started pty.Config
	manager.startPTY = func(config pty.Config) (launchedSession, error) {
		started = config
		return &fakeProcessSession{}, nil
	}

	status, err := manager.Start(context.Background(), StartRequest{
		Name:    "workspace-agent",
		Command: "/bin/cat",
		Dir:     source,
		Worktree: &WorktreeRequest{
			Branch: "feature/isolated",
		},
	})
	if err != nil {
		t.Fatalf("start workspace agent: %v", err)
	}
	if workspaces.prepareSource != source ||
		workspaces.prepareBranch != "feature/isolated" ||
		workspaces.prepareAgentID != status.AgentID {
		t.Fatalf("prepare request = %+v", workspaces)
	}
	if started.Dir != worktree {
		t.Fatalf("process directory = %q, want %q", started.Dir, worktree)
	}

	stored, err := store.Replay(status.AgentID)
	if err != nil {
		t.Fatalf("replay stored events: %v", err)
	}
	if len(stored) == 0 {
		t.Fatal("stored history has no creation event")
	}
	var metadata createdPayload
	if err := json.Unmarshal([]byte(stored[0].Payload), &metadata); err != nil {
		t.Fatalf("decode stored creation metadata: %v", err)
	}
	if metadata.WorkingDir != worktree ||
		metadata.Workspace == nil ||
		metadata.Workspace.Repository != repository ||
		metadata.Workspace.Path != worktree ||
		metadata.Workspace.Branch != "feature/isolated" {
		t.Fatalf("stored creation metadata = %+v", metadata)
	}

	public, err := manager.Replay(status.AgentID)
	if err != nil {
		t.Fatalf("replay public events: %v", err)
	}
	if len(public) == 0 ||
		strings.Contains(public[0].Payload, repository) ||
		strings.Contains(public[0].Payload, worktree) ||
		strings.Contains(public[0].Payload, "feature/isolated") ||
		strings.Contains(public[0].Payload, `"workspace"`) {
		t.Fatalf("public creation payload exposed workspace metadata: %s", public[0].Payload)
	}
	if workspaces.discardCount != 0 {
		t.Fatalf("successful workspace was discarded %d times", workspaces.discardCount)
	}
}

func TestStartDiscardsWorkspaceWhenCreationCannotPersist(t *testing.T) {
	manager, store := newTestManager(t)
	source := t.TempDir()
	workspaces := &fakeWorkspaceLifecycle{
		prepared: workspace.Workspace{
			Repository: source,
			Path:       filepath.Join(t.TempDir(), "worktree"),
			Branch:     "feature/rollback",
		},
	}
	manager.workspaces = workspaces
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	status, err := manager.Start(context.Background(), StartRequest{
		Command: "/bin/cat",
		Dir:     source,
		Worktree: &WorktreeRequest{
			Branch: "feature/rollback",
		},
	})
	if err == nil {
		t.Fatal("start succeeded with a closed store")
	}
	if status != nil {
		t.Fatalf("status = %+v, want nil", status)
	}
	if workspaces.discardCount != 1 ||
		workspaces.discarded.Path != workspaces.prepared.Path {
		t.Fatalf("discard calls = %+v", workspaces)
	}
}

func TestStartRejectsWorkspaceWhenManagerIsUnavailable(t *testing.T) {
	manager, _ := newTestManager(t)
	status, err := manager.Start(context.Background(), StartRequest{
		Command:  "/bin/cat",
		Dir:      t.TempDir(),
		Worktree: &WorktreeRequest{},
	})
	if !errors.Is(err, ErrWorkspaceUnavailable) {
		t.Fatalf("start error = %v, want ErrWorkspaceUnavailable", err)
	}
	if status != nil {
		t.Fatalf("status = %+v, want nil", status)
	}
}

func TestRecoveryProjectorValidatesWorkspaceMetadata(t *testing.T) {
	projector := newRecoveryProjector()
	err := projector.Apply(store.EventRow{
		Seq:       1,
		Timestamp: time.Now().UTC(),
		Type:      string(event.TypeSessionLifecycle),
		SessionID: "agent-1",
		AgentID:   "agent-1",
		Reason:    "created",
		Payload: `{"version":2,"name":"agent","vendor":"generic",` +
			`"mode":"interactive","hook_policy":"off",` +
			`"working_dir":"/tmp/worktree","workspace":{` +
			`"repository":"relative","path":"/tmp/worktree","branch":"feature"}}`,
	})
	if err == nil || !strings.Contains(err.Error(), "validate workspace metadata") {
		t.Fatalf("projection error = %v, want workspace validation error", err)
	}
}

type fakeWorkspaceLifecycle struct {
	prepared workspace.Workspace

	prepareSource  string
	prepareBranch  string
	prepareAgentID string
	prepareErr     error
	discardCount   int
	discarded      workspace.Workspace
}

func (f *fakeWorkspaceLifecycle) Prepare(
	_ context.Context,
	source string,
	branch string,
	agentID string,
) (workspace.Workspace, error) {
	f.prepareSource = source
	f.prepareBranch = branch
	f.prepareAgentID = agentID
	if f.prepareErr != nil {
		return workspace.Workspace{}, f.prepareErr
	}
	prepared := f.prepared
	prepared.AgentID = agentID
	return prepared, nil
}

func (f *fakeWorkspaceLifecycle) Discard(
	_ context.Context,
	target workspace.Workspace,
) error {
	f.discardCount++
	f.discarded = target
	return nil
}
