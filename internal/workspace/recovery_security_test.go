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

func TestDiscardRejectsReplacedDataDirectory(t *testing.T) {
	repository := newTestRepository(t)
	parent := t.TempDir()
	dataDir := filepath.Join(parent, "data")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatalf("create data directory: %v", err)
	}
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

	original := dataDir + "-original"
	if err := os.Rename(dataDir, original); err != nil {
		t.Fatalf("move data directory: %v", err)
	}
	outside := t.TempDir()
	outsideWorkspace := filepath.Join(
		outside,
		worktreeDirectory,
		repositoryHash(repository),
		testAgentID,
	)
	if err := os.MkdirAll(outsideWorkspace, 0o700); err != nil {
		t.Fatalf("create outside workspace: %v", err)
	}
	sentinel := filepath.Join(outsideWorkspace, "must-remain")
	if err := os.WriteFile(sentinel, []byte("outside\n"), 0o600); err != nil {
		t.Fatalf("write outside sentinel: %v", err)
	}
	if err := os.Symlink(outside, dataDir); err != nil {
		t.Fatalf("replace data directory: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove(dataDir)
		_ = os.Rename(original, dataDir)
	})

	if err := manager.Discard(context.Background(), prepared); err == nil {
		t.Fatal("discard accepted a replaced data directory")
	}
	assertFileContents(t, sentinel, "outside\n")
}

func TestAcknowledgeRemovalRejectsReplacedRepositoryBucket(t *testing.T) {
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

	bucket := filepath.Dir(prepared.Path)
	original := bucket + "-original"
	if err := os.Rename(bucket, original); err != nil {
		t.Fatalf("move repository bucket: %v", err)
	}
	outside := t.TempDir()
	recordName := filepath.Base(workspaceRecordPath(prepared.Path))
	raw, err := os.ReadFile(filepath.Join(original, recordName))
	if err != nil {
		t.Fatalf("read original record: %v", err)
	}
	outsideRecord := filepath.Join(outside, recordName)
	if err := os.WriteFile(outsideRecord, raw, 0o600); err != nil {
		t.Fatalf("write outside record: %v", err)
	}
	if err := os.Symlink(outside, bucket); err != nil {
		t.Fatalf("replace repository bucket: %v", err)
	}

	if err := manager.AcknowledgeRemoval(result.Removal); err == nil {
		t.Fatal("acknowledgement accepted a replaced repository bucket")
	}
	if _, err := os.Lstat(outsideRecord); err != nil {
		t.Fatalf("acknowledgement removed outside record: %v", err)
	}
	if err := os.Remove(bucket); err != nil {
		t.Fatalf("remove replacement bucket: %v", err)
	}
	if err := os.Rename(original, bucket); err != nil {
		t.Fatalf("restore repository bucket: %v", err)
	}
	if err := manager.AcknowledgeRemoval(result.Removal); err != nil {
		t.Fatalf("acknowledge restored removal: %v", err)
	}
}

func TestPreparePersistsRecordBeforeCreatingBranch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test requires a POSIX shell")
	}
	repository := newTestRepository(t)
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	recordPath := workspaceRecordPath(filepath.Join(
		manager.root,
		repositoryHash(repository),
		testAgentID,
	))
	marker := filepath.Join(t.TempDir(), "branch-checked")
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := `#!/bin/sh
case " $* " in
  *" branch $DROVE_TEST_BRANCH HEAD"*)
    test -f "$DROVE_TEST_RECORD" || exit 97
    : > "$DROVE_TEST_MARKER"
    ;;
esac
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	t.Setenv("DROVE_TEST_BRANCH", "crash-safe-branch")
	t.Setenv("DROVE_TEST_RECORD", recordPath)
	t.Setenv("DROVE_TEST_MARKER", marker)
	manager.git = wrapper

	prepared, err := manager.Prepare(
		context.Background(),
		repository,
		"crash-safe-branch",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare worktree: %v", err)
	}
	if _, err := os.Lstat(marker); err != nil {
		t.Fatalf("branch creation was not checked: %v", err)
	}
	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("discard worktree: %v", err)
	}
	if _, err := os.Lstat(recordPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discarded record remains or inspect failed: %v", err)
	}
}
