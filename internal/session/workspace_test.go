package session

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/agent"
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
	if _, err := manager.CleanupWorkspace(
		context.Background(),
		status.AgentID,
		false,
	); !errors.Is(err, ErrWorkspaceInUse) {
		t.Fatalf("active workspace cleanup error = %v, want ErrWorkspaceInUse", err)
	}
	if workspaces.cleanupCount != 0 {
		t.Fatalf("active workspace cleanup reached lifecycle %d times", workspaces.cleanupCount)
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

func TestStartPreservesWorkspaceAfterDurableCreationPublishFails(t *testing.T) {
	manager, st := newTestManager(t)
	source := t.TempDir()
	workspaces := &fakeWorkspaceLifecycle{
		prepared: workspace.Workspace{
			Repository: source,
			Path:       filepath.Join(t.TempDir(), "worktree"),
			Branch:     "feature/durable",
		},
	}
	manager.workspaces = workspaces
	manager.hub.Close()

	status, err := manager.Start(context.Background(), StartRequest{
		Command: "/bin/cat",
		Dir:     source,
		Worktree: &WorktreeRequest{
			Branch: "feature/durable",
		},
	})
	if err == nil {
		t.Fatal("start succeeded with a closed event Hub")
	}
	if status != nil {
		t.Fatalf("status = %+v, want nil", status)
	}
	if workspaces.discardCount != 0 {
		t.Fatalf(
			"durable workspace was discarded %d times",
			workspaces.discardCount,
		)
	}
	stored, replayErr := st.Replay(workspaces.prepareAgentID)
	if replayErr != nil {
		t.Fatalf("replay durable creation: %v", replayErr)
	}
	if len(stored) != 2 || stored[0].Reason != "created" {
		t.Fatalf("stored creation events = %+v", stored)
	}
	if _, cleanupErr := manager.CleanupWorkspace(
		context.Background(),
		workspaces.prepareAgentID,
		false,
	); !errors.Is(cleanupErr, ErrWorkspaceInUse) {
		t.Fatalf(
			"cleanup after durable publish failure error = %v, want ErrWorkspaceInUse",
			cleanupErr,
		)
	}
	if workspaces.cleanupCount != 0 {
		t.Fatalf("durable workspace cleanup reached lifecycle %d times", workspaces.cleanupCount)
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

func TestStartReservesWorkspaceBeforePrepareCompletes(t *testing.T) {
	manager, _ := newTestManager(t)
	prepareStarted := make(chan struct{})
	releasePrepare := make(chan struct{})
	workspaces := &fakeWorkspaceLifecycle{
		prepareStarted: prepareStarted,
		releasePrepare: releasePrepare,
		prepareErr:     errors.New("prepare stopped"),
	}
	manager.workspaces = workspaces
	source := t.TempDir()

	startResult := make(chan error, 1)
	go func() {
		_, err := manager.Start(context.Background(), StartRequest{
			Command:  "/bin/cat",
			Dir:      source,
			Worktree: &WorktreeRequest{},
		})
		startResult <- err
	}()
	<-prepareStarted

	if _, err := manager.CleanupWorkspace(
		context.Background(),
		workspaces.prepareAgentID,
		false,
	); !errors.Is(err, ErrWorkspaceInUse) {
		t.Fatalf("cleanup during workspace creation error = %v, want ErrWorkspaceInUse", err)
	}
	if workspaces.cleanupCount != 0 {
		t.Fatalf("cleanup reached workspace lifecycle %d times", workspaces.cleanupCount)
	}
	close(releasePrepare)
	if err := <-startResult; !errors.Is(err, ErrWorkspacePrepare) {
		t.Fatalf("start error = %v, want ErrWorkspacePrepare", err)
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

func TestRecoveryProjectorRejectsMismatchedWorkspaceDirectory(t *testing.T) {
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
			`"working_dir":"/tmp/other","workspace":{` +
			`"repository":"/tmp/repository","path":"/tmp/worktree",` +
			`"branch":"feature"}}`,
	})
	if err == nil || !strings.Contains(
		err.Error(),
		"workspace path does not match working directory",
	) {
		t.Fatalf("projection error = %v, want workspace directory mismatch", err)
	}
}

func TestCleanupWorkspacePreventsConcurrentResume(t *testing.T) {
	manager, _ := newTestManager(t)
	id := agent.ID("11111111-1111-4111-8111-111111111111")
	addStoppedAgent(t, manager, id, "claude", "vendor-session")
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	workspaces := &fakeWorkspaceLifecycle{
		cleanupStarted: cleanupStarted,
		releaseCleanup: releaseCleanup,
		cleanupResult: workspace.Workspace{
			AgentID: string(id),
			Branch:  "feature/cleanup",
		},
	}
	manager.workspaces = workspaces

	result := make(chan error, 1)
	go func() {
		_, err := manager.CleanupWorkspace(context.Background(), string(id), false)
		result <- err
	}()
	<-cleanupStarted

	if _, err := manager.Resume(context.Background(), id); !errors.Is(
		err,
		ErrResumeConflict,
	) {
		t.Fatalf("resume during cleanup error = %v, want ErrResumeConflict", err)
	}
	close(releaseCleanup)
	if err := <-result; err != nil {
		t.Fatalf("cleanup workspace: %v", err)
	}
}

func TestCleanupWorkspacePersistsRemovalAndDisablesResumeAfterRecovery(t *testing.T) {
	manager, st := newTestManager(t)
	id := agent.ID("11111111-1111-4111-8111-111111111111")
	mode := agent.RunModeInteractive
	policy := agent.HooksOff
	rawCreatedPayload, err := json.Marshal(createdPayload{
		Version:    2,
		Name:       "workspace-agent",
		Vendor:     "claude",
		Mode:       &mode,
		HookPolicy: &policy,
		WorkingDir: "/tmp/drove-workspace",
		Workspace: &workspaceMetadata{
			Repository: "/tmp/repository",
			Path:       "/tmp/drove-workspace",
			Branch:     "feature/workspace",
		},
	})
	if err != nil {
		t.Fatalf("encode creation metadata: %v", err)
	}
	resumedPayload, err := json.Marshal(event.AgentResumedPayloadV1{
		Version:          1,
		VendorSessionRef: "vendor-session",
	})
	if err != nil {
		t.Fatalf("encode resume metadata: %v", err)
	}
	if _, err := manager.committer.CommitEvents(
		context.Background(),
		[]event.Draft{
			event.NewSessionLifecycleDraft(
				string(id),
				string(id),
				"created",
				string(rawCreatedPayload),
			),
			event.NewStateChangedDraft(
				string(id),
				string(id),
				string(agent.StatePending),
				string(agent.StateStopped),
				"test stopped",
				"",
			),
			event.NewAgentResumedDraft(
				string(id),
				string(id),
				string(resumedPayload),
			),
		},
	); err != nil {
		t.Fatalf("persist resumable session history: %v", err)
	}
	managed := addStoppedAgent(t, manager, id, "claude", "vendor-session")
	managed.workingDir = "/tmp/drove-workspace"
	manager.workspaces = &fakeWorkspaceLifecycle{
		cleanupResult: workspace.Workspace{
			AgentID: string(id),
			Branch:  "feature/workspace",
		},
	}

	if _, err := manager.CleanupWorkspace(
		context.Background(),
		string(id),
		false,
	); err != nil {
		t.Fatalf("cleanup workspace: %v", err)
	}
	status, err := manager.Status(id)
	if err != nil {
		t.Fatalf("status after cleanup: %v", err)
	}
	if status.Resumable || status.Dir != "" {
		t.Fatalf("status after cleanup = %+v, want non-resumable without directory", status)
	}
	if _, err := manager.Resume(context.Background(), id); !errors.Is(err, ErrResumeConflict) {
		t.Fatalf("resume after cleanup error = %v, want ErrResumeConflict", err)
	}

	rows, err := st.Replay(string(id))
	if err != nil {
		t.Fatalf("replay cleanup history: %v", err)
	}
	if len(rows) != 4 ||
		rows[3].Type != string(event.TypeSessionLifecycle) ||
		rows[3].Reason != workspaceRemovedReason ||
		rows[3].Payload != `{"version":1}` {
		t.Fatalf("cleanup history = %+v", rows)
	}

	registry := manager.reg
	if err := manager.Close(); err != nil {
		t.Fatalf("close manager before recovery: %v", err)
	}
	recovered, err := Bootstrap(context.Background(), registry, st)
	if err != nil {
		t.Fatalf("bootstrap cleaned workspace: %v", err)
	}
	t.Cleanup(func() {
		if err := recovered.Manager.Close(); err != nil {
			t.Errorf("close recovered manager: %v", err)
		}
	})
	recoveredStatus, err := recovered.Manager.Status(id)
	if err != nil {
		t.Fatalf("recovered status: %v", err)
	}
	if recoveredStatus.Resumable || recoveredStatus.Dir != "" {
		t.Fatalf(
			"recovered status = %+v, want non-resumable without directory",
			recoveredStatus,
		)
	}
	recoveredManaged, ok := recovered.Manager.managed(id)
	if !ok || recoveredManaged.vendorSessionReference() != "vendor-session" {
		t.Fatalf("recovered session reference = %+v", recoveredManaged)
	}
	if _, err := recovered.Manager.Resume(
		context.Background(),
		id,
	); !errors.Is(err, ErrResumeConflict) {
		t.Fatalf("recovered resume error = %v, want ErrResumeConflict", err)
	}
}

type fakeWorkspaceLifecycle struct {
	prepared workspace.Workspace

	prepareSource  string
	prepareBranch  string
	prepareAgentID string
	prepareErr     error
	prepareStarted chan struct{}
	releasePrepare chan struct{}
	discardCount   int
	discarded      workspace.Workspace
	cleanupCount   int
	cleanupAgentID string
	cleanupForce   bool
	cleanupStarted chan struct{}
	releaseCleanup chan struct{}
	cleanupResult  workspace.Workspace
	cleanupErr     error
}

func (f *fakeWorkspaceLifecycle) Prepare(
	ctx context.Context,
	source string,
	branch string,
	agentID string,
) (workspace.Workspace, error) {
	f.prepareSource = source
	f.prepareBranch = branch
	f.prepareAgentID = agentID
	if f.prepareStarted != nil {
		close(f.prepareStarted)
	}
	if f.releasePrepare != nil {
		select {
		case <-ctx.Done():
			return workspace.Workspace{}, ctx.Err()
		case <-f.releasePrepare:
		}
	}
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

func (f *fakeWorkspaceLifecycle) Cleanup(
	ctx context.Context,
	agentID string,
	force bool,
) (workspace.Workspace, error) {
	f.cleanupCount++
	f.cleanupAgentID = agentID
	f.cleanupForce = force
	if f.cleanupStarted != nil {
		close(f.cleanupStarted)
	}
	if f.releaseCleanup != nil {
		select {
		case <-ctx.Done():
			return workspace.Workspace{}, ctx.Err()
		case <-f.releaseCleanup:
		}
	}
	return f.cleanupResult, f.cleanupErr
}
