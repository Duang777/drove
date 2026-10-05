package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReconcilePreparationsAdoptsDurableAndDiscardsOrphan(t *testing.T) {
	repository := newTestRepository(t)
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	durable, err := manager.Prepare(
		context.Background(),
		repository,
		"durable-preparation",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare durable workspace: %v", err)
	}
	orphan, err := manager.Prepare(
		context.Background(),
		repository,
		"orphan-preparation",
		secondTestAgentID,
	)
	if err != nil {
		t.Fatalf("prepare orphan workspace: %v", err)
	}

	if err := manager.ReconcilePreparations(
		context.Background(),
		[]Workspace{durable},
	); err != nil {
		t.Fatalf("reconcile preparations: %v", err)
	}

	record, exists, err := manager.readWorkspaceRecord(durable.Path)
	if err != nil || !exists || !record.PreparationCommitted {
		t.Fatalf(
			"durable preparation record = %+v, exists=%v err=%v",
			record,
			exists,
			err,
		)
	}
	if _, err := os.Lstat(durable.Path); err != nil {
		t.Fatalf("durable preparation was removed: %v", err)
	}
	runGit(
		t,
		repository,
		"show-ref",
		"--verify",
		"refs/heads/"+durable.Branch,
	)

	if _, err := os.Lstat(orphan.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan workspace remains or inspect failed: %v", err)
	}
	if _, err := os.Lstat(workspaceRecordPath(orphan.Path)); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("orphan preparation record remains or inspect failed: %v", err)
	}
	command := exec.Command(
		"git",
		"-C",
		repository,
		"show-ref",
		"--verify",
		"--quiet",
		"refs/heads/"+orphan.Branch,
	)
	if err := command.Run(); err == nil {
		t.Fatalf("orphan branch %q remains", orphan.Branch)
	}
}

func TestReconcilePreparationsDiscardsPendingAfterRestart(t *testing.T) {
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
		t.Fatalf("prepare workspace: %v", err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read preparation record: exists=%v err=%v", exists, err)
	}
	if record.Version != workspaceRecordVersion ||
		record.RepositoryEvidence == nil {
		t.Fatalf("preparation record has no repository evidence: %+v", record)
	}
	if err := prepared.preparation.Close(); err != nil {
		t.Fatalf("close preparation lease: %v", err)
	}

	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	if err := restarted.ReconcilePreparations(
		context.Background(),
		nil,
	); err != nil {
		t.Fatalf("reconcile preparations after restart: %v", err)
	}

	if _, err := os.Lstat(prepared.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending workspace remains or inspect failed: %v", err)
	}
	if _, err := os.Lstat(
		workspaceRecordPath(prepared.Path),
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending record remains or inspect failed: %v", err)
	}
	command := exec.Command(
		"git",
		"-C",
		repository,
		"show-ref",
		"--verify",
		"--quiet",
		"refs/heads/"+prepared.Branch,
	)
	if err := command.Run(); !isExitCode(err, 1) {
		t.Fatalf("pending branch remains after restart: %v", err)
	}
}

func TestReconcilePreparationsDiscardsPromotePendingStagingWorktree(
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
		t.Fatalf("prepare workspace: %v", err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read preparation record: exists=%v err=%v", exists, err)
	}
	stagingName, err := preparedWorktreeStagingName(
		prepared.AgentID,
		record.BranchOperationID,
	)
	if err != nil {
		t.Fatalf("derive staging name: %v", err)
	}
	stagingPath := filepath.Join(filepath.Dir(prepared.Path), stagingName)
	if err := os.Rename(prepared.Path, stagingPath); err != nil {
		t.Fatalf("restore promote-pending staging path: %v", err)
	}
	runGit(t, stagingPath, "worktree", "repair", ".")
	if err := prepared.preparation.Close(); err != nil {
		t.Fatalf("close preparation lease: %v", err)
	}

	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	if err := restarted.ReconcilePreparations(
		context.Background(),
		nil,
	); err != nil {
		t.Fatalf("reconcile staging preparation: %v", err)
	}
	for _, path := range []string{
		prepared.Path,
		stagingPath,
		workspaceRecordPath(prepared.Path),
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("reconciled path %q remains: %v", path, err)
		}
	}
	if strings.Contains(
		runGit(t, repository, "worktree", "list", "--porcelain"),
		stagingPath,
	) {
		t.Fatal("staging worktree registration remains")
	}
}

