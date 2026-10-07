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

func TestEnsureManagedRootRejectsLateParentSymlink(t *testing.T) {
	parent := t.TempDir()
	redirect := filepath.Join(parent, "redirect")
	dataDir := filepath.Join(redirect, "data")
	manager, err := New(dataDir)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if _, err := os.Lstat(redirect); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new manager changed the filesystem: %v", err)
	}

	outside := t.TempDir()
	if err := os.Symlink(outside, redirect); err != nil {
		t.Skipf("create late parent symlink: %v", err)
	}
	if err := manager.ensureManagedRoot(); err == nil {
		t.Fatal("managed root creation followed a late parent symlink")
	}
	if _, err := os.Lstat(filepath.Join(outside, "data")); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("managed root creation changed the symlink target: %v", err)
	}
}

func TestEnsureManagedRootRejectsLateAncestorSymlink(t *testing.T) {
	parent := t.TempDir()
	ancestor := filepath.Join(parent, "ancestor")
	stable := filepath.Join(ancestor, "stable")
	if err := os.MkdirAll(stable, 0o700); err != nil {
		t.Fatalf("create stable ancestor: %v", err)
	}
	manager, err := New(filepath.Join(stable, "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if err := os.Rename(ancestor, ancestor+"-opened"); err != nil {
		t.Fatalf("move original ancestor: %v", err)
	}
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(filepath.Join(outside, "stable"), 0o700); err != nil {
		t.Fatalf("create redirect target: %v", err)
	}
	if err := os.Symlink(outside, ancestor); err != nil {
		t.Skipf("create ancestor symlink: %v", err)
	}

	if err := manager.ensureManagedRoot(); err == nil {
		t.Fatal("managed root creation followed a symlink in an earlier ancestor")
	}
	if _, err := os.Lstat(
		filepath.Join(outside, "stable", "data"),
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed root creation changed the symlink target: %v", err)
	}
}

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

func TestRemoveRootDirectoryContentsRejectsReplacementParent(t *testing.T) {
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
	defer workspace.Close()
	entries, err := readRootDirectory(workspace)
	if err != nil {
		t.Fatalf("read workspace root: %v", err)
	}
	outsidePath := filepath.Join(t.TempDir(), "moved-workspace")
	if err := os.Rename(workspacePath, outsidePath); err != nil {
		t.Fatalf("move workspace outside bucket: %v", err)
	}
	replacementSentinel := filepath.Join(
		workspacePath,
		"nested",
		"must-remain",
	)
	if err := os.MkdirAll(filepath.Dir(replacementSentinel), 0o700); err != nil {
		t.Fatalf("create replacement workspace: %v", err)
	}
	if err := os.WriteFile(
		replacementSentinel,
		[]byte("replacement\n"),
		0o600,
	); err != nil {
		t.Fatalf("write replacement sentinel: %v", err)
	}

	err = removeRootDirectoryContents(
		bucket,
		"workspace",
		workspace,
		entries,
	)
	if err == nil || !strings.Contains(err.Error(), "changed while in use") {
		t.Fatalf("remove replaced workspace error = %v", err)
	}
	assertFileContents(t, replacementSentinel, "replacement\n")
	if _, err := os.Lstat(
		filepath.Join(outsidePath, "nested", "must-remain"),
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("opened original directory was not cleaned: %v", err)
	}
}

func TestRemoveOpenedDirectoryPreservesCanonicalReplacement(t *testing.T) {
	parentPath := t.TempDir()
	originalPath := filepath.Join(parentPath, "workspace")
	if err := os.Mkdir(originalPath, 0o700); err != nil {
		t.Fatalf("create original directory: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(originalPath, "old"),
		[]byte("old\n"),
		0o600,
	); err != nil {
		t.Fatalf("write original file: %v", err)
	}
	parent, err := openRealPathRoot(parentPath)
	if err != nil {
		t.Fatalf("open parent root: %v", err)
	}
	defer parent.Close()
	opened, err := openRealRootFromRoot(parent, "workspace")
	if err != nil {
		t.Fatalf("open original directory: %v", err)
	}
	replacement := filepath.Join(originalPath, "replacement")

	err = removeOpenedDirectoryFromRootAfterIsolation(
		parent,
		"workspace",
		opened,
		"workspace",
		func(string) {
			if err := os.Mkdir(originalPath, 0o700); err != nil {
				t.Fatalf("create canonical replacement: %v", err)
			}
			if err := os.WriteFile(
				replacement,
				[]byte("replacement\n"),
				0o600,
			); err != nil {
				t.Fatalf("write replacement file: %v", err)
			}
		},
	)
	if err != nil {
		t.Fatalf("remove opened directory: %v", err)
	}
	assertFileContents(t, replacement, "replacement\n")
	entries, err := os.ReadDir(parentPath)
	if err != nil {
		t.Fatalf("read parent directory: %v", err)
	}
	for _, entry := range entries {
		if isRootDeletionName(entry.Name(), "workspace") {
			t.Fatalf("deletion debris remains: %q", entry.Name())
		}
	}
}

func TestRemoveAllRecoversIsolatedDirectoryDeletion(t *testing.T) {
	parentPath := t.TempDir()
	debrisName := rootDeletionPrefix("workspace") +
		"49494949-4949-4949-8949-494949494949"
	debrisPath := filepath.Join(parentPath, debrisName)
	if err := os.Mkdir(debrisPath, 0o700); err != nil {
		t.Fatalf("create deletion debris: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(debrisPath, "old"),
		[]byte("old\n"),
		0o600,
	); err != nil {
		t.Fatalf("write deletion debris: %v", err)
	}
	parent, err := openRealPathRoot(parentPath)
	if err != nil {
		t.Fatalf("open parent root: %v", err)
	}
	defer parent.Close()

	if err := removeAllFromRoot(parent, "workspace"); err != nil {
		t.Fatalf("recover isolated deletion: %v", err)
	}
	if _, err := os.Lstat(debrisPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deletion debris remains: %v", err)
	}
}

func TestRenameRecordRejectsReplacedTemporaryPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test requires renaming an open file")
	}
	directoryPath := t.TempDir()
	temporaryName := "record.tmp"
	temporaryPath := filepath.Join(directoryPath, temporaryName)
	temporary, err := os.OpenFile(
		temporaryPath,
		os.O_RDWR|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		t.Fatalf("create temporary record: %v", err)
	}
	defer temporary.Close()
	if _, err := temporary.WriteString("original\n"); err != nil {
		t.Fatalf("write temporary record: %v", err)
	}
	movedPath := temporaryPath + ".moved"
	if err := os.Rename(temporaryPath, movedPath); err != nil {
		t.Fatalf("move temporary record: %v", err)
	}
	if err := os.WriteFile(
		temporaryPath,
		[]byte("replacement\n"),
		0o600,
	); err != nil {
		t.Fatalf("replace temporary record: %v", err)
	}
	directory, err := os.Open(directoryPath)
	if err != nil {
		t.Fatalf("open record directory: %v", err)
	}
	defer directory.Close()

	installed, err := renameRecordFile(
		directory,
		temporary,
		temporaryName,
		"record.json",
		false,
	)
	if err == nil || installed {
		t.Fatalf(
			"rename replaced temporary record = installed %v, err %v",
			installed,
			err,
		)
	}
	if _, err := os.Lstat(
		filepath.Join(directoryPath, "record.json"),
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("record target exists after rejected rename: %v", err)
	}
	assertFileContents(t, temporaryPath, "replacement\n")
	assertFileContents(t, movedPath, "original\n")
}

func TestVerifyRecordBucketRejectsMovedCanonicalBucket(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	manager, err := New(dataDir)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	repository := filepath.Join(t.TempDir(), "repository")
	if err := manager.ensureManagedRoot(); err != nil {
		t.Fatalf("ensure managed root: %v", err)
	}
	if err := manager.ensureManagedBucket(repository); err != nil {
		t.Fatalf("ensure managed bucket: %v", err)
	}
	target := Workspace{
		AgentID:    testAgentID,
		Repository: repository,
		Path: filepath.Join(
			manager.root,
			repositoryHash(repository),
			testAgentID,
		),
		Branch: "record-bucket",
	}
	bucket, _, err := manager.openRecordBucket(target.Path)
	if err != nil {
		t.Fatalf("open record bucket: %v", err)
	}
	defer bucket.Close()
	bucketPath := filepath.Dir(target.Path)
	originalPath := bucketPath + "-original"
	if err := os.Rename(bucketPath, originalPath); err != nil {
		t.Fatalf("move canonical bucket: %v", err)
	}
	if err := os.Mkdir(bucketPath, 0o700); err != nil {
		t.Fatalf("create replacement bucket: %v", err)
	}
	sentinel := filepath.Join(bucketPath, "must-remain")
	if err := os.WriteFile(sentinel, []byte("replacement\n"), 0o600); err != nil {
		t.Fatalf("write replacement sentinel: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(bucketPath)
		_ = os.Rename(originalPath, bucketPath)
	})

	if err := manager.verifyRecordBucket(target.Path, bucket); err == nil {
		t.Fatal("record bucket verification accepted a moved bucket")
	}
	assertFileContents(t, sentinel, "replacement\n")
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
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge preparation: %v", err)
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

func TestAcknowledgedRecordRemovalDetectsBucketReplacementAfterOpening(
	t *testing.T,
) {
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
	result, err := manager.Remove(
		context.Background(),
		prepared.AgentID,
		true,
	)
	if err != nil || result.State != RemovalComplete {
		t.Fatalf("remove workspace = %+v, %v", result, err)
	}

	bucketPath := filepath.Dir(prepared.Path)
	openedBucketPath := bucketPath + "-opened"
	replacementSentinel := filepath.Join(bucketPath, "must-remain")
	t.Cleanup(func() {
		_ = os.RemoveAll(bucketPath)
		_ = os.Rename(openedBucketPath, bucketPath)
	})
	err = manager.removeAcknowledgedWorkspaceRecordAfterValidation(
		result.Removal,
		func() {
			if err := os.Rename(bucketPath, openedBucketPath); err != nil {
				t.Fatalf("move opened repository bucket: %v", err)
			}
			if err := os.Mkdir(bucketPath, 0o700); err != nil {
				t.Fatalf("create replacement repository bucket: %v", err)
			}
			if err := os.WriteFile(
				replacementSentinel,
				[]byte("replacement\n"),
				0o600,
			); err != nil {
				t.Fatalf("write replacement sentinel: %v", err)
			}
		},
	)
	if err == nil {
		t.Fatal("acknowledged record removal missed a replaced repository bucket")
	}
	assertFileContents(t, replacementSentinel, "replacement\n")
}

func TestAcknowledgeRemovalPreservesRecordWhenRepositoryDisappears(
	t *testing.T,
) {
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
	result, err := manager.Remove(
		context.Background(),
		prepared.AgentID,
		true,
	)
	if err != nil || result.State != RemovalComplete {
		t.Fatalf("remove workspace = %+v, %v", result, err)
	}

	movedRepository := repository + "-moved"
	if err := os.Rename(repository, movedRepository); err != nil {
		t.Fatalf("move source repository: %v", err)
	}
	repositoryMoved := true
	t.Cleanup(func() {
		if repositoryMoved {
			_ = os.Rename(movedRepository, repository)
		}
	})
	if err := manager.AcknowledgeRemoval(result.Removal); err == nil {
		t.Fatal("acknowledgement ignored a missing source repository")
	}
	if _, err := os.Lstat(workspaceRecordPath(prepared.Path)); err != nil {
		t.Fatalf("failed acknowledgement removed the sidecar: %v", err)
	}

	if err := os.Rename(movedRepository, repository); err != nil {
		t.Fatalf("restore source repository: %v", err)
	}
	repositoryMoved = false
	if err := manager.AcknowledgeRemoval(result.Removal); err != nil {
		t.Fatalf("acknowledge restored removal: %v", err)
	}
}

func TestAcknowledgeRemovalRetryDoesNotRequireDeletedRepository(
	t *testing.T,
) {
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
	result, err := manager.Remove(
		context.Background(),
		prepared.AgentID,
		true,
	)
	if err != nil || result.State != RemovalComplete {
		t.Fatalf("remove workspace = %+v, %v", result, err)
	}
	if err := manager.AcknowledgeRemoval(result.Removal); err != nil {
		t.Fatalf("acknowledge removal: %v", err)
	}
	movedRepository := repository + "-moved"
	if err := os.Rename(repository, movedRepository); err != nil {
		t.Fatalf("move acknowledged source repository: %v", err)
	}
	defer func() {
		_ = os.Rename(movedRepository, repository)
	}()

	if err := manager.AcknowledgeRemoval(result.Removal); err != nil {
		t.Fatalf("retry acknowledgement without source repository: %v", err)
	}
}

func TestAcknowledgeRemovalRejectsMissingRecordWithReplacementWorkspace(
	t *testing.T,
) {
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
	result, err := manager.Remove(
		context.Background(),
		prepared.AgentID,
		true,
	)
	if err != nil || result.State != RemovalComplete {
		t.Fatalf("remove workspace = %+v, %v", result, err)
	}
	if err := os.Remove(workspaceRecordPath(prepared.Path)); err != nil {
		t.Fatalf("remove pending acknowledgement record: %v", err)
	}
	if err := os.Mkdir(prepared.Path, 0o700); err != nil {
		t.Fatalf("create replacement workspace: %v", err)
	}
	replacementMarker := filepath.Join(prepared.Path, "replacement")
	if err := os.WriteFile(replacementMarker, []byte("replacement\n"), 0o600); err != nil {
		t.Fatalf("write replacement marker: %v", err)
	}

	if err := manager.AcknowledgeRemoval(result.Removal); err == nil {
		t.Fatal("acknowledgement accepted a missing record")
	}
	contents, err := os.ReadFile(replacementMarker)
	if err != nil {
		t.Fatalf("read replacement marker: %v", err)
	}
	if string(contents) != "replacement\n" {
		t.Fatalf("replacement marker = %q", contents)
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

func TestDiscardRejectsReplacementWorkspacePath(t *testing.T) {
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
	originalPath := prepared.Path + "-original"
	if err := os.Rename(prepared.Path, originalPath); err != nil {
		t.Fatalf("move prepared workspace: %v", err)
	}
	if err := os.Mkdir(prepared.Path, 0o700); err != nil {
		t.Fatalf("create replacement workspace: %v", err)
	}
	sentinel := filepath.Join(prepared.Path, "must-remain")
	if err := os.WriteFile(sentinel, []byte("replacement\n"), 0o600); err != nil {
		t.Fatalf("write replacement sentinel: %v", err)
	}

	if err := manager.Discard(context.Background(), prepared); err == nil {
		t.Fatal("discard accepted a replacement workspace path")
	}
	assertFileContents(t, sentinel, "replacement\n")
	if err := os.RemoveAll(prepared.Path); err != nil {
		t.Fatalf("remove replacement workspace: %v", err)
	}
	if err := os.Rename(originalPath, prepared.Path); err != nil {
		t.Fatalf("restore prepared workspace: %v", err)
	}
	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("discard restored workspace: %v", err)
	}
}

func TestDiscardPreservesRecoveryEvidenceWhenStagingProbeFails(t *testing.T) {
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
	originalPath := prepared.Path + "-original"
	if err := os.Rename(prepared.Path, originalPath); err != nil {
		t.Fatalf("move prepared workspace: %v", err)
	}
	stagingName, err := preparedWorktreeStagingName(
		prepared.AgentID,
		prepared.branchOperationID,
	)
	if err != nil {
		t.Fatalf("derive staging name: %v", err)
	}
	stagingPath := filepath.Join(filepath.Dir(prepared.Path), stagingName)
	if err := os.Symlink(originalPath, stagingPath); err != nil {
		t.Skipf("create replacement staging symlink: %v", err)
	}

	if err := manager.Discard(context.Background(), prepared); err == nil {
		t.Fatal("discard accepted a replacement staging path")
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf(
			"recovery record after failed staging probe: exists=%v err=%v",
			exists,
			err,
		)
	}
	if record.BranchOperationID != prepared.branchOperationID {
		t.Fatalf("recovery record changed after failed staging probe: %+v", record)
	}
	if err := exec.Command(
		"git",
		"-C",
		repository,
		"show-ref",
		"--verify",
		"--quiet",
		"refs/heads/"+prepared.Branch,
	).Run(); err != nil {
		t.Fatalf("owned branch was removed after failed staging probe: %v", err)
	}
	marker, err := manager.branchOwnershipMarkerExists(
		context.Background(),
		repository,
		prepared.branchOperationID,
	)
	if err != nil || !marker {
		t.Fatalf("branch ownership marker = %v, err=%v", marker, err)
	}

	if err := os.Remove(stagingPath); err != nil {
		t.Fatalf("remove replacement staging path: %v", err)
	}
	if err := os.Rename(originalPath, prepared.Path); err != nil {
		t.Fatalf("restore prepared workspace: %v", err)
	}
	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("discard restored workspace: %v", err)
	}
}

func TestRemoveRejectsReplacementGitWorktree(t *testing.T) {
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
		t.Fatalf("prepare workspace: %v", err)
	}
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge preparation: %v", err)
	}
	replacementPath := filepath.Join(t.TempDir(), "replacement-worktree")
	runGit(t, repository, "branch", "replacement-remove", "HEAD")
	runGit(
		t,
		repository,
		"worktree",
		"add",
		"--quiet",
		replacementPath,
		"replacement-remove",
	)
	if err := os.WriteFile(
		filepath.Join(replacementPath, "must-remain"),
		[]byte("replacement\n"),
		0o600,
	); err != nil {
		t.Fatalf("write replacement sentinel: %v", err)
	}
	originalPath := prepared.Path + "-original"
	if err := os.Rename(prepared.Path, originalPath); err != nil {
		t.Fatalf("move original workspace: %v", err)
	}
	if err := os.Rename(replacementPath, prepared.Path); err != nil {
		t.Fatalf("install replacement workspace: %v", err)
	}
	defer func() {
		_ = os.Rename(prepared.Path, replacementPath)
		_ = os.Rename(originalPath, prepared.Path)
	}()

	if _, err := manager.Remove(
		context.Background(),
		prepared.AgentID,
		true,
	); err == nil {
		t.Fatal("remove accepted a replacement Git worktree")
	}
	assertFileContents(
		t,
		filepath.Join(prepared.Path, "must-remain"),
		"replacement\n",
	)
}

