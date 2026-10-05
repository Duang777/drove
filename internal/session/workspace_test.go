package session

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/adapter"
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
	if len(workspaces.preparationAcks) != 1 ||
		workspaces.preparationAcks[0].AgentID != status.AgentID {
		t.Fatalf(
			"preparation acknowledgements = %+v",
			workspaces.preparationAcks,
		)
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
	if len(workspaces.preparationAcks) != 0 {
		t.Fatalf(
			"failed creation acknowledgements = %+v",
			workspaces.preparationAcks,
		)
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
	if len(workspaces.preparationAcks) != 1 {
		t.Fatalf(
			"durable preparation acknowledgements = %+v",
			workspaces.preparationAcks,
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

func TestStartPreservesDurableWorkspaceWhenPreparationAckFails(t *testing.T) {
	manager, st := newTestManager(t)
	source := t.TempDir()
	ackErr := errors.New("preparation acknowledgement failed")
	workspaces := &fakeWorkspaceLifecycle{
		prepared: workspace.Workspace{
			Repository: source,
			Path:       filepath.Join(t.TempDir(), "worktree"),
			Branch:     "feature/ack-failure",
		},
		preparationAckErr: ackErr,
	}
	manager.workspaces = workspaces

	status, err := manager.Start(context.Background(), StartRequest{
		Command: "/bin/cat",
		Dir:     source,
		Worktree: &WorktreeRequest{
			Branch: "feature/ack-failure",
		},
	})
	if !errors.Is(err, ackErr) {
		t.Fatalf("start error = %v, want acknowledgement failure", err)
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
			"cleanup after acknowledgement failure error = %v, want ErrWorkspaceInUse",
			cleanupErr,
		)
	}
	select {
	case fatalErr := <-manager.Fatal():
		if !errors.Is(fatalErr, ackErr) {
			t.Fatalf("fatal error = %v, want acknowledgement failure", fatalErr)
		}
	case <-time.After(time.Second):
		t.Fatal("manager did not fail-stop after acknowledgement failure")
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

func TestPrepareWorkspaceTreatsMissingGitAsRuntimeFailure(t *testing.T) {
	manager, _ := newTestManager(t)
	workspaces, err := workspace.New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new workspace manager: %v", err)
	}
	manager.workspaces = workspaces
	t.Setenv("PATH", t.TempDir())

	prepared, err := manager.prepareWorkspace(
		context.Background(),
		"11111111-1111-4111-8111-111111111111",
		&WorktreeRequest{},
		t.TempDir(),
	)
	if prepared != nil {
		t.Fatalf("prepared workspace = %+v, want nil", prepared)
	}
	if !errors.Is(err, ErrWorkspacePrepare) {
		t.Fatalf("prepare error = %v, want ErrWorkspacePrepare", err)
	}
	if errors.Is(err, ErrWorkspaceRequest) {
		t.Fatalf("prepare error = %v, must not be a user request error", err)
	}
}

func TestBootstrapRejectsWorkspaceInitializationFailure(t *testing.T) {
	st := newTestStore(t)
	initializationErr := errors.New("workspace root unavailable")

	recovered, err := Bootstrap(
		context.Background(),
		adapter.NewRegistry(),
		st,
		func(manager *Manager) {
			manager.workspaceErr = initializationErr
		},
	)
	if recovered != nil {
		t.Fatalf("bootstrap result = %+v, want nil", recovered)
	}
	if !errors.Is(err, ErrWorkspaceUnavailable) ||
		!errors.Is(err, initializationErr) {
		t.Fatalf(
			"bootstrap error = %v, want workspace initialization failure",
			err,
		)
	}
}

func TestBootstrapWithoutManagedWorkspacesDoesNotRequireGit(t *testing.T) {
	st := newTestStore(t)
	dataDir := t.TempDir()
	t.Setenv("PATH", t.TempDir())

	recovered, err := Bootstrap(
		context.Background(),
		adapter.NewRegistry(),
		st,
		WithWorkspaces(dataDir),
	)
	if err != nil {
		t.Fatalf("bootstrap without Git: %v", err)
	}
	if recovered == nil || recovered.Manager == nil {
		t.Fatalf("bootstrap result = %+v", recovered)
	}
	if err := recovered.Manager.Close(); err != nil {
		t.Fatalf("close recovered manager: %v", err)
	}
}

func TestBootstrapReconcilesPreparationsAgainstDurableMetadata(t *testing.T) {
	manager, st := newTestManager(t)
	id := agent.ID("11111111-1111-4111-8111-111111111111")
	target := workspace.Workspace{
		AgentID:    string(id),
		Repository: filepath.Join(t.TempDir(), "repository"),
		Path:       filepath.Join(t.TempDir(), "worktree"),
		Branch:     "feature/recovered",
	}
	persistStoppedWorkspaceHistory(t, manager, id, target)
	if err := manager.Close(); err != nil {
		t.Fatalf("close initial manager: %v", err)
	}
	workspaces := &fakeWorkspaceLifecycle{}

	recovered, err := Bootstrap(
		context.Background(),
		adapter.NewRegistry(),
		st,
		withWorkspaceLifecycle(workspaces),
	)
	if err != nil {
		t.Fatalf("bootstrap workspace metadata: %v", err)
	}
	defer recovered.Manager.Close()
	if len(workspaces.reconcileExpected) != 1 {
		t.Fatalf(
			"expected preparations = %+v, want %+v",
			workspaces.reconcileExpected,
			target,
		)
	}
	reconciled := workspaces.reconcileExpected[0]
	if reconciled.AgentID != target.AgentID ||
		reconciled.Repository != target.Repository ||
		reconciled.Path != target.Path ||
		reconciled.Branch != target.Branch {
		t.Fatalf("expected preparation = %+v, want %+v", reconciled, target)
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
	status, err := manager.Status(id)
	if err != nil {
		t.Fatalf("status during cleanup: %v", err)
	}
	if status.Resumable {
		t.Fatalf("status during cleanup = %+v, want non-resumable", status)
	}
	close(releaseCleanup)
	if err := <-result; err != nil {
		t.Fatalf("cleanup workspace: %v", err)
	}
}

func TestCleanupWorkspaceAcceptsCompletedRemovalWithCommandError(t *testing.T) {
	manager, _ := newTestManager(t)
	id := "11111111-1111-4111-8111-111111111111"
	commandErr := errors.New("git exited after removing the worktree")
	workspaces := &fakeWorkspaceLifecycle{
		cleanupState: workspace.RemovalComplete,
		cleanupErr:   commandErr,
		cleanupResult: workspace.Workspace{
			AgentID: id,
			Path:    "/tmp/removed-worktree",
			Branch:  "feature/cleanup",
		},
	}
	manager.workspaces = workspaces

	removed, err := manager.CleanupWorkspace(context.Background(), id, false)
	if err != nil {
		t.Fatalf("cleanup completed workspace: %v", err)
	}
	if removed.Path != workspaces.cleanupResult.Path {
		t.Fatalf("removed workspace = %+v", removed)
	}
	if workspaces.acknowledgeCount != 1 {
		t.Fatalf(
			"completed removal acknowledgement count = %d, want 1",
			workspaces.acknowledgeCount,
		)
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
	managed.setWorkspaceState(workspaceRuntimeState{
		workingDir: "/tmp/drove-workspace",
	})
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

func TestCleanupWorkspaceStoreFailureReconcilesOnceAfterRestart(t *testing.T) {
	manager, st := newTestManager(t)
	id := agent.ID("11111111-1111-4111-8111-111111111111")
	target := workspace.Workspace{
		AgentID:    string(id),
		Repository: "/tmp/repository",
		Path:       "/tmp/drove-workspace",
		Branch:     "feature/workspace",
	}
	persistStoppedWorkspaceHistory(t, manager, id, target)
	managed := addStoppedAgent(t, manager, id, "claude", "vendor-session")
	managed.setWorkspaceState(workspaceRuntimeState{workingDir: target.Path})
	workspaces := &fakeWorkspaceLifecycle{cleanupResult: target}
	manager.workspaces = workspaces

	lastSeq := manager.committer.HighWatermark()
	manager.committer.Close()
	appendErr := errors.New("workspace tombstone unavailable")
	manager.committer = newCommitter(
		lastSeq,
		&failingCommitStore{commitStore: st, err: appendErr},
		manager.hub,
	)
	if _, err := manager.CleanupWorkspace(
		context.Background(),
		string(id),
		false,
	); !errors.Is(err, appendErr) {
		t.Fatalf("cleanup error = %v, want append failure", err)
	}
	state := managed.workspaceState()
	if !state.removalPending || state.removed || state.resumeOnStart {
		t.Fatalf("workspace state after append failure = %+v", state)
	}
	if workspaces.acknowledgeCount != 0 {
		t.Fatalf("append failure acknowledged removal %d times", workspaces.acknowledgeCount)
	}

	registry := manager.reg
	if err := manager.Close(); err != nil {
		t.Fatalf("close failed manager: %v", err)
	}
	recoveryWorkspace := &fakeWorkspaceLifecycle{
		reconcileResult: []workspace.Removal{{Workspace: target}},
	}
	recovered, err := Bootstrap(
		context.Background(),
		registry,
		st,
		withWorkspaceLifecycle(recoveryWorkspace),
	)
	if err != nil {
		t.Fatalf("bootstrap pending removal: %v", err)
	}
	if recoveryWorkspace.acknowledgeCount != 1 {
		t.Fatalf(
			"recovery acknowledgement count = %d, want 1",
			recoveryWorkspace.acknowledgeCount,
		)
	}
	recoveredStatus, err := recovered.Manager.Status(id)
	if err != nil {
		t.Fatalf("recovered status: %v", err)
	}
	if recoveredStatus.Resumable || recoveredStatus.Dir != "" {
		t.Fatalf("recovered status = %+v", recoveredStatus)
	}
	if err := recovered.Manager.Close(); err != nil {
		t.Fatalf("close recovered manager: %v", err)
	}

	secondWorkspace := &fakeWorkspaceLifecycle{
		reconcileResult: []workspace.Removal{{Workspace: target}},
	}
	second, err := Bootstrap(
		context.Background(),
		registry,
		st,
		withWorkspaceLifecycle(secondWorkspace),
	)
	if err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	defer second.Manager.Close()
	rows, err := st.Replay(string(id))
	if err != nil {
		t.Fatalf("replay reconciled history: %v", err)
	}
	removedEvents := 0
	for _, row := range rows {
		if row.Type == string(event.TypeSessionLifecycle) &&
			row.Reason == workspaceRemovedReason {
			removedEvents++
		}
	}
	if removedEvents != 1 || secondWorkspace.acknowledgeCount != 1 {
		t.Fatalf(
			"removed events = %d, second acknowledgements = %d",
			removedEvents,
			secondWorkspace.acknowledgeCount,
		)
	}
	if len(secondWorkspace.reconcileExpected) != 0 {
		t.Fatalf(
			"removed workspace preparations = %+v, want none",
			secondWorkspace.reconcileExpected,
		)
	}
}

func TestCleanupWorkspaceAcknowledgesAfterDurablePublishFailure(t *testing.T) {
	manager, st := newTestManager(t)
	id := agent.ID("11111111-1111-4111-8111-111111111111")
	target := workspace.Workspace{
		AgentID:    string(id),
		Repository: "/tmp/repository",
		Path:       "/tmp/drove-workspace",
		Branch:     "feature/workspace",
	}
	persistStoppedWorkspaceHistory(t, manager, id, target)
	managed := addStoppedAgent(t, manager, id, "claude", "vendor-session")
	managed.setWorkspaceState(workspaceRuntimeState{workingDir: target.Path})
	workspaces := &fakeWorkspaceLifecycle{cleanupResult: target}
	manager.workspaces = workspaces
	manager.hub.Close()

	if _, err := manager.CleanupWorkspace(
		context.Background(),
		string(id),
		false,
	); !errors.Is(err, event.ErrHubClosed) {
		t.Fatalf("cleanup error = %v, want closed Hub", err)
	}
	if workspaces.acknowledgeCount != 1 {
		t.Fatalf("durable removal acknowledgement count = %d", workspaces.acknowledgeCount)
	}
	state := managed.workspaceState()
	if !state.removed || state.removalPending || state.workingDir != "" {
		t.Fatalf("workspace state after publish failure = %+v", state)
	}
	rows, err := st.Replay(string(id))
	if err != nil {
		t.Fatalf("replay durable removal: %v", err)
	}
	if rows[len(rows)-1].Reason != workspaceRemovedReason {
		t.Fatalf("durable history = %+v", rows)
	}
}

func TestCleanupWorkspaceRetriesAcknowledgementWithoutDuplicateEvent(t *testing.T) {
	manager, st := newTestManager(t)
	id := agent.ID("11111111-1111-4111-8111-111111111111")
	target := workspace.Workspace{
		AgentID:    string(id),
		Repository: "/tmp/repository",
		Path:       "/tmp/drove-workspace",
		Branch:     "feature/workspace",
	}
	persistStoppedWorkspaceHistory(t, manager, id, target)
	managed := addStoppedAgent(t, manager, id, "claude", "vendor-session")
	managed.setWorkspaceState(workspaceRuntimeState{workingDir: target.Path})
	ackErr := errors.New("sidecar unlink unavailable")
	workspaces := &fakeWorkspaceLifecycle{
		cleanupResult:  target,
		acknowledgeErr: ackErr,
	}
	manager.workspaces = workspaces

	if _, err := manager.CleanupWorkspace(
		context.Background(),
		string(id),
		false,
	); !errors.Is(err, ackErr) {
		t.Fatalf("first cleanup error = %v, want acknowledgement failure", err)
	}
	workspaces.acknowledgeErr = nil
	if _, err := manager.CleanupWorkspace(
		context.Background(),
		string(id),
		false,
	); err != nil {
		t.Fatalf("retry cleanup: %v", err)
	}
	rows, err := st.Replay(string(id))
	if err != nil {
		t.Fatalf("replay cleanup history: %v", err)
	}
	removedEvents := 0
	for _, row := range rows {
		if row.Type == string(event.TypeSessionLifecycle) &&
			row.Reason == workspaceRemovedReason {
			removedEvents++
		}
	}
	if removedEvents != 1 || workspaces.acknowledgeCount != 2 {
		t.Fatalf(
			"removed events = %d, acknowledgement count = %d",
			removedEvents,
			workspaces.acknowledgeCount,
		)
	}
}

func TestManagerCloseWaitsForWorkspaceCleanupCommit(t *testing.T) {
	manager, _ := newTestManager(t)
	id := agent.ID("11111111-1111-4111-8111-111111111111")
	addStoppedAgent(t, manager, id, "claude", "vendor-session")
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	manager.workspaces = &fakeWorkspaceLifecycle{
		cleanupStarted: cleanupStarted,
		releaseCleanup: releaseCleanup,
		cleanupResult: workspace.Workspace{
			AgentID: string(id),
			Branch:  "feature/cleanup",
		},
	}

	cleanupResult := make(chan error, 1)
	go func() {
		_, err := manager.CleanupWorkspace(context.Background(), string(id), false)
		cleanupResult <- err
	}()
	<-cleanupStarted
	closeResult := make(chan error, 1)
	go func() {
		closeResult <- manager.Close()
	}()
	select {
	case err := <-closeResult:
		t.Fatalf("manager closed before cleanup completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseCleanup)
	if err := <-cleanupResult; err != nil {
		t.Fatalf("cleanup workspace: %v", err)
	}
	if err := <-closeResult; err != nil {
		t.Fatalf("close manager: %v", err)
	}
}

func TestPendingWorkspaceRemovalBlocksManualAndStartupResume(t *testing.T) {
	manager, _ := newTestManager(t)
	id := agent.ID("11111111-1111-4111-8111-111111111111")
	managed := addStoppedAgent(t, manager, id, "claude", "vendor-session")
	state := managed.workspaceState()
	state.resumeOnStart = true
	managed.setWorkspaceState(state)
	manager.workspaces = &fakeWorkspaceLifecycle{
		cleanupState: workspace.RemovalPending,
		cleanupErr:   errors.New("partial physical removal"),
		cleanupResult: workspace.Workspace{
			AgentID: string(id),
			Branch:  "feature/cleanup",
		},
	}

	if _, err := manager.CleanupWorkspace(
		context.Background(),
		string(id),
		true,
	); err == nil {
		t.Fatal("pending cleanup returned success")
	}
	if _, err := manager.Resume(context.Background(), id); !errors.Is(
		err,
		ErrResumeConflict,
	) {
		t.Fatalf("manual resume error = %v, want conflict", err)
	}
	if results := manager.ResumeOnStart(context.Background()); len(results) != 0 {
		t.Fatalf("startup resume results = %+v, want none", results)
	}
}

func TestUnchangedWorkspaceRemovalRestoresStartupResumeCandidate(t *testing.T) {
	manager, _ := newTestManager(t)
	id := agent.ID("11111111-1111-4111-8111-111111111111")
	managed := addStoppedAgent(t, manager, id, "claude", "vendor-session")
	state := managed.workspaceState()
	state.resumeOnStart = true
	managed.setWorkspaceState(state)
	managed.setWorkspaceRemovalPending()
	if managed.shouldResumeOnStart() {
		t.Fatal("pending workspace removal did not block startup resume")
	}
	manager.workspaces = &fakeWorkspaceLifecycle{
		cleanupState: workspace.RemovalUnchanged,
		cleanupErr:   workspace.ErrDirty,
	}

	if _, err := manager.CleanupWorkspace(
		context.Background(),
		string(id),
		false,
	); !errors.Is(err, ErrWorkspaceDirty) {
		t.Fatalf("unchanged cleanup error = %v, want ErrWorkspaceDirty", err)
	}
	state = managed.workspaceState()
	if state.removalPending || !state.resumeOnStart {
		t.Fatalf("workspace state after unchanged cleanup = %+v", state)
	}
}

func TestUnknownWorkspaceRemovalErrorKeepsResumeBlocked(t *testing.T) {
	manager, _ := newTestManager(t)
	id := agent.ID("11111111-1111-4111-8111-111111111111")
	managed := addStoppedAgent(t, manager, id, "claude", "vendor-session")
	managed.setWorkspaceRemovalPending()
	recoveryErr := errors.New("acknowledgement recovery failed")
	manager.workspaces = &fakeWorkspaceLifecycle{
		cleanupState: workspace.RemovalUnchanged,
		cleanupErr:   recoveryErr,
	}

	if _, err := manager.CleanupWorkspace(
		context.Background(),
		string(id),
		false,
	); !errors.Is(err, recoveryErr) {
		t.Fatalf("cleanup error = %v, want recovery failure", err)
	}
	if !managed.workspaceState().removalPending {
		t.Fatal("unknown removal error cleared pending workspace state")
	}
	if _, err := manager.Resume(context.Background(), id); !errors.Is(
		err,
		ErrResumeConflict,
	) {
		t.Fatalf("resume error = %v, want conflict", err)
	}
}

func TestStartupResumeWaitsForFailedWorkspaceCleanup(t *testing.T) {
	manager, _ := newTestManager(t)
	id := agent.ID("11111111-1111-4111-8111-111111111111")
	managed := addStoppedAgent(t, manager, id, "claude", "vendor-session")
	state := managed.workspaceState()
	state.resumeOnStart = true
	managed.setWorkspaceState(state)
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	manager.workspaces = &fakeWorkspaceLifecycle{
		cleanupStarted: cleanupStarted,
		releaseCleanup: releaseCleanup,
		cleanupState:   workspace.RemovalUnchanged,
		cleanupErr:     workspace.ErrDirty,
	}
	startedPTY := make(chan struct{}, 1)
	manager.startPTY = func(pty.Config) (launchedSession, error) {
		startedPTY <- struct{}{}
		return &fakeProcessSession{}, nil
	}

	cleanupResult := make(chan error, 1)
	go func() {
		_, err := manager.CleanupWorkspace(context.Background(), string(id), false)
		cleanupResult <- err
	}()
	<-cleanupStarted
	resumeResult := make(chan []StartupResumeResult, 1)
	go func() {
		resumeResult <- manager.ResumeOnStart(context.Background())
	}()
	close(releaseCleanup)
	if err := <-cleanupResult; !errors.Is(err, ErrWorkspaceDirty) {
		t.Fatalf("cleanup error = %v, want dirty workspace", err)
	}
	results := <-resumeResult
	if len(results) != 1 || results[0].AgentID != id || results[0].Err != nil {
		t.Fatalf("startup resume results = %+v", results)
	}
	select {
	case <-startedPTY:
	default:
		t.Fatal("startup resume did not start the PTY")
	}
}

func TestSuccessfulWorkspaceCleanupPreventsWaitingStartupResume(t *testing.T) {
	manager, _ := newTestManager(t)
	id := agent.ID("11111111-1111-4111-8111-111111111111")
	managed := addStoppedAgent(t, manager, id, "claude", "vendor-session")
	state := managed.workspaceState()
	state.resumeOnStart = true
	managed.setWorkspaceState(state)
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	manager.workspaces = &fakeWorkspaceLifecycle{
		cleanupStarted: cleanupStarted,
		releaseCleanup: releaseCleanup,
		cleanupResult: workspace.Workspace{
			AgentID: string(id),
			Branch:  "feature/cleanup",
		},
	}
	startedPTY := make(chan struct{}, 1)
	manager.startPTY = func(pty.Config) (launchedSession, error) {
		startedPTY <- struct{}{}
		return &fakeProcessSession{}, nil
	}

	cleanupResult := make(chan error, 1)
	go func() {
		_, err := manager.CleanupWorkspace(context.Background(), string(id), false)
		cleanupResult <- err
	}()
	<-cleanupStarted
	resumeResult := make(chan []StartupResumeResult, 1)
	go func() {
		resumeResult <- manager.ResumeOnStart(context.Background())
	}()
	close(releaseCleanup)
	if err := <-cleanupResult; err != nil {
		t.Fatalf("cleanup workspace: %v", err)
	}
	if results := <-resumeResult; len(results) != 0 {
		t.Fatalf("startup resume results = %+v, want none", results)
	}
	select {
	case <-startedPTY:
		t.Fatal("startup resume started after successful cleanup")
	default:
	}
}

func TestCanceledStartupResumeDoesNotConsumeCandidate(t *testing.T) {
	manager, _ := newTestManager(t)
	id := agent.ID("11111111-1111-4111-8111-111111111111")
	managed := addStoppedAgent(t, manager, id, "claude", "vendor-session")
	state := managed.workspaceState()
	state.resumeOnStart = true
	managed.setWorkspaceState(state)
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	manager.workspaces = &fakeWorkspaceLifecycle{
		cleanupStarted: cleanupStarted,
		releaseCleanup: releaseCleanup,
		cleanupState:   workspace.RemovalUnchanged,
		cleanupErr:     workspace.ErrDirty,
	}

	cleanupResult := make(chan error, 1)
	go func() {
		_, err := manager.CleanupWorkspace(context.Background(), string(id), false)
		cleanupResult <- err
	}()
	<-cleanupStarted
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	results := manager.ResumeOnStart(ctx)
	if len(results) != 1 || !errors.Is(results[0].Err, context.DeadlineExceeded) {
		t.Fatalf("canceled startup resume results = %+v", results)
	}
	if !managed.shouldResumeOnStart() {
		t.Fatal("canceled startup resume consumed its candidate")
	}
	close(releaseCleanup)
	if err := <-cleanupResult; !errors.Is(err, ErrWorkspaceDirty) {
		t.Fatalf("cleanup error = %v, want dirty workspace", err)
	}
}

func persistStoppedWorkspaceHistory(
	t *testing.T,
	manager *Manager,
	id agent.ID,
	target workspace.Workspace,
) {
	t.Helper()
	mode := agent.RunModeInteractive
	policy := agent.HooksOff
	created, err := json.Marshal(createdPayload{
		Version:    2,
		Name:       "workspace-agent",
		Vendor:     "claude",
		Mode:       &mode,
		HookPolicy: &policy,
		WorkingDir: target.Path,
		Workspace: &workspaceMetadata{
			Repository: target.Repository,
			Path:       target.Path,
			Branch:     target.Branch,
		},
	})
	if err != nil {
		t.Fatalf("encode creation metadata: %v", err)
	}
	resumed, err := json.Marshal(event.AgentResumedPayloadV1{
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
				string(created),
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
				string(resumed),
			),
		},
	); err != nil {
		t.Fatalf("persist workspace history: %v", err)
	}
}

func withWorkspaceLifecycle(lifecycle workspaceLifecycle) ManagerOption {
	return func(manager *Manager) {
		manager.workspaces = lifecycle
	}
}

type failingCommitStore struct {
	commitStore
	err error
}

func (s *failingCommitStore) AppendEvents(
	context.Context,
	uint64,
	[]store.EventRow,
) (uint64, error) {
	return 0, s.err
}

type fakeWorkspaceLifecycle struct {
	prepared workspace.Workspace

	prepareSource     string
	prepareBranch     string
	prepareAgentID    string
	prepareErr        error
	prepareStarted    chan struct{}
	releasePrepare    chan struct{}
	preparationAckErr error
	preparationAcks   []workspace.Workspace
	reconcileExpected []workspace.Workspace
	reconcilePrepErr  error
	discardCount      int
	discarded         workspace.Workspace
	cleanupCount      int
	cleanupAgentID    string
	cleanupForce      bool
	cleanupStarted    chan struct{}
	releaseCleanup    chan struct{}
	cleanupResult     workspace.Workspace
	cleanupState      workspace.RemovalState
	cleanupErr        error
	acknowledgeErr    error
	acknowledgeCount  int
	reconcileResult   []workspace.Removal
	reconcileErr      error
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

func (f *fakeWorkspaceLifecycle) AcknowledgePreparation(
	target workspace.Workspace,
) error {
	f.preparationAcks = append(f.preparationAcks, target)
	return f.preparationAckErr
}

func (f *fakeWorkspaceLifecycle) ReconcilePreparations(
	_ context.Context,
	expected []workspace.Workspace,
) error {
	f.reconcileExpected = append(
		[]workspace.Workspace(nil),
		expected...,
	)
	return f.reconcilePrepErr
}

func (f *fakeWorkspaceLifecycle) Discard(
	_ context.Context,
	target workspace.Workspace,
) error {
	f.discardCount++
	f.discarded = target
	return nil
}

func (f *fakeWorkspaceLifecycle) Remove(
	ctx context.Context,
	agentID string,
	force bool,
) (workspace.RemovalResult, error) {
	f.cleanupCount++
	f.cleanupAgentID = agentID
	f.cleanupForce = force
	if f.cleanupStarted != nil {
		close(f.cleanupStarted)
	}
	if f.releaseCleanup != nil {
		select {
		case <-ctx.Done():
			return workspace.RemovalResult{}, ctx.Err()
		case <-f.releaseCleanup:
		}
	}
	state := f.cleanupState
	if state == workspace.RemovalUnchanged && f.cleanupErr == nil {
		state = workspace.RemovalComplete
	}
	return workspace.RemovalResult{
		Removal: workspace.Removal{Workspace: f.cleanupResult},
		State:   state,
	}, f.cleanupErr
}

func (f *fakeWorkspaceLifecycle) ReconcileRemovals(
	context.Context,
) ([]workspace.Removal, error) {
	return append([]workspace.Removal(nil), f.reconcileResult...), f.reconcileErr
}

func (f *fakeWorkspaceLifecycle) AcknowledgeRemoval(workspace.Removal) error {
	f.acknowledgeCount++
	return f.acknowledgeErr
}