func TestReconcilePreparationWithIncompleteGitIdentityFailsClosed(
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
		t.Fatalf("prepare workspace: %v", err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists || record.DirectoryIdentity == "" {
		t.Fatalf(
			"read preparation record: exists=%v record=%+v err=%v",
			exists,
			record,
			err,
		)
	}
	record.GitDirectory = ""
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("clear prepared Git directory: %v", err)
	}
	if err := prepared.preparation.Close(); err != nil {
		t.Fatalf("close preparation lease: %v", err)
	}

	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	err = restarted.ReconcilePreparations(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "incomplete worktree identity") {
		t.Fatalf("reconcile incomplete preparation error = %v", err)
	}
	if _, err := os.Lstat(prepared.Path); err != nil {
		t.Fatalf("incomplete preparation path was changed: %v", err)
	}
	if _, err := os.Lstat(workspaceRecordPath(prepared.Path)); err != nil {
		t.Fatalf("incomplete preparation record was changed: %v", err)
	}
	runGit(
		t,
		repository,
		"show-ref",
		"--verify",
		"refs/heads/"+prepared.Branch,
	)
}

func TestReconcilePreparationsDiscardsGitSuffixedWorktreeAfterRestart(
	t *testing.T,
) {
	repository := newTestRepository(t)
	stalePath := filepath.Join(t.TempDir(), testAgentID)
	runGit(
		t,
		repository,
		"worktree",
		"add",
		"--quiet",
		"-b",
		"stale-admin-entry",
		stalePath,
	)
	if err := os.RemoveAll(stalePath); err != nil {
		t.Fatalf("remove stale worktree path: %v", err)
	}

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
		t.Fatalf("prepare workspace: %v", err)
	}
	gitDirectoryName := filepath.Base(prepared.gitDirectory)
	suffix := strings.TrimPrefix(gitDirectoryName, testAgentID)
	if suffix == "" || strings.Trim(suffix, "0123456789") != "" {
		t.Fatalf(
			"prepared Git directory %q has no collision suffix",
			prepared.gitDirectory,
		)
	}
	if err := prepared.preparation.Close(); err != nil {
		t.Fatalf("close preparation lease: %v", err)
	}

	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	if err := restarted.ReconcilePreparations(
		context.Background(),
		nil,
	); err != nil {
		t.Fatalf("reconcile preparation after restart: %v", err)
	}

	if _, err := os.Lstat(prepared.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending workspace remains or inspect failed: %v", err)
	}
	if _, err := os.Lstat(
		workspaceRecordPath(prepared.Path),
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending record remains or inspect failed: %v", err)
	}
	command := exec.Command(
		"git",
		"-C",
		repository,
		"show-ref",
		"--verify",
		"--quiet",
		"refs/heads/"+prepared.Branch,
	)
	if err := command.Run(); !isExitCode(err, 1) {
		t.Fatalf("pending branch remains after restart: %v", err)
	}
}

func TestReconcilePreparationsRejectsPendingVersionThreeRecord(t *testing.T) {
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
		t.Fatalf("prepare workspace: %v", err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read preparation record: exists=%v err=%v", exists, err)
	}
	record.Version = preparationWorkspaceRecordVersion
	record.RepositoryEvidence = nil
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("downgrade preparation record: %v", err)
	}
	if err := prepared.preparation.Close(); err != nil {
		t.Fatalf("close preparation lease: %v", err)
	}

	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	err = restarted.ReconcilePreparations(context.Background(), nil)
	if err == nil || !strings.Contains(
		err.Error(),
		"repository identity evidence",
	) {
		t.Fatalf("reconcile pending version 3 record error = %v", err)
	}
	if _, err := os.Lstat(prepared.Path); err != nil {
		t.Fatalf("pending workspace was changed: %v", err)
	}
	if _, err := os.Lstat(workspaceRecordPath(prepared.Path)); err != nil {
		t.Fatalf("pending record was changed: %v", err)
	}
	runGit(
		t,
		repository,
		"show-ref",
		"--verify",
		"refs/heads/"+prepared.Branch,
	)
}