func TestRemoveRejectsCopiedWorkspaceDirectory(t *testing.T) {
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
		t.Fatalf("prepare workspace: %v", err)
	}
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge preparation: %v", err)
	}
	originalPath := prepared.Path + "-original"
	if err := os.Rename(prepared.Path, originalPath); err != nil {
		t.Fatalf("move original workspace: %v", err)
	}
	if err := os.CopyFS(prepared.Path, os.DirFS(originalPath)); err != nil {
		t.Fatalf("copy workspace directory: %v", err)
	}
	sentinel := filepath.Join(prepared.Path, "must-remain")
	if err := os.WriteFile(sentinel, []byte("replacement\n"), 0o600); err != nil {
		t.Fatalf("write replacement sentinel: %v", err)
	}

	if _, err := manager.Remove(
		context.Background(),
		prepared.AgentID,
		true,
	); err == nil {
		t.Fatal("remove accepted a copied workspace directory")
	}
	assertFileContents(t, sentinel, "replacement\n")
}

func TestListRejectsCopiedWorkspaceDirectory(t *testing.T) {
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
		t.Fatalf("prepare workspace: %v", err)
	}
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge preparation: %v", err)
	}
	originalPath := prepared.Path + "-original"
	if err := os.Rename(prepared.Path, originalPath); err != nil {
		t.Fatalf("move original workspace: %v", err)
	}
	if err := os.CopyFS(prepared.Path, os.DirFS(originalPath)); err != nil {
		t.Fatalf("copy workspace directory: %v", err)
	}

	if listed, err := manager.List(context.Background()); err == nil {
		t.Fatalf("list accepted copied workspace: %+v", listed)
	}
}

