package workspace

import (
	"context"
	"errors"
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
		OperationID:    "77777777-7777-4777-8777-777777777777",
		DirectoryToken: "78787878-7878-4787-8787-787878787878",
		Force:          true,
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
		OperationID:    "88888888-8888-4888-8888-888888888888",
		DirectoryToken: "89898989-8989-4898-8989-898989898989",
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

func TestRemoveRechecksDirtyAfterQuarantine(t *testing.T) {
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
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge preparation: %v", err)
	}

	realGit := manager.git
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := `#!/bin/sh
worktree=
for argument in "$@"; do
  case "$argument" in
    */.*.removal-*)
      worktree=$argument
      break
      ;;
  esac
done
case "$GIT_WORK_TREE" in
  */.*.removal-*) worktree=$GIT_WORK_TREE ;;
esac
case "$worktree" in
  */.*.removal-*)
    printf 'changed after quarantine\n' > "$worktree/tracked.txt"
    ;;
esac
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	manager.git = wrapper

	result, err := manager.Remove(
		context.Background(),
		prepared.AgentID,
		false,
	)
	if !errors.Is(err, ErrDirty) || result.State != RemovalUnchanged {
		t.Fatalf("remove after quarantined write = %+v, %v", result, err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists || record.Removal != nil {
		t.Fatalf(
			"record after quarantined write = %+v, exists=%v err=%v",
			record.Removal,
			exists,
			err,
		)
	}
	assertFileContents(
		t,
		filepath.Join(prepared.Path, "tracked.txt"),
		"changed after quarantine\n",
	)
}

func TestReconcileDiscoversAcknowledgementQuarantineAfterRestart(t *testing.T) {
	repository := newTestRepository(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	manager, err := New(dataDir)
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
	result, err := manager.Remove(
		context.Background(),
		prepared.AgentID,
		true,
	)
	if err != nil || result.State != RemovalComplete {
		t.Fatalf("remove workspace = %+v, %v", result, err)
	}

	recordPath := workspaceRecordPath(prepared.Path)
	quarantineName := recordAcknowledgementPrefix(
		prepared.AgentID,
		result.Removal.operationID,
	) + "67676767-6767-4767-8767-676767676767"
	quarantinePath := filepath.Join(filepath.Dir(recordPath), quarantineName)
	if err := os.Rename(recordPath, quarantinePath); err != nil {
		t.Fatalf("quarantine removal record: %v", err)
	}

	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	removals, err := restarted.ReconcileRemovals(context.Background())
	if err != nil {
		t.Fatalf("reconcile acknowledgement quarantine: %v", err)
	}
	if len(removals) != 1 ||
		removals[0].operationID != result.Removal.operationID {
		t.Fatalf("reconciled removals = %+v", removals)
	}
	if err := restarted.AcknowledgeRemoval(removals[0]); err != nil {
		t.Fatalf("acknowledge recovered removal: %v", err)
	}
	if _, err := os.Lstat(quarantinePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("acknowledgement quarantine remains or inspect failed: %v", err)
	}
}
