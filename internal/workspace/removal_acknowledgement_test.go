package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAcknowledgeRemovalReservesManagedPathDuringRepositoryCheck(
	t *testing.T,
) {
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
	result, err := manager.Remove(
		context.Background(),
		prepared.AgentID,
		true,
	)
	if err != nil || result.State != RemovalComplete {
		t.Fatalf("remove workspace = %+v, %v", result, err)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	hookMarker := filepath.Join(t.TempDir(), "reservation-observed")
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := `#!/bin/sh
"$DROVE_TEST_REAL_GIT" "$@"
status=$?
case " $* " in
  *" worktree list --porcelain -z "*)
    if mkdir "$DROVE_TEST_WORKSPACE_PATH" 2>/dev/null; then
      exit 91
    fi
    [ -d "$DROVE_TEST_WORKSPACE_PATH" ] || exit 92
    : > "$DROVE_TEST_HOOK_MARKER"
    ;;
esac
exit "$status"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	t.Setenv("DROVE_TEST_WORKSPACE_PATH", prepared.Path)
	t.Setenv("DROVE_TEST_HOOK_MARKER", hookMarker)
	manager.git = wrapper

	if err := manager.AcknowledgeRemoval(result.Removal); err != nil {
		t.Fatalf("acknowledge removal: %v", err)
	}
	if _, err := os.Stat(hookMarker); err != nil {
		t.Fatalf("repository check did not observe reservation: %v", err)
	}
	if _, err := os.Lstat(prepared.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("acknowledgement reservation remains: %v", err)
	}
	if _, err := os.Lstat(workspaceRecordPath(prepared.Path)); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("acknowledged record remains: %v", err)
	}
}

func TestAcknowledgeRemovalRecoversManagedPathReservation(
	t *testing.T,
) {
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
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read removal record: exists=%v err=%v", exists, err)
	}
	reservation, err := manager.reserveRemovalAcknowledgementPath(record)
	if err != nil {
		t.Fatalf("reserve acknowledgement path: %v", err)
	}
	listed, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("list acknowledgement reservation: %v", err)
	}
	if len(listed) != 1 || !listed[0].Missing {
		t.Fatalf("listed acknowledgement reservation = %+v", listed)
	}
	if err := reservation.root.Close(); err != nil {
		t.Fatalf("close reservation root: %v", err)
	}
	reservation.root = nil
	if err := reservation.bucket.Close(); err != nil {
		t.Fatalf("close reservation bucket: %v", err)
	}
	reservation.bucket = nil

	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	removals, err := restarted.ReconcileRemovals(context.Background())
	if err != nil {
		t.Fatalf("reconcile removal with reservation: %v", err)
	}
	if len(removals) != 1 ||
		removals[0].operationID != result.Removal.operationID {
		t.Fatalf("reconciled removals = %+v", removals)
	}
	if err := restarted.AcknowledgeRemoval(removals[0]); err != nil {
		t.Fatalf("acknowledge recovered reservation: %v", err)
	}
	if _, err := os.Lstat(prepared.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered acknowledgement reservation remains: %v", err)
	}
}