func TestReconcilePreparationsRejectsPendingVersionFourRecord(t *testing.T) {
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
		t.Fatalf("prepare workspace: %v", err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists || record.RepositoryEvidence == nil {
		t.Fatalf("read preparation record: exists=%v err=%v", exists, err)
	}
	record.Version = repositoryWorkspaceRecordVersion
	record.RepositoryEvidence.GitDirectory = ""
	record.RepositoryEvidence.GitDirectoryIdentity = ""
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("downgrade preparation record: %v", err)
	}
	if err := prepared.preparation.Close(); err != nil {
		t.Fatalf("close preparation lease: %v", err)
	}

	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	err = restarted.ReconcilePreparations(context.Background(), nil)
	if err == nil || !strings.Contains(
		err.Error(),
		"repository identity evidence",
	) {
		t.Fatalf("reconcile pending version 4 record error = %v", err)
	}
	if _, err := os.Lstat(prepared.Path); err != nil {
		t.Fatalf("pending workspace was changed: %v", err)
	}
	if _, err := os.Lstat(workspaceRecordPath(prepared.Path)); err != nil {
		t.Fatalf("pending record was changed: %v", err)
	}
	runGit(
		t,
		repository,
		"show-ref",
		"--verify",
		"refs/heads/"+prepared.Branch,
	)
}

func TestReconcilePreparationsAdoptsCommittedVersionThreeRecord(t *testing.T) {
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
		t.Fatalf("prepare workspace: %v", err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read preparation record: exists=%v err=%v", exists, err)
	}
	if err := prepared.preparation.repository.removeOwnershipMarker(
		context.Background(),
		record.BranchOperationID,
	); err != nil {
		t.Fatalf("remove legacy ownership marker: %v", err)
	}
	record.Version = preparationWorkspaceRecordVersion
	record.RepositoryEvidence = nil
	record.PreparationCommitted = true
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("write committed version 3 record: %v", err)
	}
	if err := prepared.preparation.Close(); err != nil {
		t.Fatalf("close preparation lease: %v", err)
	}

	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	expected := Workspace{
		AgentID:    prepared.AgentID,
		Repository: prepared.Repository,
		Path:       prepared.Path,
		Branch:     prepared.Branch,
	}
	if err := restarted.ReconcilePreparations(
		context.Background(),
		[]Workspace{expected},
	); err != nil {
		t.Fatalf("adopt committed version 3 preparation: %v", err)
	}

	adopted, exists, err := restarted.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read adopted record: exists=%v err=%v", exists, err)
	}
	if adopted.Version != preparationWorkspaceRecordVersion ||
		!adopted.PreparationCommitted ||
		adopted.BranchOperationID != record.BranchOperationID {
		t.Fatalf("adopted record = %+v", adopted)
	}
	if _, err := os.Lstat(prepared.Path); err != nil {
		t.Fatalf("adopted workspace was changed: %v", err)
	}
}

