package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

func TestRemoveRootEntriesDoesNotFollowMovedDirectory(t *testing.T) {
	bucketPath := t.TempDir()
	workspacePath := filepath.Join(bucketPath, "workspace")
	nestedPath := filepath.Join(workspacePath, "nested")
	if err := os.MkdirAll(nestedPath, 0o700); err != nil {
		t.Fatalf("create nested workspace: %v", err)
	}
	sentinel := filepath.Join(nestedPath, "must-remain")
	if err := os.WriteFile(sentinel, []byte("outside\n"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	bucket, err := openRealPathRoot(bucketPath)
	if err != nil {
		t.Fatalf("open bucket root: %v", err)
	}
	defer bucket.Close()
	workspace, err := openRealRootFromRoot(bucket, "workspace")
	if err != nil {
		t.Fatalf("open workspace root: %v", err)
	}
	entries, err := readRootDirectory(workspace)
	if err != nil {
		_ = workspace.Close()
		t.Fatalf("read workspace root: %v", err)
	}
	if err := workspace.Close(); err != nil {
		t.Fatalf("close workspace root: %v", err)
	}
	outsidePath := filepath.Join(t.TempDir(), "moved-workspace")
	if err := os.Rename(workspacePath, outsidePath); err != nil {
		t.Fatalf("move workspace outside bucket: %v", err)
	}

	if err := removeRootEntries(bucket, "workspace", entries); err != nil {
		t.Fatalf("remove anchored child entries: %v", err)
	}
	if err := bucket.Remove("workspace"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove moved workspace error = %v, want not exist", err)
	}
	assertFileContents(
		t,
		filepath.Join(outsidePath, "nested", "must-remain"),
		"outside\n",
	)
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

func TestListRejectsReplacedWorkspaceDirectory(t *testing.T) {
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
		"managed-branch",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare managed workspace: %v", err)
	}
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge managed workspace: %v", err)
	}
	replacementPath := filepath.Join(t.TempDir(), "replacement-worktree")
	runGit(t, repository, "branch", "replacement-branch", "HEAD")
	runGit(
		t,
		repository,
		"worktree",
		"add",
		"--quiet",
		replacementPath,
		"replacement-branch",
	)
	originalPath := filepath.Join(filepath.Dir(prepared.Path), "original-worktree")
	swapMarker := filepath.Join(t.TempDir(), "swapped")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := `#!/bin/sh
case " $* " in
  *" rev-parse --is-bare-repository "*)
    if test ! -e "$DROVE_TEST_SWAP_MARKER"; then
      mv "$DROVE_TEST_MANAGED_PATH" "$DROVE_TEST_ORIGINAL_PATH" || exit 91
      mv "$DROVE_TEST_REPLACEMENT_PATH" "$DROVE_TEST_MANAGED_PATH" || exit 92
      : > "$DROVE_TEST_SWAP_MARKER"
    fi
    ;;
esac
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	t.Setenv("DROVE_TEST_MANAGED_PATH", prepared.Path)
	t.Setenv("DROVE_TEST_ORIGINAL_PATH", originalPath)
	t.Setenv("DROVE_TEST_REPLACEMENT_PATH", replacementPath)
	t.Setenv("DROVE_TEST_SWAP_MARKER", swapMarker)
	manager.git = wrapper
	defer func() {
		manager.git = realGit
		if _, err := os.Lstat(swapMarker); err == nil {
			_ = os.Rename(prepared.Path, replacementPath)
			_ = os.Rename(originalPath, prepared.Path)
		}
	}()

	listed, err := manager.List(context.Background())
	if err == nil || !strings.Contains(err.Error(), "changed while in use") {
		t.Fatalf("list replaced workspace = %+v, err=%v", listed, err)
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

func TestReconcilePreparationPreservesRecreatedBranch(t *testing.T) {
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
	const operationID = "45454545-4545-4454-8454-454545454545"
	target := Workspace{
		AgentID:    testAgentID,
		Repository: repository,
		Path: filepath.Join(
			manager.root,
			repositoryHash(repository),
			testAgentID,
		),
		Branch:            "recreated-external-branch",
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
	originalOID := strings.TrimSpace(runGit(
		t,
		repository,
		"rev-parse",
		"refs/heads/"+target.Branch,
	))
	runGit(t, repository, "branch", "-D", target.Branch)
	if err := os.WriteFile(
		filepath.Join(repository, "replacement.txt"),
		[]byte("replacement\n"),
		0o600,
	); err != nil {
		t.Fatalf("write replacement commit: %v", err)
	}
	runGit(t, repository, "add", "replacement.txt")
	runGit(t, repository, "commit", "-m", "create replacement commit")
	replacementOID := strings.TrimSpace(runGit(t, repository, "rev-parse", "HEAD"))
	if replacementOID == originalOID {
		t.Fatal("replacement commit did not change the branch object ID")
	}
	runGit(t, repository, "branch", target.Branch, replacementOID)

	if err := manager.ReconcilePreparations(context.Background(), nil); err != nil {
		t.Fatalf("reconcile pending preparation: %v", err)
	}
	currentOID := strings.TrimSpace(runGit(
		t,
		repository,
		"rev-parse",
		"refs/heads/"+target.Branch,
	))
	if currentOID != replacementOID {
		t.Fatalf("recreated branch object ID = %q, want %q", currentOID, replacementOID)
	}
	if marker, err := manager.branchOwnershipMarkerExists(
		context.Background(),
		repository,
		operationID,
	); err != nil || marker {
		t.Fatalf("branch ownership marker = %v, err=%v", marker, err)
	}
	if _, err := os.Lstat(workspaceRecordPath(target.Path)); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("discarded preparation record remains or inspect failed: %v", err)
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

func TestReconcileQuarantinedRemovalPreservesReplacementPath(t *testing.T) {
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
		OperationID: "56565656-5656-4656-8656-565656565656",
		Force:       true,
	}
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("write removal intent: %v", err)
	}
	quarantineName := removalQuarantinePrefix(record) +
		"57575757-5757-4757-8757-575757575757"
	quarantinePath := filepath.Join(filepath.Dir(prepared.Path), quarantineName)
	if err := os.Rename(prepared.Path, quarantinePath); err != nil {
		t.Fatalf("quarantine worktree: %v", err)
	}
	if err := os.Mkdir(prepared.Path, 0o700); err != nil {
		t.Fatalf("create replacement path: %v", err)
	}
	sentinel := filepath.Join(prepared.Path, "must-remain")
	if err := os.WriteFile(sentinel, []byte("replacement\n"), 0o600); err != nil {
		t.Fatalf("write replacement sentinel: %v", err)
	}

	if _, err := manager.ReconcileRemovals(context.Background()); err == nil {
		t.Fatal("reconciliation accepted a replacement at the original path")
	}
	assertFileContents(t, sentinel, "replacement\n")
	if _, err := os.Lstat(quarantinePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("quarantined worktree remains or inspect failed: %v", err)
	}
	current, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists ||
		current.Removal == nil ||
		!current.Removal.Started ||
		!current.Removal.Quarantined {
		t.Fatalf(
			"removal phase = %+v, exists=%v err=%v",
			current.Removal,
			exists,
			err,
		)
	}
}

func TestAcknowledgeRemovalRecoversQuarantinedRecord(t *testing.T) {
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
	result, err := manager.Remove(context.Background(), prepared.AgentID, true)
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

	if err := manager.AcknowledgeRemoval(result.Removal); err != nil {
		t.Fatalf("recover removal acknowledgement: %v", err)
	}
	if _, err := os.Lstat(quarantinePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("acknowledgement quarantine remains or inspect failed: %v", err)
	}
}

func TestStaleAcknowledgementPreservesNewWorkspaceRecord(t *testing.T) {
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
	result, err := manager.Remove(context.Background(), prepared.AgentID, true)
	if err != nil || result.State != RemovalComplete {
		t.Fatalf("remove workspace = %+v, %v", result, err)
	}
	recordPath := workspaceRecordPath(prepared.Path)
	quarantineName := recordAcknowledgementPrefix(
		prepared.AgentID,
		result.Removal.operationID,
	) + "78787878-7878-4787-8787-787878787878"
	quarantinePath := filepath.Join(filepath.Dir(recordPath), quarantineName)
	if err := os.Rename(recordPath, quarantinePath); err != nil {
		t.Fatalf("quarantine old removal record: %v", err)
	}
	const newOperationID = "89898989-8989-4898-8989-898989898989"
	replacement := newWorkspaceRecord(prepared, nil)
	replacement.PreparationCommitted = true
	replacement.Removal = &workspaceRemovalRecord{
		OperationID: newOperationID,
		Force:       true,
		Started:     true,
	}
	if err := manager.installWorkspaceRecord(replacement, true); err != nil {
		t.Fatalf("install replacement record: %v", err)
	}

	if err := manager.AcknowledgeRemoval(result.Removal); err == nil {
		t.Fatal("stale acknowledgement accepted a replacement record")
	}
	current, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists ||
		current.Removal == nil ||
		current.Removal.OperationID != newOperationID {
		t.Fatalf(
			"replacement record = %+v, exists=%v err=%v",
			current,
			exists,
			err,
		)
	}
	if _, err := os.Lstat(quarantinePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old acknowledgement quarantine remains: %v", err)
	}
}