func TestReconcilePreparationWithoutIdentityPreservesPresentPath(t *testing.T) {
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
		t.Fatalf("prepare workspace: %v", err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read workspace record: exists=%v err=%v", exists, err)
	}
	record.GitDirectory = ""
	record.DirectoryIdentity = ""
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("clear recorded identity: %v", err)
	}
	originalPath := prepared.Path + "-original"
	if err := os.Rename(prepared.Path, originalPath); err != nil {
		t.Fatalf("move original workspace: %v", err)
	}
	if err := os.CopyFS(prepared.Path, os.DirFS(originalPath)); err != nil {
		t.Fatalf("copy replacement workspace: %v", err)
	}
	sentinel := filepath.Join(prepared.Path, "must-remain")
	if err := os.WriteFile(sentinel, []byte("replacement\n"), 0o600); err != nil {
		t.Fatalf("write replacement sentinel: %v", err)
	}

	if err := manager.ReconcilePreparations(
		context.Background(),
		nil,
	); err == nil {
		t.Fatal("reconciliation accepted a preparation without identity")
	}
	assertFileContents(t, sentinel, "replacement\n")
}

func TestReconcileStartedLegacyRemovalPreservesReplacementPath(t *testing.T) {
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
		t.Fatalf("prepare workspace: %v", err)
	}
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge preparation: %v", err)
	}
	legacy := workspaceRecord{
		Version:         protectedWorkspaceRecordVersion,
		AgentID:         prepared.AgentID,
		Repository:      prepared.Repository,
		Path:            prepared.Path,
		Branch:          prepared.Branch,
		ProtectionKnown: true,
		IncludedPaths:   []string{},
		Removal: &workspaceRemovalRecord{
			OperationID: "56565656-5656-4656-8656-565656565656",
			Force:       true,
		},
	}
	if err := manager.replaceWorkspaceRecord(legacy); err != nil {
		t.Fatalf("write legacy removal record: %v", err)
	}
	originalPath := prepared.Path + "-original"
	if err := os.Rename(prepared.Path, originalPath); err != nil {
		t.Fatalf("move original workspace: %v", err)
	}
	if err := os.CopyFS(prepared.Path, os.DirFS(originalPath)); err != nil {
		t.Fatalf("copy replacement workspace: %v", err)
	}
	sentinel := filepath.Join(prepared.Path, "must-remain")
	if err := os.WriteFile(sentinel, []byte("replacement\n"), 0o600); err != nil {
		t.Fatalf("write replacement sentinel: %v", err)
	}

	if _, err := manager.ReconcileRemovals(
		context.Background(),
	); err == nil {
		t.Fatal("reconciliation accepted a legacy removal without identity")
	}
	assertFileContents(t, sentinel, "replacement\n")
}

