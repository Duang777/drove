package workspace

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReconcileAcceptsCompletedRemovalAfterGitError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test requires a POSIX shell")
	}
	repository := newTestRepository(t)
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	prepared, err := manager.Prepare(
		context.Background(),
		repository,
		"",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare worktree: %v", err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read workspace record: exists=%v err=%v", exists, err)
	}
	record.Removal = &workspaceRemovalRecord{
		OperationID: "77777777-7777-4777-8777-777777777777",
		Force:       true,
	}
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("write removal intent: %v", err)
	}

	realGit := manager.git
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := `#!/bin/sh
case " $* " in
  *" worktree prune "*)
    "$DROVE_TEST_REAL_GIT" "$@"
    exit 1
    ;;
esac
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	manager.git = wrapper

	removals, err := manager.ReconcileRemovals(context.Background())
	if err != nil {
		t.Fatalf("reconcile completed removal: %v", err)
	}
	if len(removals) != 1 || removals[0].Workspace.Path != prepared.Path {
		t.Fatalf("reconciled removals = %+v", removals)
	}
	if err := manager.AcknowledgeRemoval(removals[0]); err != nil {
		t.Fatalf("acknowledge completed removal: %v", err)
	}
}

func TestReconcilePreservesIntentOnInspectionFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test requires a POSIX shell")
	}
	repository := newTestRepository(t)
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	prepared, err := manager.Prepare(
		context.Background(),
		repository,
		"",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare worktree: %v", err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read workspace record: exists=%v err=%v", exists, err)
	}
	record.Removal = &workspaceRemovalRecord{
		OperationID: "88888888-8888-4888-8888-888888888888",
	}
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("write removal intent: %v", err)
	}

	realGit := manager.git
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := `#!/bin/sh
case " $* " in
  *" status --porcelain=v1 "*)
    exit 1
    ;;
esac
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	manager.git = wrapper

	if _, err := manager.ReconcileRemovals(context.Background()); err == nil {
		t.Fatal("reconciliation ignored an inspection failure")
	}
	record, exists, err = manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists || record.Removal == nil {
		t.Fatalf("record after inspection failure = %+v, exists=%v err=%v", record, exists, err)
	}
	if _, err := os.Lstat(prepared.Path); err != nil {
		t.Fatalf("inspection failure removed workspace: %v", err)
	}
}