func TestReconcilePreparationsAdoptsCommittedVersionFourRecord(t *testing.T) {
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
		t.Fatalf("prepare workspace: %v", err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists || record.RepositoryEvidence == nil {
		t.Fatalf("read preparation record: exists=%v err=%v", exists, err)
	}
	if err := prepared.preparation.repository.removeOwnershipMarker(
		context.Background(),
		record.BranchOperationID,
	); err != nil {
		t.Fatalf("remove legacy ownership marker: %v", err)
	}
	record.Version = repositoryWorkspaceRecordVersion
	record.RepositoryEvidence.GitDirectory = ""
	record.RepositoryEvidence.GitDirectoryIdentity = ""
	record.PreparationCommitted = true
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("write committed version 4 record: %v", err)
	}
	if err := prepared.preparation.Close(); err != nil {
		t.Fatalf("close preparation lease: %v", err)
	}

	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	expected := Workspace{
		AgentID:    prepared.AgentID,
		Repository: prepared.Repository,
		Path:       prepared.Path,
		Branch:     prepared.Branch,
	}
	if err := restarted.ReconcilePreparations(
		context.Background(),
		[]Workspace{expected},
	); err != nil {
		t.Fatalf("adopt committed version 4 preparation: %v", err)
	}

	adopted, exists, err := restarted.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read adopted record: exists=%v err=%v", exists, err)
	}
	if adopted.Version != repositoryWorkspaceRecordVersion ||
		!adopted.PreparationCommitted ||
		adopted.BranchOperationID != record.BranchOperationID {
		t.Fatalf("adopted record = %+v", adopted)
	}
	if _, err := os.Lstat(prepared.Path); err != nil {
		t.Fatalf("adopted workspace was changed: %v", err)
	}
}

func TestAcknowledgePreparationClearsBranchOwnershipMarker(t *testing.T) {
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
	if prepared.branchOperationID == "" {
		t.Fatal("prepared workspace has no branch operation ID")
	}
	exists, err := manager.branchOwnershipMarkerExists(
		context.Background(),
		repository,
		prepared.branchOperationID,
	)
	if err != nil || !exists {
		t.Fatalf("branch ownership marker exists = %v, err=%v", exists, err)
	}

	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge preparation: %v", err)
	}
	exists, err = manager.branchOwnershipMarkerExists(
		context.Background(),
		repository,
		prepared.branchOperationID,
	)
	if err != nil || exists {
		t.Fatalf("branch ownership marker exists = %v, err=%v", exists, err)
	}
	record, hasRecord, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !hasRecord {
		t.Fatalf("read acknowledged record: exists=%v err=%v", hasRecord, err)
	}
	if !record.PreparationCommitted || record.BranchOperationID != "" {
		t.Fatalf("acknowledged record = %+v", record)
	}
}

func TestAcknowledgePreparationRejectsReplacementTarget(t *testing.T) {
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
	originalPath := prepared.Path + "-original"
	if err := os.Rename(prepared.Path, originalPath); err != nil {
		t.Fatalf("move prepared target: %v", err)
	}
	if err := os.Mkdir(prepared.Path, 0o700); err != nil {
		t.Fatalf("create replacement target: %v", err)
	}
	sentinel := filepath.Join(prepared.Path, "must-remain")
	if err := os.WriteFile(sentinel, []byte("replacement\n"), 0o600); err != nil {
		t.Fatalf("write replacement sentinel: %v", err)
	}

	if err := manager.AcknowledgePreparation(prepared); err == nil {
		t.Fatal("acknowledgement accepted a replacement target")
	}
	assertFileContents(t, sentinel, "replacement\n")
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read preparation record: exists=%v err=%v", exists, err)
	}
	if record.PreparationCommitted || record.BranchOperationID == "" {
		t.Fatalf("failed acknowledgement changed record: %+v", record)
	}
	marker, err := manager.branchOwnershipMarkerExists(
		context.Background(),
		repository,
		prepared.branchOperationID,
	)
	if err != nil || !marker {
		t.Fatalf("branch ownership marker = %v, err=%v", marker, err)
	}

	if err := os.RemoveAll(prepared.Path); err != nil {
		t.Fatalf("remove replacement target: %v", err)
	}
	if err := os.Rename(originalPath, prepared.Path); err != nil {
		t.Fatalf("restore prepared target: %v", err)
	}
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge restored target: %v", err)
	}
}