func TestRemoveRejectsCopiedLegacyWorkspaceDirectory(t *testing.T) {
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
		t.Fatalf("prepare workspace: %v", err)
	}
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge preparation: %v", err)
	}
	legacy := workspaceRecord{
		Version:         protectedWorkspaceRecordVersion,
		AgentID:         prepared.AgentID,
		Repository:      prepared.Repository,
		Path:            prepared.Path,
		Branch:          prepared.Branch,
		ProtectionKnown: true,
		IncludedPaths:   []string{},
	}
	if err := manager.replaceWorkspaceRecord(legacy); err != nil {
		t.Fatalf("write legacy workspace record: %v", err)
	}
	originalPath := prepared.Path + "-original"
	if err := os.Rename(prepared.Path, originalPath); err != nil {
		t.Fatalf("move original workspace: %v", err)
	}
	if err := os.CopyFS(prepared.Path, os.DirFS(originalPath)); err != nil {
		t.Fatalf("copy replacement workspace: %v", err)
	}
	sentinel := filepath.Join(prepared.Path, "must-remain")
	if err := os.WriteFile(sentinel, []byte("replacement\n"), 0o600); err != nil {
		t.Fatalf("write replacement sentinel: %v", err)
	}

	if _, err := manager.Remove(
		context.Background(),
		prepared.AgentID,
		true,
	); err == nil {
		t.Fatal("remove accepted a copied legacy workspace directory")
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
  *" update-ref "*" --stdin "*)
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
  *" update-ref "*" --stdin "*)
    env -u GIT_DIR -u GIT_COMMON_DIR -u GIT_WORK_TREE \
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
	target.repositoryEvidence = testRepositoryEvidence(
		t,
		manager,
		repository,
	)
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
	target.repositoryEvidence = testRepositoryEvidence(
		t,
		manager,
		repository,
	)
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

