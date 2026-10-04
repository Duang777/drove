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

func TestDiscardRejectsReplacementRepositoryBucket(t *testing.T) {
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
	bucket := filepath.Dir(prepared.Path)
	original := bucket + "-original"
	if err := os.Rename(bucket, original); err != nil {
		t.Fatalf("move repository bucket: %v", err)
	}
	replacement := filepath.Join(bucket, testAgentID)
	if err := os.MkdirAll(replacement, 0o700); err != nil {
		t.Fatalf("create replacement bucket: %v", err)
	}
	sentinel := filepath.Join(replacement, "must-remain")
	if err := os.WriteFile(sentinel, []byte("replacement\n"), 0o600); err != nil {
		t.Fatalf("write replacement sentinel: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(bucket)
		_ = os.Rename(original, bucket)
	})

	if err := manager.Discard(context.Background(), prepared); err == nil {
		t.Fatal("discard accepted a replacement repository bucket")
	}
	assertFileContents(t, sentinel, "replacement\n")
}

func TestListRejectsReplacementDataDirectory(t *testing.T) {
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
	if _, err := manager.Prepare(
		context.Background(),
		repository,
		"",
		testAgentID,
	); err != nil {
		t.Fatalf("prepare worktree: %v", err)
	}
	original := dataDir + "-original"
	if err := os.Rename(dataDir, original); err != nil {
		t.Fatalf("move data directory: %v", err)
	}
	if err := os.MkdirAll(
		filepath.Join(dataDir, worktreeDirectory),
		0o700,
	); err != nil {
		t.Fatalf("create replacement data directory: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dataDir)
		_ = os.Rename(original, dataDir)
	})

	if listed, err := manager.List(context.Background()); err == nil {
		t.Fatalf("list accepted replacement data directory: %+v", listed)
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
  *" update-ref --stdin "*)
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

func TestPreparePreservesConcurrentlyCreatedBranch(t *testing.T) {
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
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := `#!/bin/sh
case " $* " in
  *" update-ref --stdin "*)
    "$DROVE_TEST_REAL_GIT" -C "$DROVE_TEST_REPOSITORY" branch "$DROVE_TEST_BRANCH" HEAD
    ;;
esac
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	t.Setenv("DROVE_TEST_REPOSITORY", repository)
	t.Setenv("DROVE_TEST_BRANCH", "concurrent-branch")
	manager.git = wrapper

	if _, err := manager.Prepare(
		context.Background(),
		repository,
		"concurrent-branch",
		testAgentID,
	); err == nil {
		t.Fatal("prepare accepted a concurrently created branch")
	}
	runGit(
		t,
		repository,
		"show-ref",
		"--verify",
		"refs/heads/concurrent-branch",
	)
}

func TestReconcilePreparationUsesBranchOwnershipMarker(t *testing.T) {
	repository := newTestRepository(t)
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if err := manager.ensureManagedRoot(); err != nil {
		t.Fatalf("ensure managed root: %v", err)
	}
	if err := manager.ensureManagedBucket(repository); err != nil {
		t.Fatalf("ensure repository bucket: %v", err)
	}
	const operationID = "34343434-3434-4434-8434-343434343434"
	target := Workspace{
		AgentID:    testAgentID,
		Repository: repository,
		Path: filepath.Join(
			manager.root,
			repositoryHash(repository),
			testAgentID,
		),
		Branch:            "marker-owned-branch",
		branchOperationID: operationID,
	}
	if err := manager.writeWorkspaceRecord(target, nil); err != nil {
		t.Fatalf("write pending preparation: %v", err)
	}
	if err := manager.createOwnedBranch(
		context.Background(),
		repository,
		target.Branch,
		operationID,
	); err != nil {
		t.Fatalf("create marker-owned branch: %v", err)
	}

	if err := manager.ReconcilePreparations(context.Background(), nil); err != nil {
		t.Fatalf("reconcile pending preparation: %v", err)
	}
	command := exec.Command(
		"git",
		"-C",
		repository,
		"show-ref",
		"--verify",
		"--quiet",
		"refs/heads/"+target.Branch,
	)
	if err := command.Run(); err == nil {
		t.Fatal("reconciliation retained marker-owned branch")
	}
	if marker, err := manager.branchOwnershipMarkerExists(
		context.Background(),
		repository,
		operationID,
	); err != nil || marker {
		t.Fatalf("branch ownership marker = %v, err=%v", marker, err)
	}
}

func TestManagerRecreatesAcknowledgedRepositoryBucket(t *testing.T) {
	repository := newTestRepository(t)
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	first, err := manager.Prepare(
		context.Background(),
		repository,
		"",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare first worktree: %v", err)
	}
	result, err := manager.Remove(context.Background(), first.AgentID, true)
	if err != nil {
		t.Fatalf("remove first worktree: %v", err)
	}
	if err := manager.AcknowledgeRemoval(result.Removal); err != nil {
		t.Fatalf("acknowledge first removal: %v", err)
	}
	second, err := manager.Prepare(
		context.Background(),
		repository,
		"",
		secondTestAgentID,
	)
	if err != nil {
		t.Fatalf("prepare after bucket removal: %v", err)
	}
	if err := manager.Discard(context.Background(), second); err != nil {
		t.Fatalf("discard second worktree: %v", err)
	}
}