func TestAcknowledgePreparationRejectsReplacedGitPointer(t *testing.T) {
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
	pointerPath := filepath.Join(prepared.Path, ".git")
	originalPointer, err := os.ReadFile(pointerPath)
	if err != nil {
		t.Fatalf("read prepared Git pointer: %v", err)
	}
	if err := os.WriteFile(
		pointerPath,
		[]byte("gitdir: "+filepath.Join(repository, ".git")+"\n"),
		0o600,
	); err != nil {
		t.Fatalf("replace prepared Git pointer: %v", err)
	}

	if err := manager.AcknowledgePreparation(prepared); err == nil {
		t.Fatal("acknowledgement accepted a replaced Git pointer")
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read preparation record: exists=%v err=%v", exists, err)
	}
	if record.PreparationCommitted || record.BranchOperationID == "" {
		t.Fatalf("failed acknowledgement changed record: %+v", record)
	}

	if err := os.WriteFile(pointerPath, originalPointer, 0o600); err != nil {
		t.Fatalf("restore prepared Git pointer: %v", err)
	}
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge restored Git pointer: %v", err)
	}
}

func TestAcknowledgePreparationDoesNotOverwriteConcurrentRemoval(
	t *testing.T,
) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX Git wrapper")
	}
	repository := newTestRepository(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	first, err := New(dataDir)
	if err != nil {
		t.Fatalf("new first manager: %v", err)
	}
	second, err := New(dataDir)
	if err != nil {
		t.Fatalf("new second manager: %v", err)
	}
	prepared, err := first.Prepare(
		context.Background(),
		repository,
		"",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare workspace: %v", err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	entered := filepath.Join(t.TempDir(), "entered")
	release := filepath.Join(t.TempDir(), "release")
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := `#!/bin/sh
case " $* " in
  *" update-ref -d refs/drove/preparations/"*)
    : > "$DROVE_TEST_ENTERED"
    while ! test -e "$DROVE_TEST_RELEASE"; do
      sleep 0.01
    done
    ;;
esac
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_ENTERED", entered)
	t.Setenv("DROVE_TEST_RELEASE", release)
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	first.git = wrapper

	ackResult := make(chan error, 1)
	go func() {
		ackResult <- first.AcknowledgePreparation(prepared)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(entered); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("acknowledgement did not reach marker cleanup")
		}
		time.Sleep(10 * time.Millisecond)
	}

	type removalAttempt struct {
		result RemovalResult
		err    error
	}
	removeResult := make(chan removalAttempt, 1)
	go func() {
		result, err := second.Remove(
			context.Background(),
			prepared.AgentID,
			true,
		)
		removeResult <- removalAttempt{result: result, err: err}
	}()
	select {
	case result := <-removeResult:
		t.Fatalf("removal bypassed shared manager lock: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatalf("release acknowledgement: %v", err)
	}
	if err := <-ackResult; err != nil {
		t.Fatalf("acknowledge preparation: %v", err)
	}
	attempt := <-removeResult
	if attempt.err != nil || attempt.result.State != RemovalComplete {
		t.Fatalf(
			"concurrent removal = %+v, err=%v",
			attempt.result,
			attempt.err,
		)
	}
	record, exists, err := second.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists || record.Removal == nil {
		t.Fatalf(
			"removal record = %+v, exists=%v err=%v",
			record,
			exists,
			err,
		)
	}
	if err := second.AcknowledgeRemoval(attempt.result.Removal); err != nil {
		t.Fatalf("acknowledge removal: %v", err)
	}
}

func TestReconcilePreparationsRejectsCommittedMetadataMismatch(t *testing.T) {
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
	otherRepository := filepath.Join(t.TempDir(), "other-repository")
	expected := Workspace{
		AgentID:    prepared.AgentID,
		Repository: otherRepository,
		Path: filepath.Join(
			manager.root,
			repositoryHash(otherRepository),
			prepared.AgentID,
		),
		Branch: prepared.Branch,
	}

	err = manager.ReconcilePreparations(
		context.Background(),
		[]Workspace{expected},
	)
	if err == nil || !strings.Contains(err.Error(), "does not match session metadata") {
		t.Fatalf("reconcile mismatched committed record error = %v", err)
	}
	if _, err := os.Lstat(prepared.Path); err != nil {
		t.Fatalf("mismatched committed workspace was changed: %v", err)
	}
}

func TestReconcilePreparationsRejectsMissingDurableRecord(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	repository := filepath.Join(t.TempDir(), "repository")
	expected := Workspace{
		AgentID:    testAgentID,
		Repository: repository,
		Path: filepath.Join(
			manager.root,
			repositoryHash(repository),
			testAgentID,
		),
		Branch: "missing-record",
	}

	err = manager.ReconcilePreparations(
		context.Background(),
		[]Workspace{expected},
	)
	if err == nil || !strings.Contains(err.Error(), "records missing") {
		t.Fatalf("reconcile missing record error = %v", err)
	}
}

func TestReconcilePreparationsLeavesRemovalForRemovalReconciliation(t *testing.T) {
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
	result, err := manager.Remove(context.Background(), prepared.AgentID, true)
	if err != nil {
		t.Fatalf("remove workspace: %v", err)
	}

	if err := manager.ReconcilePreparations(context.Background(), nil); err != nil {
		t.Fatalf("reconcile preparations with removal record: %v", err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists || record.Removal == nil {
		t.Fatalf(
			"removal record after preparation reconciliation = %+v, exists=%v err=%v",
			record,
			exists,
			err,
		)
	}
	if err := manager.AcknowledgeRemoval(result.Removal); err != nil {
		t.Fatalf("acknowledge removal: %v", err)
	}
}

func TestVersionTwoRecordLoadsAsCommittedWithStartedRemoval(t *testing.T) {
	repository := newTestRepository(t)
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	path := filepath.Join(
		manager.root,
		repositoryHash(repository),
		testAgentID,
	)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create record directory: %v", err)
	}
	raw, err := json.Marshal(struct {
		Version         int                     `json:"version"`
		AgentID         string                  `json:"agent_id"`
		Repository      string                  `json:"repository"`
		Path            string                  `json:"path"`
		Branch          string                  `json:"branch"`
		ProtectionKnown bool                    `json:"protection_known"`
		IncludedPaths   []string                `json:"included_paths"`
		Removal         *workspaceRemovalRecord `json:"removal,omitempty"`
	}{
		Version:         protectedWorkspaceRecordVersion,
		AgentID:         testAgentID,
		Repository:      repository,
		Path:            path,
		Branch:          "legacy-removal",
		ProtectionKnown: true,
		IncludedPaths:   []string{},
		Removal: &workspaceRemovalRecord{
			OperationID: "99999999-9999-4999-8999-999999999999",
		},
	})
	if err != nil {
		t.Fatalf("encode version 2 record: %v", err)
	}
	if err := os.WriteFile(workspaceRecordPath(path), raw, 0o600); err != nil {
		t.Fatalf("write version 2 record: %v", err)
	}

	record, exists, err := manager.readWorkspaceRecord(path)
	if err != nil || !exists {
		t.Fatalf("read version 2 record: exists=%v err=%v", exists, err)
	}
	if !record.PreparationCommitted ||
		record.Removal == nil ||
		!record.Removal.Started {
		t.Fatalf("migrated version 2 record = %+v", record)
	}
}

func TestReconcilePreparationsRejectsReplacedCommittedWorkspace(t *testing.T) {
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
		t.Fatalf("prepare workspace: %v", err)
	}
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge preparation: %v", err)
	}

	originalPath := prepared.Path + "-original"
	if err := os.Rename(prepared.Path, originalPath); err != nil {
		t.Fatalf("move committed workspace: %v", err)
	}
	if err := os.Mkdir(prepared.Path, 0o700); err != nil {
		t.Fatalf("create replacement workspace: %v", err)
	}
	sentinel := filepath.Join(prepared.Path, "must-remain")
	if err := os.WriteFile(sentinel, []byte("replacement\n"), 0o600); err != nil {
		t.Fatalf("write replacement sentinel: %v", err)
	}

	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	expected := Workspace{
		AgentID:    prepared.AgentID,
		Repository: prepared.Repository,
		Path:       prepared.Path,
		Branch:     prepared.Branch,
	}
	if err := restarted.ReconcilePreparations(
		context.Background(),
		[]Workspace{expected},
	); err == nil {
		t.Fatal("reconciliation accepted a replaced committed workspace")
	}
	assertFileContents(t, sentinel, "replacement\n")
}