func TestReconcilePreparationPreservesSameCommitRecreatedBranch(
	t *testing.T,
) {
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
	const operationID = "46464646-4646-4646-8646-464646464646"
	target := Workspace{
		AgentID:    testAgentID,
		Repository: repository,
		Path: filepath.Join(
			manager.root,
			repositoryHash(repository),
			testAgentID,
		),
		Branch:            "same-commit-recreated-branch",
		branchOperationID: operationID,
	}
	target.repositoryEvidence = testRepositoryEvidence(
		t,
		manager,
		repository,
	)
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
	runGit(t, repository, "branch", target.Branch, originalOID)

	if err := manager.ReconcilePreparations(context.Background(), nil); err != nil {
		t.Fatalf("reconcile pending preparation: %v", err)
	}
	currentOID := strings.TrimSpace(runGit(
		t,
		repository,
		"rev-parse",
		"refs/heads/"+target.Branch,
	))
	if currentOID != originalOID {
		t.Fatalf("recreated branch object ID = %q, want %q", currentOID, originalOID)
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

func TestPreparedRefTransactionLocksBranchDuringValidation(t *testing.T) {
	repository := newTestRepository(t)
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	oldOID := strings.TrimSpace(runGit(t, repository, "rev-parse", "HEAD"))
	if err := os.WriteFile(
		filepath.Join(repository, "second.txt"),
		[]byte("second\n"),
		0o600,
	); err != nil {
		t.Fatalf("write second commit: %v", err)
	}
	runGit(t, repository, "add", "second.txt")
	runGit(t, repository, "commit", "-m", "second")
	newOID := strings.TrimSpace(runGit(t, repository, "rev-parse", "HEAD"))
	const branch = "refs/heads/prepared-lock"
	runGit(t, repository, "update-ref", branch, oldOID)

	var externalErr error
	committed, err := manager.runPreparedRefTransaction(
		context.Background(),
		repository,
		[]string{"delete " + branch + " " + oldOID},
		func() (bool, error) {
			command := exec.Command(
				"git",
				"-C",
				repository,
				"-c",
				"core.filesRefLockTimeout=50",
				"update-ref",
				branch,
				newOID,
				oldOID,
			)
			externalErr = command.Run()
			return false, nil
		},
	)
	if err != nil {
		t.Fatalf("abort prepared transaction: %v", err)
	}
	if committed {
		t.Fatal("prepared transaction committed after validation rejected it")
	}
	if externalErr == nil {
		t.Fatal("external branch update succeeded while ref lock was held")
	}
	runGit(t, repository, "update-ref", branch, newOID, oldOID)
	current := strings.TrimSpace(runGit(t, repository, "rev-parse", branch))
	if current != newOID {
		t.Fatalf("branch object ID = %q, want %q", current, newOID)
	}
}

func TestManagerRetainsAcknowledgedRepositoryBucket(t *testing.T) {
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
	bucket := filepath.Dir(first.Path)
	info, err := os.Lstat(bucket)
	if err != nil {
		t.Fatalf("inspect retained repository bucket: %v", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("retained repository bucket mode = %v", info.Mode())
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
		OperationID:    "56565656-5656-4656-8656-565656565656",
		DirectoryToken: "57575757-5757-4757-8757-575757575757",
		Force:          true,
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

func TestReconcileStartedRemovalRejectsReplacementQuarantine(t *testing.T) {
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
		t.Fatalf("prepare workspace: %v", err)
	}
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge preparation: %v", err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read workspace record: exists=%v err=%v", exists, err)
	}
	record.Removal = &workspaceRemovalRecord{
		OperationID:    "47474747-4747-4747-8747-474747474747",
		DirectoryToken: "48484848-4848-4848-8848-484848484848",
		Force:          true,
		Started:        true,
		Quarantined:    true,
	}
	workspaceRoot, err := openRealPathRoot(prepared.Path)
	if err != nil {
		t.Fatalf("open workspace root: %v", err)
	}
	if err := ensureRemovalMarker(workspaceRoot, record); err != nil {
		_ = workspaceRoot.Close()
		t.Fatalf("install removal marker: %v", err)
	}
	if err := workspaceRoot.Close(); err != nil {
		t.Fatalf("close workspace root: %v", err)
	}
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("persist started removal: %v", err)
	}
	quarantineName := removalQuarantinePrefix(record) +
		"49494949-4949-4949-8949-494949494949"
	quarantinePath := filepath.Join(filepath.Dir(prepared.Path), quarantineName)
	if err := os.Rename(prepared.Path, quarantinePath); err != nil {
		t.Fatalf("quarantine workspace: %v", err)
	}
	originalQuarantine := quarantinePath + "-original"
	if err := os.Rename(quarantinePath, originalQuarantine); err != nil {
		t.Fatalf("move original quarantine: %v", err)
	}
	if err := os.Mkdir(quarantinePath, 0o700); err != nil {
		t.Fatalf("create replacement quarantine: %v", err)
	}
	sentinel := filepath.Join(quarantinePath, "must-remain")
	if err := os.WriteFile(sentinel, []byte("replacement\n"), 0o600); err != nil {
		t.Fatalf("write replacement sentinel: %v", err)
	}

	if _, err := manager.ReconcileRemovals(context.Background()); err == nil {
		t.Fatal("reconciliation accepted a replacement quarantine")
	}
	assertFileContents(t, sentinel, "replacement\n")
	if _, err := os.Stat(originalQuarantine); err != nil {
		t.Fatalf("original quarantine changed: %v", err)
	}
}

func TestEnsureRemovalMarkerRecoversInterruptedPublication(t *testing.T) {
	record := workspaceRecord{
		AgentID: "11111111-1111-4111-8111-111111111111",
		Removal: &workspaceRemovalRecord{
			OperationID:    "47474747-4747-4747-8747-474747474747",
			DirectoryToken: "48484848-4848-4848-8848-484848484848",
		},
	}
	for _, test := range []struct {
		name string
		path string
	}{
		{
			name: "staged payload",
			path: removalMarkerTemporaryPrefix(record) +
				"49494949-4949-4949-8949-494949494949",
		},
		{
			name: "canonical payload",
			path: removalMarkerName(record),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := t.TempDir()
			if err := os.WriteFile(
				filepath.Join(path, test.path),
				[]byte("partial"),
				0o600,
			); err != nil {
				t.Fatalf("write interrupted marker: %v", err)
			}
			root, err := openRealPathRoot(path)
			if err != nil {
				t.Fatalf("open marker root: %v", err)
			}
			defer root.Close()

			if err := ensureRemovalMarker(root, record); err != nil {
				t.Fatalf("recover removal marker: %v", err)
			}
			if err := verifyRemovalMarker(root, record); err != nil {
				t.Fatalf("verify recovered marker: %v", err)
			}
			if test.path != removalMarkerName(record) {
				if _, err := root.Lstat(test.path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("staged marker remains after recovery: %v", err)
				}
			}
			entries, err := readRootDirectory(root)
			if err != nil {
				t.Fatalf("read recovered marker directory: %v", err)
			}
			if len(entries) != 1 ||
				entries[0].Name() != removalMarkerName(record) {
				t.Fatalf("recovered marker entries = %v", entries)
			}
		})
	}
}

func TestWorkspaceStatusDirtyIgnoresOnlyActiveRemovalFiles(t *testing.T) {
	record := workspaceRecord{
		Removal: &workspaceRemovalRecord{
			OperationID:    "47474747-4747-4747-8747-474747474747",
			DirectoryToken: "48484848-4848-4848-8848-484848484848",
		},
	}
	temporary := removalMarkerTemporaryPrefix(record) +
		"49494949-4949-4949-8949-494949494949"
	clean := []byte(
		"?? " + removalMarkerName(record) + "\n" +
			"?? " + temporary + "\n",
	)
	if workspaceStatusDirty(clean, &record) {
		t.Fatalf("active removal files were classified as dirty: %q", clean)
	}
	for _, dirty := range [][]byte{
		[]byte("?? " + removalMarkerTemporaryPrefix(record) + "not-a-uuid\n"),
		[]byte("?? .drove-removal-other.install-49494949-4949-4949-8949-494949494949\n"),
	} {
		if !workspaceStatusDirty(dirty, &record) {
			t.Fatalf("unrelated removal file was ignored: %q", dirty)
		}
	}
}

func TestReconcileStartedRemovalDoesNotRecreateMissingMarker(t *testing.T) {
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
		t.Fatalf("prepare workspace: %v", err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read workspace record: exists=%v err=%v", exists, err)
	}
	record.Removal = &workspaceRemovalRecord{
		OperationID:    "50505050-5050-4050-8050-505050505050",
		DirectoryToken: "51515151-5151-4151-8151-515151515151",
		Force:          true,
		Started:        true,
		Quarantined:    true,
	}
	root, err := openRealPathRoot(prepared.Path)
	if err != nil {
		t.Fatalf("open workspace root: %v", err)
	}
	if err := ensureRemovalMarker(root, record); err != nil {
		_ = root.Close()
		t.Fatalf("install removal marker: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close workspace root: %v", err)
	}
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("persist started removal: %v", err)
	}
	quarantineName := removalQuarantinePrefix(record) +
		"52525252-5252-4252-8252-525252525252"
	quarantinePath := filepath.Join(filepath.Dir(prepared.Path), quarantineName)
	if err := os.Rename(prepared.Path, quarantinePath); err != nil {
		t.Fatalf("quarantine workspace: %v", err)
	}
	markerPath := filepath.Join(quarantinePath, removalMarkerName(record))
	if err := os.Remove(markerPath); err != nil {
		t.Fatalf("remove quarantine marker: %v", err)
	}

	if _, err := manager.ReconcileRemovals(context.Background()); err == nil {
		t.Fatal("reconciliation recreated a missing started marker")
	}
	if _, err := os.Stat(quarantinePath); err != nil {
		t.Fatalf("quarantine was removed: %v", err)
	}
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing marker was recreated: %v", err)
	}
}

func TestReconcileContentsClearedRemovalAcceptsMissingMarker(t *testing.T) {
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
		t.Fatalf("prepare workspace: %v", err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read workspace record: exists=%v err=%v", exists, err)
	}
	record.Removal = &workspaceRemovalRecord{
		OperationID:     "53535353-5353-4353-8353-535353535353",
		DirectoryToken:  "54545454-5454-4454-8454-545454545454",
		Force:           true,
		Started:         true,
		Quarantined:     true,
		ContentsCleared: true,
	}
	quarantineName := removalQuarantinePrefix(record) +
		"55555555-5555-4555-8555-555555555555"
	quarantinePath := filepath.Join(filepath.Dir(prepared.Path), quarantineName)
	if err := os.Rename(prepared.Path, quarantinePath); err != nil {
		t.Fatalf("quarantine workspace: %v", err)
	}
	entries, err := os.ReadDir(quarantinePath)
	if err != nil {
		t.Fatalf("read quarantine: %v", err)
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(quarantinePath, entry.Name())); err != nil {
			t.Fatalf("clear quarantine entry %q: %v", entry.Name(), err)
		}
	}
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("persist cleared removal: %v", err)
	}

	removals, err := manager.ReconcileRemovals(context.Background())
	if err != nil {
		t.Fatalf("reconcile cleared removal: %v", err)
	}
	if len(removals) != 1 || removals[0].Workspace.Path != prepared.Path {
		t.Fatalf("reconciled removals = %+v", removals)
	}
	if _, err := os.Lstat(quarantinePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleared quarantine remains or inspect failed: %v", err)
	}
}

func TestReconcileContentsClearedRemovalRejectsUnexpectedEntry(t *testing.T) {
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
		t.Fatalf("prepare workspace: %v", err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read workspace record: exists=%v err=%v", exists, err)
	}
	record.Removal = &workspaceRemovalRecord{
		OperationID:     "56565656-5656-4656-8656-565656565656",
		DirectoryToken:  "57575757-5757-4757-8757-575757575757",
		Force:           true,
		Started:         true,
		Quarantined:     true,
		ContentsCleared: true,
	}
	quarantineName := removalQuarantinePrefix(record) +
		"58585858-5858-4858-8858-585858585858"
	quarantinePath := filepath.Join(filepath.Dir(prepared.Path), quarantineName)
	if err := os.Rename(prepared.Path, quarantinePath); err != nil {
		t.Fatalf("quarantine workspace: %v", err)
	}
	entries, err := os.ReadDir(quarantinePath)
	if err != nil {
		t.Fatalf("read quarantine: %v", err)
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(quarantinePath, entry.Name())); err != nil {
			t.Fatalf("clear quarantine entry %q: %v", entry.Name(), err)
		}
	}
	unexpected := filepath.Join(quarantinePath, "unexpected")
	if err := os.WriteFile(unexpected, []byte("preserve\n"), 0o600); err != nil {
		t.Fatalf("write unexpected entry: %v", err)
	}
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("persist cleared removal: %v", err)
	}

	if _, err := manager.ReconcileRemovals(
		context.Background(),
	); err == nil || !strings.Contains(err.Error(), "unexpected entry") {
		t.Fatalf("reconcile unexpected cleared entry error = %v", err)
	}
	assertFileContents(t, unexpected, "preserve\n")
}

func TestPersistRemovalStartRecordsAbsentPath(t *testing.T) {
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
		t.Fatalf("prepare workspace: %v", err)
	}
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge preparation: %v", err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read workspace record: exists=%v err=%v", exists, err)
	}
	record.Removal = &workspaceRemovalRecord{
		OperationID:    "59595959-5959-4959-8959-595959595959",
		DirectoryToken: "60606060-6060-4060-8060-606060606060",
	}
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("persist removal intent: %v", err)
	}
	savedPath := filepath.Join(t.TempDir(), "saved-worktree")
	if err := os.Rename(prepared.Path, savedPath); err != nil {
		t.Fatalf("move workspace out of managed path: %v", err)
	}
	started, err := manager.persistRemovalStart(record, false)
	if err != nil {
		t.Fatalf("persist absent-path removal start: %v", err)
	}
	if !started.Removal.Started || !started.Removal.PathAbsent {
		t.Fatalf("started removal = %+v", started.Removal)
	}
	if err := os.Rename(savedPath, prepared.Path); err != nil {
		t.Fatalf("restore workspace path: %v", err)
	}
	if _, err := manager.ReconcileRemovals(
		context.Background(),
	); err == nil || !strings.Contains(err.Error(), "appeared after removal began") {
		t.Fatalf("reconcile restored absent path error = %v", err)
	}
	if _, err := os.Lstat(prepared.Path); err != nil {
		t.Fatalf("restored workspace was removed: %v", err)
	}
}

func TestReconcileAbsentPathRemovalPreservesReplacementPath(t *testing.T) {
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
		OperationID:    "45454545-4545-4545-8545-454545454545",
		DirectoryToken: "46464646-4646-4646-8646-464646464646",
		Force:          true,
		Started:        true,
		PathAbsent:     true,
	}
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("write absent-path removal intent: %v", err)
	}
	runGit(t, repository, "worktree", "remove", "--force", prepared.Path)
	if err := os.Mkdir(prepared.Path, 0o700); err != nil {
		t.Fatalf("create replacement path: %v", err)
	}
	sentinel := filepath.Join(prepared.Path, "must-remain")
	if err := os.WriteFile(sentinel, []byte("replacement\n"), 0o600); err != nil {
		t.Fatalf("write replacement sentinel: %v", err)
	}

	if _, err := manager.ReconcileRemovals(
		context.Background(),
	); err == nil || !strings.Contains(err.Error(), "appeared after removal began") {
		t.Fatalf("reconcile replacement path error = %v", err)
	}
	assertFileContents(t, sentinel, "replacement\n")
	current, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists ||
		current.Removal == nil ||
		!current.Removal.PathAbsent {
		t.Fatalf(
			"absent-path removal = %+v, exists=%v err=%v",
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

func TestAcknowledgeRemovalRejectsWrongTokenForQuarantinedRecord(
	t *testing.T,
) {
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
	) + "69696969-6969-4969-8969-696969696969"
	quarantinePath := filepath.Join(filepath.Dir(recordPath), quarantineName)
	if err := os.Rename(recordPath, quarantinePath); err != nil {
		t.Fatalf("quarantine removal record: %v", err)
	}
	wrong := result.Removal
	wrong.operationID = "70707070-7070-4070-8070-707070707070"

	if err := manager.AcknowledgeRemoval(wrong); err == nil {
		t.Fatal("wrong token acknowledged a quarantined removal record")
	}
	if _, err := os.Lstat(quarantinePath); err != nil {
		t.Fatalf("wrong token changed acknowledgement quarantine: %v", err)
	}
	if err := manager.AcknowledgeRemoval(result.Removal); err != nil {
		t.Fatalf("acknowledge removal with correct token: %v", err)
	}
}

func TestReconcileRemovalCoalescesAcknowledgementHardLinks(t *testing.T) {
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
	result, err := manager.Remove(context.Background(), prepared.AgentID, true)
	if err != nil || result.State != RemovalComplete {
		t.Fatalf("remove workspace = %+v, %v", result, err)
	}
	recordPath := workspaceRecordPath(prepared.Path)
	prefix := recordAcknowledgementPrefix(
		prepared.AgentID,
		result.Removal.operationID,
	)
	firstPath := filepath.Join(
		filepath.Dir(recordPath),
		prefix+"71717171-7171-4717-8717-717171717171",
	)
	secondPath := filepath.Join(
		filepath.Dir(recordPath),
		prefix+"72727272-7272-4727-8727-727272727272",
	)
	if err := os.Rename(recordPath, firstPath); err != nil {
		t.Fatalf("quarantine removal record: %v", err)
	}
	if err := os.Link(firstPath, secondPath); err != nil {
		t.Skipf("create acknowledgement hard link: %v", err)
	}

	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	removals, err := restarted.ReconcileRemovals(context.Background())
	if err != nil {
		t.Fatalf("reconcile acknowledgement aliases: %v", err)
	}
	if len(removals) != 1 {
		t.Fatalf("reconciled removals = %+v, want one", removals)
	}
	if err := restarted.AcknowledgeRemoval(removals[0]); err != nil {
		t.Fatalf("acknowledge reconciled removal: %v", err)
	}
	for _, path := range []string{recordPath, firstPath, secondPath} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("acknowledgement path %q remains: %v", path, err)
		}
	}
}

func TestCoalesceAcknowledgementAliasesPreservesReplacementAfterValidation(
	t *testing.T,
) {
	rootPath := t.TempDir()
	operationID := "73737373-7373-4737-8737-737373737373"
	prefix := recordAcknowledgementPrefix(testAgentID, operationID)
	firstName := prefix + "74747474-7474-4747-8747-747474747474"
	secondName := prefix + "75757575-7575-4757-8757-757575757575"
	firstPath := filepath.Join(rootPath, firstName)
	secondPath := filepath.Join(rootPath, secondName)
	if err := os.WriteFile(firstPath, []byte("record\n"), 0o600); err != nil {
		t.Fatalf("write first acknowledgement alias: %v", err)
	}
	if err := os.Link(firstPath, secondPath); err != nil {
		t.Skipf("create acknowledgement hard link: %v", err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("open acknowledgement root: %v", err)
	}
	defer root.Close()
	originalSecondPath := secondPath + "-original"

	_, err = coalesceRecordAcknowledgementsAfterValidation(
		root,
		[]recordAcknowledgement{
			{name: firstName, operationID: operationID},
			{name: secondName, operationID: operationID},
		},
		func() {
			if err := os.Rename(secondPath, originalSecondPath); err != nil {
				t.Fatalf("move validated acknowledgement alias: %v", err)
			}
			if err := os.WriteFile(
				secondPath,
				[]byte("replacement\n"),
				0o600,
			); err != nil {
				t.Fatalf("install replacement acknowledgement path: %v", err)
			}
		},
	)
	if err == nil {
		t.Fatal("coalescing accepted a replacement acknowledgement alias")
	}
	assertFileContents(t, firstPath, "record\n")
	assertFileContents(t, originalSecondPath, "record\n")
	assertFileContents(t, secondPath, "replacement\n")
}

func TestRemoveRecoversQuarantinedAcknowledgementRecord(t *testing.T) {
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
	) + "68686868-6868-4868-8868-686868686868"
	quarantinePath := filepath.Join(filepath.Dir(recordPath), quarantineName)
	if err := os.Rename(recordPath, quarantinePath); err != nil {
		t.Fatalf("quarantine removal record: %v", err)
	}

	retried, err := manager.Remove(
		context.Background(),
		prepared.AgentID,
		true,
	)
	if err != nil || retried.State != RemovalComplete {
		t.Fatalf("retry removal = %+v, %v", retried, err)
	}
	if retried.Removal.operationID != result.Removal.operationID {
		t.Fatalf(
			"retried operation ID = %q, want %q",
			retried.Removal.operationID,
			result.Removal.operationID,
		)
	}
	if err := manager.AcknowledgeRemoval(retried.Removal); err != nil {
		t.Fatalf("acknowledge retried removal: %v", err)
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
		OperationID:    newOperationID,
		DirectoryToken: "90909090-9090-4090-8090-909090909090",
		Force:          true,
		Started:        true,
		PathAbsent:     true,
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

func testRepositoryEvidence(
	t *testing.T,
	manager *Manager,
	repository string,
) *repositoryEvidence {
	t.Helper()
	root, err := openRealPathRoot(repository)
	if err != nil {
		t.Fatalf("open test repository: %v", err)
	}
	lease, err := newPreparationLease(
		context.Background(),
		manager,
		repository,
		root,
	)
	if err != nil {
		_ = root.Close()
		t.Fatalf("capture test repository evidence: %v", err)
	}
	evidence := cloneRepositoryEvidence(&lease.evidence)
	if err := lease.Close(); err != nil {
		t.Fatalf("close test repository evidence lease: %v", err)
	}
	return evidence
}
