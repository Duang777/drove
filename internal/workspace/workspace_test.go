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

const testAgentID = "11111111-1111-4111-8111-111111111111"
const secondTestAgentID = "22222222-2222-4222-8222-222222222222"

func TestConcurrentPrepareRecordConflictPreservesWinner(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type prepareResult struct {
		manager   *Manager
		workspace Workspace
		err       error
	}
	results := make(chan prepareResult, 2)
	for _, manager := range []*Manager{first, second} {
		go func() {
			prepared, err := manager.Prepare(
				ctx,
				repository,
				"",
				testAgentID,
			)
			results <- prepareResult{
				manager:   manager,
				workspace: prepared,
				err:       err,
			}
		}()
	}
	var succeeded *prepareResult
	var failed int
	for range 2 {
		result := <-results
		if result.err != nil {
			failed++
			continue
		}
		current := result
		succeeded = &current
	}
	if succeeded == nil || failed != 1 {
		t.Fatalf("concurrent prepare = success %+v, failures %d", succeeded, failed)
	}
	record, exists, err := succeeded.manager.readWorkspaceRecord(
		succeeded.workspace.Path,
	)
	if err != nil || !exists ||
		record.BranchOperationID != succeeded.workspace.branchOperationID {
		t.Fatalf(
			"winning record = %+v, exists=%v err=%v",
			record,
			exists,
			err,
		)
	}
	if _, err := os.Stat(succeeded.workspace.Path); err != nil {
		t.Fatalf("winning workspace was removed: %v", err)
	}
	if err := succeeded.manager.Discard(
		context.Background(),
		succeeded.workspace,
	); err != nil {
		t.Fatalf("discard winning workspace: %v", err)
	}
}

func TestInstallWorkspaceRecordRejectsOversizedEncoding(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	repository := filepath.Join(t.TempDir(), "repository")
	path := filepath.Join(
		manager.root,
		repositoryHash(repository),
		testAgentID,
	)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create record directory: %v", err)
	}
	record := newWorkspaceRecord(Workspace{
		AgentID:    testAgentID,
		Repository: repository,
		Path:       path,
		Branch:     strings.Repeat("b", maxWorkspaceRecordSize),
	}, nil)

	err = manager.installWorkspaceRecord(record, true)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("install oversized record error = %v", err)
	}
	if _, statErr := os.Lstat(workspaceRecordPath(path)); !errors.Is(
		statErr,
		os.ErrNotExist,
	) {
		t.Fatalf("oversized record exists or inspect failed: %v", statErr)
	}
}

func TestInstallWorkspaceRecordRequireAbsentIsAtomic(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	firstManager, err := New(dataDir)
	if err != nil {
		t.Fatalf("new first manager: %v", err)
	}
	secondManager, err := New(dataDir)
	if err != nil {
		t.Fatalf("new second manager: %v", err)
	}
	repository := filepath.Join(t.TempDir(), "repository")
	for index, manager := range []*Manager{firstManager, secondManager} {
		if err := manager.ensureManagedRoot(); err != nil {
			t.Fatalf("ensure managed root %d: %v", index, err)
		}
		if err := manager.ensureManagedBucket(repository); err != nil {
			t.Fatalf("ensure managed bucket %d: %v", index, err)
		}
	}
	target := Workspace{
		AgentID:    testAgentID,
		Repository: repository,
		Path: filepath.Join(
			firstManager.root,
			repositoryHash(repository),
			testAgentID,
		),
	}
	records := []workspaceRecord{
		newWorkspaceRecord(target, nil),
		newWorkspaceRecord(target, nil),
	}
	records[0].Branch = "first"
	records[1].Branch = "second"
	type installResult struct {
		installed bool
		err       error
	}
	start := make(chan struct{})
	results := make(chan installResult, 2)
	for index, manager := range []*Manager{firstManager, secondManager} {
		go func(manager *Manager, record workspaceRecord) {
			<-start
			installed, err := manager.installWorkspaceRecordState(
				record,
				true,
			)
			results <- installResult{installed: installed, err: err}
		}(manager, records[index])
	}
	close(start)
	var succeeded int
	for range 2 {
		result := <-results
		if result.err == nil {
			if !result.installed {
				t.Fatal("successful installation did not report installed")
			}
			succeeded++
			continue
		}
		if result.installed {
			t.Fatalf("failed no-replace installation reported installed: %v", result.err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful installations = %d, want 1", succeeded)
	}
	record, exists, err := firstManager.readWorkspaceRecord(target.Path)
	if err != nil || !exists {
		t.Fatalf("read installed record: exists=%v err=%v", exists, err)
	}
	if record.Branch != "first" && record.Branch != "second" {
		t.Fatalf("installed branch = %q", record.Branch)
	}
}

func TestPrepareListAndCleanupWorktree(t *testing.T) {
	repository := newTestRepository(t)
	if err := os.WriteFile(
		filepath.Join(repository, ".env"),
		[]byte("TOKEN=local\n"),
		0o600,
	); err != nil {
		t.Fatalf("write included environment: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repository, "ignored.key"),
		[]byte("do not copy\n"),
		0o600,
	); err != nil {
		t.Fatalf("write unrelated ignored file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repository, "local"), 0o700); err != nil {
		t.Fatalf("create local directory: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repository, "local", "cert.pem"),
		[]byte("certificate\n"),
		0o600,
	); err != nil {
		t.Fatalf("write included certificate: %v", err)
	}

	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	prepared, err := manager.Prepare(
		context.Background(),
		filepath.Join(repository, "subdirectory"),
		"",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare worktree: %v", err)
	}
	if prepared.AgentID != testAgentID ||
		prepared.Repository != repository ||
		prepared.Branch != "drove/"+testAgentID ||
		filepath.Base(prepared.Path) != testAgentID {
		t.Fatalf("prepared workspace = %+v", prepared)
	}
	assertFileContents(t, filepath.Join(prepared.Path, "tracked.txt"), "tracked\n")
	assertFileContents(t, filepath.Join(prepared.Path, ".env"), "TOKEN=local\n")
	assertFileContents(t, filepath.Join(prepared.Path, "local", "cert.pem"), "certificate\n")
	if _, err := os.Lstat(filepath.Join(prepared.Path, "ignored.key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unlisted ignored file exists or inspect failed: %v", err)
	}
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge preparation: %v", err)
	}

	listed, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("list worktrees: %v", err)
	}
	if len(listed) != 1 || !listed[0].Dirty || listed[0].Branch != prepared.Branch {
		t.Fatalf("listed worktrees = %+v", listed)
	}

	if err := os.WriteFile(
		filepath.Join(prepared.Path, "tracked.txt"),
		[]byte("changed\n"),
		0o600,
	); err != nil {
		t.Fatalf("dirty tracked file: %v", err)
	}
	if _, err := removeWorkspace(
		t,
		manager,
		context.Background(),
		testAgentID,
		false,
	); !errors.Is(err, ErrDirty) {
		t.Fatalf("cleanup dirty worktree error = %v, want ErrDirty", err)
	}
	removed, err := removeWorkspace(t, manager, context.Background(), testAgentID, true)
	if err != nil {
		t.Fatalf("force cleanup worktree: %v", err)
	}
	if removed.Path != prepared.Path {
		t.Fatalf("removed workspace = %+v, want path %q", removed, prepared.Path)
	}
	if _, err := os.Lstat(prepared.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed worktree still exists or inspect failed: %v", err)
	}
	runGit(t, repository, "show-ref", "--verify", "refs/heads/"+prepared.Branch)
}

func TestPrepareRejectsIncludedPathThroughTrackedSymlink(t *testing.T) {
	repository := newTestRepository(t)
	outside := t.TempDir()
	link := filepath.Join(repository, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("create tracked symlink: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repository, ".gitignore"),
		[]byte(".env\nignored.key\nlocal/\nescape/\n"),
		0o600,
	); err != nil {
		t.Fatalf("extend gitignore: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repository, ".worktreeinclude"),
		[]byte(".env\nlocal/*.pem\nescape/*.txt\n"),
		0o600,
	); err != nil {
		t.Fatalf("extend worktree include: %v", err)
	}
	runGit(t, repository, "add", ".gitignore", ".worktreeinclude", "escape")
	runGit(t, repository, "commit", "-m", "add tracked symlink")

	if err := os.Remove(link); err != nil {
		t.Fatalf("replace tracked symlink: %v", err)
	}
	if err := os.Mkdir(link, 0o700); err != nil {
		t.Fatalf("create replacement directory: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(link, "escaped.txt"),
		[]byte("must stay inside\n"),
		0o600,
	); err != nil {
		t.Fatalf("write included file: %v", err)
	}

	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	_, err = manager.Prepare(
		context.Background(),
		repository,
		"",
		testAgentID,
	)
	if err == nil || !strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("prepare through tracked symlink error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "escaped.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("included file escaped worktree or inspect failed: %v", err)
	}
	command := exec.Command(
		"git",
		"-C",
		repository,
		"show-ref",
		"--verify",
		"--quiet",
		"refs/heads/drove/"+testAgentID,
	)
	if err := command.Run(); err == nil {
		t.Fatal("failed prepare retained its branch")
	}
}

func TestPrepareIsolatesConcurrentAgentChanges(t *testing.T) {
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
	second, err := manager.Prepare(
		context.Background(),
		repository,
		"",
		secondTestAgentID,
	)
	if err != nil {
		t.Fatalf("prepare second worktree: %v", err)
	}

	if err := os.WriteFile(
		filepath.Join(first.Path, "tracked.txt"),
		[]byte("first agent\n"),
		0o600,
	); err != nil {
		t.Fatalf("write first worktree: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(second.Path, "tracked.txt"),
		[]byte("second agent\n"),
		0o600,
	); err != nil {
		t.Fatalf("write second worktree: %v", err)
	}

	assertFileContents(t, filepath.Join(repository, "tracked.txt"), "tracked\n")
	assertFileContents(t, filepath.Join(first.Path, "tracked.txt"), "first agent\n")
	assertFileContents(t, filepath.Join(second.Path, "tracked.txt"), "second agent\n")
	if _, err := removeWorkspace(
		t,
		manager,
		context.Background(),
		first.AgentID,
		true,
	); err != nil {
		t.Fatalf("cleanup first worktree: %v", err)
	}
	if _, err := removeWorkspace(
		t,
		manager,
		context.Background(),
		second.AgentID,
		true,
	); err != nil {
		t.Fatalf("cleanup second worktree: %v", err)
	}
}

func TestListAndCleanupDetachedWorktree(t *testing.T) {
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
	runGit(t, prepared.Path, "checkout", "--detach")
	if err := os.WriteFile(
		filepath.Join(prepared.Path, "detached.txt"),
		[]byte("unreferenced commit\n"),
		0o600,
	); err != nil {
		t.Fatalf("write detached commit: %v", err)
	}
	runGit(t, prepared.Path, "add", "detached.txt")
	runGit(t, prepared.Path, "commit", "-m", "detached work")

	listed, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("list detached worktree: %v", err)
	}
	if len(listed) != 1 ||
		!listed[0].Detached ||
		listed[0].Branch != prepared.Branch {
		t.Fatalf("detached worktrees = %+v", listed)
	}
	if _, err := removeWorkspace(
		t,
		manager,
		context.Background(),
		testAgentID,
		false,
	); !errors.Is(err, ErrDirty) {
		t.Fatalf("cleanup detached worktree error = %v, want ErrDirty", err)
	}
	if _, err := removeWorkspace(
		t,
		manager,
		context.Background(),
		testAgentID,
		true,
	); err != nil {
		t.Fatalf("force cleanup detached worktree: %v", err)
	}
	runGit(t, repository, "show-ref", "--verify", "refs/heads/"+prepared.Branch)
}

func TestCleanupRepairsMissingWorktreeRegistration(t *testing.T) {
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
	if err := os.RemoveAll(prepared.Path); err != nil {
		t.Fatalf("remove worktree directory externally: %v", err)
	}

	listed, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("list missing worktree: %v", err)
	}
	if len(listed) != 1 || !listed[0].Missing {
		t.Fatalf("missing worktrees = %+v", listed)
	}
	if _, err := removeWorkspace(
		t,
		manager,
		context.Background(),
		testAgentID,
		false,
	); err != nil {
		t.Fatalf("cleanup missing worktree: %v", err)
	}
	if strings.Contains(
		runGit(t, repository, "worktree", "list", "--porcelain"),
		prepared.Path,
	) {
		t.Fatalf("Git retained missing worktree registration for %q", prepared.Path)
	}
	runGit(t, repository, "show-ref", "--verify", "refs/heads/"+prepared.Branch)
}

func TestPrepareRollsBackBranchWhenCheckoutFails(t *testing.T) {
	repository := newTestRepository(t)
	runGit(t, repository, "config", "filter.reject.smudge", "false")
	runGit(t, repository, "config", "filter.reject.clean", "cat")
	runGit(t, repository, "config", "filter.reject.required", "true")
	if err := os.WriteFile(
		filepath.Join(repository, ".gitattributes"),
		[]byte("tracked.txt filter=reject\n"),
		0o600,
	); err != nil {
		t.Fatalf("write attributes: %v", err)
	}
	runGit(t, repository, "add", ".gitattributes", "tracked.txt")
	runGit(t, repository, "commit", "-m", "require failing checkout filter")

	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	_, err = manager.Prepare(
		context.Background(),
		repository,
		"",
		testAgentID,
	)
	if err == nil {
		t.Fatal("prepare succeeded with a failing required smudge filter")
	}
	command := exec.Command(
		"git",
		"-C",
		repository,
		"show-ref",
		"--verify",
		"--quiet",
		"refs/heads/drove/"+testAgentID,
	)
	if err := command.Run(); err == nil {
		t.Fatal("failed checkout retained its new branch")
	}
}

func TestDiscardRollsBackNewBranch(t *testing.T) {
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
	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("discard worktree: %v", err)
	}
	if _, err := os.Lstat(prepared.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discarded worktree still exists or inspect failed: %v", err)
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
	if err := command.Run(); err == nil {
		t.Fatalf("discarded branch %q still exists", prepared.Branch)
	}
}

func TestPrepareRejectsIncludedSymlink(t *testing.T) {
	repository := newTestRepository(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(repository, "secret.link")); err != nil {
		t.Skipf("create included symlink: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repository, ".gitignore"),
		[]byte(".env\nignored.key\nlocal/\nsecret.link\n"),
		0o600,
	); err != nil {
		t.Fatalf("extend gitignore: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repository, ".worktreeinclude"),
		[]byte(".env\nlocal/*.pem\nsecret.link\n"),
		0o600,
	); err != nil {
		t.Fatalf("extend worktree include: %v", err)
	}

	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	_, err = manager.Prepare(
		context.Background(),
		repository,
		"",
		testAgentID,
	)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("prepare included symlink error = %v", err)
	}
	assertFileContents(t, outside, "outside\n")
	path := filepath.Join(
		manager.root,
		repositoryHash(repository),
		testAgentID,
	)
	if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed prepare retained worktree or inspect failed: %v", statErr)
	}
}

func TestDiscardPreservesWorkspaceWhenRegistrationCheckFails(t *testing.T) {
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

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.Discard(ctx, prepared); err == nil {
		t.Fatal("discard succeeded with a canceled registration check")
	}
	if !prepared.preparation.isClosed() {
		t.Fatal("failed discard retained its preparation lease")
	}
	if _, err := os.Lstat(prepared.Path); err != nil {
		t.Fatalf("failed discard removed worktree: %v", err)
	}
	if _, err := os.Lstat(workspaceRecordPath(prepared.Path)); err != nil {
		t.Fatalf("failed discard removed recovery record: %v", err)
	}
	runGit(t, repository, "show-ref", "--verify", "refs/heads/"+prepared.Branch)

	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("cleanup preserved worktree: %v", err)
	}
}

func TestDiscardRejectsSymlinkedRepositoryBucket(t *testing.T) {
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
	outsideBucket := filepath.Join(t.TempDir(), "repository-bucket")
	if err := os.Rename(bucket, outsideBucket); err != nil {
		t.Fatalf("move registered repository bucket: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove(bucket)
		_ = os.Rename(outsideBucket, bucket)
	})

	outsideWorkspace := filepath.Join(outsideBucket, prepared.AgentID)
	sentinel := filepath.Join(outsideWorkspace, "must-remain.txt")
	if err := os.WriteFile(sentinel, []byte("outside\n"), 0o600); err != nil {
		t.Fatalf("write outside sentinel: %v", err)
	}
	if err := os.Symlink(outsideBucket, bucket); err != nil {
		t.Fatalf("replace repository bucket with symlink: %v", err)
	}
	registered, err := manager.worktreeRegistered(
		context.Background(),
		prepared.Repository,
		prepared.Path,
	)
	if err != nil || !registered {
		t.Fatalf("symlinked worktree registration = %v, %v", registered, err)
	}

	if err := manager.Discard(context.Background(), prepared); err == nil {
		t.Fatal("discard accepted a symlinked repository bucket")
	}
	assertFileContents(t, sentinel, "outside\n")
}

func TestCleanupRechecksIncludedFilesBeforeRemoval(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test requires a POSIX shell")
	}
	repository := newTestRepository(t)
	runGit(t, repository, "rm", "--cached", ".worktreeinclude")
	runGit(t, repository, "commit", "-m", "leave include manifest local")
	if err := os.WriteFile(
		filepath.Join(repository, ".env"),
		[]byte("initial local value\n"),
		0o600,
	); err != nil {
		t.Fatalf("write source included file: %v", err)
	}
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
	if _, err := os.Lstat(
		filepath.Join(prepared.Path, worktreeIncludeFile),
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target include manifest exists or inspect failed: %v", err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read workspace record: exists=%v err=%v", exists, err)
	}
	if !record.ProtectionKnown ||
		len(record.IncludedPaths) != 1 ||
		record.IncludedPaths[0] != ".env" {
		t.Fatalf("workspace record protection = %+v", record)
	}
	if err := os.Remove(filepath.Join(prepared.Path, ".env")); err != nil {
		t.Fatalf("remove copied include before cleanup: %v", err)
	}
	listed, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("list clean worktree: %v", err)
	}
	if len(listed) != 1 || listed[0].Dirty {
		t.Fatalf("worktrees before injection = %+v, want one clean worktree", listed)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := `#!/bin/sh
case " $* " in
  *" status --porcelain=v1 "*)
    count=0
    if [ -e "$DROVE_TEST_INJECT_MARKER" ]; then
      count=$(cat "$DROVE_TEST_INJECT_MARKER")
    fi
    count=$((count + 1))
    printf '%s' "$count" > "$DROVE_TEST_INJECT_MARKER"
    if [ "$count" -eq 2 ]; then
      printf 'late local value\n' > "$DROVE_TEST_INJECT_FILE"
    fi
    ;;
esac
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	t.Setenv("DROVE_TEST_INJECT_MARKER", filepath.Join(t.TempDir(), "injected"))
	injected := filepath.Join(prepared.Path, ".env")
	t.Setenv("DROVE_TEST_INJECT_FILE", injected)
	manager.git = wrapper

	if _, err := removeWorkspace(
		t,
		manager,
		context.Background(),
		prepared.AgentID,
		false,
	); !errors.Is(err, ErrDirty) {
		t.Fatalf("cleanup after late include error = %v, want ErrDirty", err)
	}
	assertFileContents(t, injected, "late local value\n")

	manager.git = realGit
	if _, err := removeWorkspace(
		t,
		manager,
		context.Background(),
		prepared.AgentID,
		true,
	); err != nil {
		t.Fatalf("force cleanup retained worktree: %v", err)
	}
}

func TestVersionOneRecordRequiresForceEvenWhenPathIsMissing(t *testing.T) {
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
	legacy, err := json.Marshal(struct {
		Version    int    `json:"version"`
		AgentID    string `json:"agent_id"`
		Repository string `json:"repository"`
		Path       string `json:"path"`
		Branch     string `json:"branch"`
	}{
		Version:    legacyWorkspaceRecordVersion,
		AgentID:    prepared.AgentID,
		Repository: prepared.Repository,
		Path:       prepared.Path,
		Branch:     prepared.Branch,
	})
	if err != nil {
		t.Fatalf("encode legacy record: %v", err)
	}
	if err := os.WriteFile(workspaceRecordPath(prepared.Path), legacy, 0o600); err != nil {
		t.Fatalf("write legacy record: %v", err)
	}
	if err := os.RemoveAll(prepared.Path); err != nil {
		t.Fatalf("remove worktree path: %v", err)
	}

	result, err := manager.Remove(context.Background(), prepared.AgentID, false)
	if !errors.Is(err, ErrDirty) || result.State != RemovalUnchanged {
		t.Fatalf("non-force legacy removal = %+v, %v", result, err)
	}
	result, err = manager.Remove(context.Background(), prepared.AgentID, true)
	if err != nil || result.State != RemovalComplete {
		t.Fatalf("force legacy removal = %+v, %v", result, err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read migrated record: exists=%v err=%v", exists, err)
	}
	if record.Version != workspaceRecordVersion ||
		record.ProtectionKnown ||
		record.Removal == nil ||
		!record.Removal.Force {
		t.Fatalf("migrated record = %+v", record)
	}
	if err := manager.AcknowledgeRemoval(result.Removal); err != nil {
		t.Fatalf("acknowledge migrated removal: %v", err)
	}
}

func TestRemoveUpgradesCommittedVersionFourRecord(t *testing.T) {
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
	record.PreparationCommitted = true
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("write committed version 4 record: %v", err)
	}
	if err := prepared.preparation.Close(); err != nil {
		t.Fatalf("close preparation lease: %v", err)
	}

	result, err := manager.Remove(
		context.Background(),
		prepared.AgentID,
		true,
	)
	if err != nil || result.State != RemovalComplete {
		t.Fatalf("remove version 4 workspace = %+v, %v", result, err)
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
		t.Fatalf("adopt upgraded removal record: %v", err)
	}
	upgraded, exists, err := restarted.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read upgraded removal record: exists=%v err=%v", exists, err)
	}
	if upgraded.Version != workspaceRecordVersion ||
		upgraded.RepositoryEvidence == nil ||
		upgraded.BranchOperationID != "" ||
		upgraded.Removal == nil {
		t.Fatalf("upgraded removal record = %+v", upgraded)
	}
	if err := restarted.AcknowledgeRemoval(result.Removal); err != nil {
		t.Fatalf("acknowledge removal: %v", err)
	}
}

func TestUpgradeVersionFiveRecordPreservesRecoveryEvidence(t *testing.T) {
	evidence := &repositoryEvidence{
		SourcePath:                 "/source",
		SourceDirectoryIdentity:    "source-identity",
		GitDirectory:               "/source/.git",
		GitDirectoryIdentity:       "git-identity",
		CommonGitDirectory:         "/source/.git",
		CommonGitDirectoryIdentity: "common-identity",
	}
	record := workspaceRecord{
		Version:              removalWorkspaceRecordVersion,
		PreparationCommitted: true,
		BranchOperationID:    "11111111-1111-4111-8111-111111111111",
		ExpectedHeadOID:      "expected-head",
		RepositoryEvidence:   evidence,
	}

	upgradeWorkspaceRecord(&record)

	if record.Version != workspaceRecordVersion ||
		record.BranchOperationID !=
			"11111111-1111-4111-8111-111111111111" ||
		record.ExpectedHeadOID != "expected-head" ||
		record.RepositoryEvidence != evidence ||
		record.PreparedStageDirectoryIdentity != "" {
		t.Fatalf("upgraded version 5 record = %+v", record)
	}
}

func TestRemoveReturnsPendingAfterPartialGitMutation(t *testing.T) {
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
	realGit := manager.git
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := `#!/bin/sh
case " $* " in
  *" worktree prune "*)
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

	result, err := manager.Remove(context.Background(), prepared.AgentID, true)
	if err == nil || result.State != RemovalPending {
		t.Fatalf("partial removal = %+v, %v, want pending error", result, err)
	}
	if _, err := os.Lstat(workspaceRecordPath(prepared.Path)); err != nil {
		t.Fatalf("partial removal lost sidecar: %v", err)
	}
	record, exists, recordErr := manager.readWorkspaceRecord(prepared.Path)
	if recordErr != nil || !exists || record.Removal == nil {
		t.Fatalf(
			"partial removal intent = %+v, exists=%v err=%v",
			record.Removal,
			exists,
			recordErr,
		)
	}
	if !strings.Contains(
		runGit(t, repository, "worktree", "list", "--porcelain"),
		prepared.Path,
	) {
		t.Fatal("partial removal unexpectedly removed Git registration")
	}

	manager.git = realGit
	removals, err := manager.ReconcileRemovals(context.Background())
	if err != nil {
		t.Fatalf("reconcile partial removal: %v", err)
	}
	if len(removals) != 1 || removals[0].Workspace.Path != prepared.Path {
		t.Fatalf("reconciled removals = %+v", removals)
	}
	if err := manager.AcknowledgeRemoval(removals[0]); err != nil {
		t.Fatalf("acknowledge reconciled removal: %v", err)
	}
}

func TestRemoveRevalidatesExistingNonForceIntent(t *testing.T) {
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
		OperationID:    "99999999-9999-4999-8999-999999999999",
		DirectoryToken: "98989898-9898-4898-8989-989898989898",
	}
	markerName := removalMarkerName(record)
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("write removal intent: %v", err)
	}
	root, err := openRealPathRoot(prepared.Path)
	if err != nil {
		t.Fatalf("open worktree for removal marker: %v", err)
	}
	markerErr := ensureRemovalMarker(root, record)
	closeErr := root.Close()
	if err := errors.Join(markerErr, closeErr); err != nil {
		t.Fatalf("install removal marker: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(prepared.Path, "tracked.txt"),
		[]byte("changed after intent\n"),
		0o600,
	); err != nil {
		t.Fatalf("dirty worktree: %v", err)
	}

	result, err := manager.Remove(context.Background(), prepared.AgentID, false)
	if !errors.Is(err, ErrDirty) || result.State != RemovalUnchanged {
		t.Fatalf("resume non-force removal = %+v, %v", result, err)
	}
	record, exists, err = manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists || record.Removal != nil {
		t.Fatalf("record after rejected removal = %+v, exists=%v err=%v", record, exists, err)
	}
	assertFileContents(
		t,
		filepath.Join(prepared.Path, "tracked.txt"),
		"changed after intent\n",
	)
	if _, err := os.Lstat(
		filepath.Join(prepared.Path, markerName),
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected removal marker remains: %v", err)
	}
}

func TestRemoveUpgradesExistingIntentToForce(t *testing.T) {
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
	const operationID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	record.Removal = &workspaceRemovalRecord{
		OperationID:    operationID,
		DirectoryToken: "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
	}
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("write non-force removal intent: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(prepared.Path, "tracked.txt"),
		[]byte("changed after intent\n"),
		0o600,
	); err != nil {
		t.Fatalf("dirty worktree: %v", err)
	}

	result, err := manager.Remove(context.Background(), prepared.AgentID, true)
	if err != nil || result.State != RemovalComplete {
		t.Fatalf("force resume removal = %+v, %v", result, err)
	}
	record, exists, err = manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists ||
		record.Removal == nil ||
		!record.Removal.Force ||
		record.Removal.OperationID != operationID {
		t.Fatalf(
			"upgraded record = %+v, exists=%v err=%v",
			record,
			exists,
			err,
		)
	}
	if err := manager.AcknowledgeRemoval(result.Removal); err != nil {
		t.Fatalf("acknowledge force removal: %v", err)
	}
}

func TestReconcileRemovalDeletesPresentUnregisteredPath(t *testing.T) {
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
		OperationID:    "33333333-3333-4333-8333-333333333333",
		DirectoryToken: "34343434-3434-4434-8434-343434343434",
		Force:          true,
	}
	root, err := openRealPathRoot(prepared.Path)
	if err != nil {
		t.Fatalf("open prepared workspace: %v", err)
	}
	if err := ensureRemovalMarker(root, record); err != nil {
		_ = root.Close()
		t.Fatalf("install removal marker: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close prepared workspace: %v", err)
	}
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("write removal intent: %v", err)
	}
	saved := filepath.Join(t.TempDir(), "saved-worktree")
	if err := os.Rename(prepared.Path, saved); err != nil {
		t.Fatalf("move worktree path: %v", err)
	}
	runGit(t, repository, "worktree", "remove", "--force", prepared.Path)
	if err := os.Rename(saved, prepared.Path); err != nil {
		t.Fatalf("restore unregistered worktree path: %v", err)
	}

	removals, err := manager.ReconcileRemovals(context.Background())
	if err != nil {
		t.Fatalf("reconcile unregistered path: %v", err)
	}
	if len(removals) != 1 {
		t.Fatalf("reconciled removals = %+v", removals)
	}
	if _, err := os.Lstat(prepared.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unregistered path remains or inspect failed: %v", err)
	}
	if err := manager.AcknowledgeRemoval(removals[0]); err != nil {
		t.Fatalf("acknowledge removal: %v", err)
	}
}

func TestReconcileNonForceRemovalPreservesPresentUnregisteredPath(t *testing.T) {
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
		OperationID:    "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		DirectoryToken: "a1a1a1a1-a1a1-41a1-81a1-a1a1a1a1a1a1",
	}
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("write removal intent: %v", err)
	}
	saved := filepath.Join(t.TempDir(), "saved-worktree")
	if err := os.Rename(prepared.Path, saved); err != nil {
		t.Fatalf("move worktree path: %v", err)
	}
	runGit(t, repository, "worktree", "remove", "--force", prepared.Path)
	if err := os.Rename(saved, prepared.Path); err != nil {
		t.Fatalf("restore unregistered worktree path: %v", err)
	}
	marker := filepath.Join(prepared.Path, "new-local-data")
	if err := os.WriteFile(marker, []byte("preserve\n"), 0o600); err != nil {
		t.Fatalf("write replacement data: %v", err)
	}

	if _, err := manager.ReconcileRemovals(context.Background()); !errors.Is(
		err,
		ErrDirty,
	) {
		t.Fatalf("reconcile non-force unregistered path error = %v, want ErrDirty", err)
	}
	assertFileContents(t, marker, "preserve\n")
	record, exists, err = manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists || record.Removal == nil {
		t.Fatalf("record after failed reconciliation = %+v, exists=%v err=%v", record, exists, err)
	}
}

func TestReconcileNonForceRemovalPreservesMissingDetachedRegistration(t *testing.T) {
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
	runGit(t, prepared.Path, "checkout", "--detach")
	if err := os.WriteFile(
		filepath.Join(prepared.Path, "detached-only.txt"),
		[]byte("last commit\n"),
		0o600,
	); err != nil {
		t.Fatalf("write detached commit: %v", err)
	}
	runGit(t, prepared.Path, "add", "detached-only.txt")
	runGit(t, prepared.Path, "commit", "-m", "detached only")
	detachedHead := strings.TrimSpace(runGit(t, prepared.Path, "rev-parse", "HEAD"))

	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read workspace record: exists=%v err=%v", exists, err)
	}
	record.Removal = &workspaceRemovalRecord{
		OperationID:    "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		DirectoryToken: "b1b1b1b1-b1b1-41b1-81b1-b1b1b1b1b1b1",
	}
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("write removal intent: %v", err)
	}
	if err := os.RemoveAll(prepared.Path); err != nil {
		t.Fatalf("remove detached worktree path: %v", err)
	}

	if _, err := manager.ReconcileRemovals(context.Background()); !errors.Is(
		err,
		ErrDirty,
	) {
		t.Fatalf("reconcile missing detached worktree error = %v, want ErrDirty", err)
	}
	porcelain := runGit(t, repository, "worktree", "list", "--porcelain")
	if !strings.Contains(porcelain, filepath.ToSlash(prepared.Path)) ||
		!strings.Contains(porcelain, detachedHead) ||
		!strings.Contains(porcelain, "detached") {
		t.Fatalf("detached registration was not preserved:\n%s", porcelain)
	}
	record, exists, err = manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists || record.Removal == nil {
		t.Fatalf("record after failed reconciliation = %+v, exists=%v err=%v", record, exists, err)
	}
}

func TestReconcilePendingRemovalRequiresGit(t *testing.T) {
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
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read workspace record: exists=%v err=%v", exists, err)
	}
	record.Removal = &workspaceRemovalRecord{
		OperationID:    "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		DirectoryToken: "c1c1c1c1-c1c1-41c1-81c1-c1c1c1c1c1c1",
		Force:          true,
	}
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("write removal intent: %v", err)
	}

	t.Setenv("PATH", t.TempDir())
	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("restart manager without Git: %v", err)
	}
	if _, err := restarted.ReconcileRemovals(context.Background()); err == nil {
		t.Fatal("pending removal reconciled without Git")
	}
	record, exists, err = restarted.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists || record.Removal == nil {
		t.Fatalf("pending record after failed reconciliation = %+v, exists=%v err=%v", record, exists, err)
	}
}

func TestReconcileRemovalReturnsAlreadyAbsentWorkspace(t *testing.T) {
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
		OperationID:    "55555555-5555-4555-8555-555555555555",
		DirectoryToken: "54545454-5454-4454-8454-545454545454",
		Force:          true,
	}
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("write removal intent: %v", err)
	}
	runGit(t, repository, "worktree", "remove", "--force", prepared.Path)

	removals, err := manager.ReconcileRemovals(context.Background())
	if err != nil {
		t.Fatalf("reconcile absent workspace: %v", err)
	}
	if len(removals) != 1 || removals[0].Workspace.Path != prepared.Path {
		t.Fatalf("reconciled removals = %+v", removals)
	}
	if err := manager.AcknowledgeRemoval(removals[0]); err != nil {
		t.Fatalf("acknowledge absent workspace: %v", err)
	}
}

func TestRemoveRecordsInitiallyAbsentPath(t *testing.T) {
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
	runGit(t, repository, "worktree", "remove", "--force", prepared.Path)

	result, err := manager.Remove(
		context.Background(),
		prepared.AgentID,
		true,
	)
	if err != nil || result.State != RemovalComplete {
		t.Fatalf("remove absent workspace = %+v, %v", result, err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists ||
		record.Removal == nil ||
		!record.Removal.PathAbsent ||
		!record.Removal.Started {
		t.Fatalf(
			"absent removal record = %+v, exists=%v err=%v",
			record.Removal,
			exists,
			err,
		)
	}
	if err := manager.AcknowledgeRemoval(result.Removal); err != nil {
		t.Fatalf("acknowledge absent workspace: %v", err)
	}
}

func TestReconcileClearsUnsafeNonForceIntent(t *testing.T) {
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
		OperationID:    "66666666-6666-4666-8666-666666666666",
		DirectoryToken: "67676767-6767-4767-8767-676767676767",
	}
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("write removal intent: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(prepared.Path, "tracked.txt"),
		[]byte("changed after intent\n"),
		0o600,
	); err != nil {
		t.Fatalf("dirty worktree: %v", err)
	}

	removals, err := manager.ReconcileRemovals(context.Background())
	if err != nil {
		t.Fatalf("reconcile unsafe intent: %v", err)
	}
	if len(removals) != 0 {
		t.Fatalf("unsafe intent produced removals = %+v", removals)
	}
	record, exists, err = manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists || record.Removal != nil {
		t.Fatalf("record after safe rollback = %+v, exists=%v err=%v", record, exists, err)
	}
	if _, err := os.Lstat(prepared.Path); err != nil {
		t.Fatalf("unsafe reconciliation removed workspace: %v", err)
	}
}

func TestAcknowledgeRemovalValidatesTokenAndIsIdempotent(t *testing.T) {
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
	wrong := result.Removal
	wrong.operationID = "44444444-4444-4444-8444-444444444444"
	if err := manager.AcknowledgeRemoval(wrong); err == nil {
		t.Fatal("acknowledgement accepted the wrong operation token")
	}
	if err := manager.AcknowledgeRemoval(result.Removal); err != nil {
		t.Fatalf("acknowledge removal: %v", err)
	}
	if err := manager.AcknowledgeRemoval(result.Removal); err != nil {
		t.Fatalf("repeat acknowledgement: %v", err)
	}
}

func TestPrepareUsesExistingBranchAndDiscardPreservesIt(t *testing.T) {
	repository := newTestRepository(t)
	runGit(t, repository, "branch", "existing")
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	prepared, err := manager.Prepare(
		context.Background(),
		repository,
		"existing",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare existing branch: %v", err)
	}
	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("discard existing branch worktree: %v", err)
	}
	runGit(t, repository, "show-ref", "--verify", "refs/heads/existing")
}

func TestPrepareRejectsBranchCheckedOutInSourceWorktree(t *testing.T) {
	repository := newTestRepository(t)
	branch := strings.TrimSpace(
		runGit(t, repository, "symbolic-ref", "--short", "HEAD"),
	)
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	if prepared, err := manager.Prepare(
		context.Background(),
		repository,
		branch,
		testAgentID,
	); err == nil {
		_ = manager.Discard(context.Background(), prepared)
		t.Fatal("prepare reused the source worktree branch")
	}
	if got := strings.TrimSpace(
		runGit(t, repository, "symbolic-ref", "--short", "HEAD"),
	); got != branch {
		t.Fatalf("source branch = %q, want %q", got, branch)
	}
	if status := runGit(t, repository, "status", "--porcelain"); status != "" {
		t.Fatalf("source worktree changed after rejected prepare:\n%s", status)
	}
}

func TestPrepareSkipsIncludeTrackedByTargetBranch(t *testing.T) {
	repository := newTestRepository(t)
	if err := os.WriteFile(
		filepath.Join(repository, "local.env"),
		[]byte("tracked\n"),
		0o600,
	); err != nil {
		t.Fatalf("write tracked target file: %v", err)
	}
	runGit(t, repository, "add", "local.env")
	runGit(t, repository, "commit", "-m", "track target local file")
	runGit(t, repository, "branch", "target-with-local-file")
	runGit(t, repository, "rm", "local.env")
	if err := os.WriteFile(
		filepath.Join(repository, ".gitignore"),
		[]byte("local.env\n"),
		0o600,
	); err != nil {
		t.Fatalf("write gitignore: %v", err)
	}
	runGit(t, repository, "add", ".gitignore")
	runGit(t, repository, "commit", "-m", "ignore local file on source")
	if err := os.WriteFile(
		filepath.Join(repository, ".worktreeinclude"),
		[]byte("local.env\n"),
		0o600,
	); err != nil {
		t.Fatalf("write include manifest: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repository, "local.env"),
		[]byte("source-local\n"),
		0o600,
	); err != nil {
		t.Fatalf("write source local file: %v", err)
	}

	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	prepared, err := manager.Prepare(
		context.Background(),
		repository,
		"target-with-local-file",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare target branch: %v", err)
	}
	assertFileContents(t, filepath.Join(prepared.Path, "local.env"), "tracked\n")
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read preparation record: exists=%v err=%v", exists, err)
	}
	if len(record.IncludedPaths) != 0 {
		t.Fatalf("protected include paths = %q, want none", record.IncludedPaths)
	}
	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("discard target branch worktree: %v", err)
	}
}

func TestPrepareSkipsCaseAliasTrackedByTargetBranch(t *testing.T) {
	repository := newTestRepository(t)
	runGit(t, repository, "config", "core.ignoreCase", "true")
	if err := os.WriteFile(
		filepath.Join(repository, "Secret.local"),
		[]byte("tracked\n"),
		0o600,
	); err != nil {
		t.Fatalf("write tracked target file: %v", err)
	}
	runGit(t, repository, "add", "Secret.local")
	runGit(t, repository, "commit", "-m", "track target local file")
	runGit(t, repository, "branch", "target-with-case-alias")
	runGit(t, repository, "rm", "Secret.local")
	if err := os.WriteFile(
		filepath.Join(repository, ".gitignore"),
		[]byte("*.local\n"),
		0o600,
	); err != nil {
		t.Fatalf("write gitignore: %v", err)
	}
	runGit(t, repository, "add", ".gitignore")
	runGit(t, repository, "commit", "-m", "ignore local files on source")
	if err := os.WriteFile(
		filepath.Join(repository, worktreeIncludeFile),
		[]byte("secret.local\n"),
		0o600,
	); err != nil {
		t.Fatalf("write include manifest: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repository, "secret.local"),
		[]byte("source local\n"),
		0o600,
	); err != nil {
		t.Fatalf("write source local file: %v", err)
	}

	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	prepared, err := manager.Prepare(
		context.Background(),
		repository,
		"target-with-case-alias",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare target branch: %v", err)
	}
	assertFileContents(t, filepath.Join(prepared.Path, "Secret.local"), "tracked\n")
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read preparation record: exists=%v err=%v", exists, err)
	}
	if len(record.IncludedPaths) != 0 {
		t.Fatalf("protected include paths = %q, want none", record.IncludedPaths)
	}
	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("discard target branch worktree: %v", err)
	}
}

func TestPrepareFromLinkedWorktreeUsesCommonRepositoryIdentity(t *testing.T) {
	repository := newTestRepository(t)
	source := filepath.Join(t.TempDir(), "source-worktree")
	runGit(t, repository, "worktree", "add", "-b", "source-branch", source)
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	prepared, err := manager.Prepare(
		context.Background(),
		filepath.Join(source, "subdirectory"),
		"nested-branch",
		secondTestAgentID,
	)
	if err != nil {
		t.Fatalf("prepare from linked worktree: %v", err)
	}
	if prepared.Repository != repository {
		t.Fatalf(
			"repository = %q, want common repository %q",
			prepared.Repository,
			repository,
		)
	}
	listed, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("list nested worktree: %v", err)
	}
	if len(listed) != 1 || listed[0].Repository != repository {
		t.Fatalf("listed worktrees = %+v", listed)
	}
	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("discard nested worktree: %v", err)
	}
}

func TestPrepareFromSeparateGitDirectoryUsesWorktreeIdentity(t *testing.T) {
	parent := t.TempDir()
	repository := filepath.Join(parent, "repository")
	gitDirectory := filepath.Join(parent, "metadata", "repository.git")
	if err := os.MkdirAll(filepath.Dir(gitDirectory), 0o700); err != nil {
		t.Fatalf("create metadata directory: %v", err)
	}
	runGit(
		t,
		parent,
		"init",
		"--separate-git-dir="+gitDirectory,
		repository,
	)
	runGit(t, repository, "config", "user.email", "drove@example.com")
	runGit(t, repository, "config", "user.name", "Drove Test")
	if err := os.WriteFile(
		filepath.Join(repository, "tracked.txt"),
		[]byte("tracked\n"),
		0o600,
	); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	runGit(t, repository, "add", "tracked.txt")
	runGit(t, repository, "commit", "-m", "initial")
	repository, err := resolvePath(repository)
	if err != nil {
		t.Fatalf("resolve worktree path: %v", err)
	}

	manager, err := New(filepath.Join(parent, "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	prepared, err := manager.Prepare(
		context.Background(),
		repository,
		"separate-git-directory",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare from separate Git directory: %v", err)
	}
	if prepared.Repository != repository {
		t.Fatalf(
			"repository = %q, want worktree %q",
			prepared.Repository,
			repository,
		)
	}
	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("discard separate Git directory worktree: %v", err)
	}
}

func TestPrepareFromLinkedWorktreeUsesSourceIndexForIncludes(t *testing.T) {
	repository := newTestRepository(t)
	const includedPath = "linked-local.env"
	if err := os.WriteFile(
		filepath.Join(repository, includedPath),
		[]byte("linked local value\n"),
		0o600,
	); err != nil {
		t.Fatalf("write main worktree file: %v", err)
	}
	runGit(t, repository, "add", includedPath)
	runGit(t, repository, "commit", "-m", "track linked include candidate")

	source := filepath.Join(t.TempDir(), "source-worktree")
	runGit(t, repository, "worktree", "add", "-b", "source-branch", source)
	runGit(t, source, "rm", "--cached", includedPath)
	runGit(t, source, "commit", "-m", "untrack linked include candidate")
	if err := os.WriteFile(
		filepath.Join(source, worktreeIncludeFile),
		[]byte(includedPath+"\n"),
		0o600,
	); err != nil {
		t.Fatalf("write linked include manifest: %v", err)
	}

	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	prepared, err := manager.Prepare(
		context.Background(),
		source,
		"linked-include-branch",
		secondTestAgentID,
	)
	if err != nil {
		t.Fatalf("prepare from linked worktree: %v", err)
	}
	assertFileContents(
		t,
		filepath.Join(prepared.Path, includedPath),
		"linked local value\n",
	)
	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("discard linked include worktree: %v", err)
	}
}

func TestPrepareSupportsSubmoduleRepository(t *testing.T) {
	submoduleRepository := newTestRepository(t)
	superRepository := newTestRepository(t)
	runGit(
		t,
		superRepository,
		"-c",
		"protocol.file.allow=always",
		"submodule",
		"add",
		submoduleRepository,
		"modules/sub",
	)
	runGit(t, superRepository, "commit", "-m", "add submodule")
	submodulePath := filepath.Join(superRepository, "modules", "sub")
	resolvedSubmodulePath, err := filepath.EvalSymlinks(submodulePath)
	if err != nil {
		t.Fatalf("resolve submodule path: %v", err)
	}

	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	prepared, err := manager.Prepare(
		context.Background(),
		submodulePath,
		"",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare submodule worktree: %v", err)
	}
	if prepared.Repository != resolvedSubmodulePath {
		t.Fatalf(
			"repository = %q, want submodule root %q",
			prepared.Repository,
			resolvedSubmodulePath,
		)
	}
	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("discard submodule worktree: %v", err)
	}
}

func TestPrepareRejectsNonRepository(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	_, err = manager.Prepare(
		context.Background(),
		t.TempDir(),
		"",
		testAgentID,
	)
	if !errors.Is(err, ErrNotRepository) {
		t.Fatalf("prepare non-repository error = %v, want ErrNotRepository", err)
	}
}

func TestPrepareReportsMissingGitAsRuntimeFailure(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	manager.git = filepath.Join(t.TempDir(), "missing-git")

	_, err = manager.Prepare(
		context.Background(),
		t.TempDir(),
		"",
		testAgentID,
	)
	if err == nil {
		t.Fatal("prepare succeeded without Git")
	}
	if errors.Is(err, ErrNotRepository) {
		t.Fatalf("missing Git error = %v, want runtime failure", err)
	}
	if !strings.Contains(err.Error(), "inspect repository") {
		t.Fatalf("missing Git error = %v, want repository inspection context", err)
	}
}

func TestCleanupRejectsInvalidAndUnknownAgentIDs(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if _, err := removeWorkspace(
		t,
		manager,
		context.Background(),
		"not-an-id",
		false,
	); err == nil {
		t.Fatal("cleanup accepted an invalid Agent ID")
	}
	if _, err := removeWorkspace(
		t,
		manager,
		context.Background(),
		testAgentID,
		false,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cleanup unknown Agent ID error = %v, want ErrNotFound", err)
	}
}

func TestListRejectsSymlinkedWorktreeRoot(t *testing.T) {
	dataDir := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(dataDir, worktreeDirectory)); err != nil {
		t.Fatalf("symlink worktree root: %v", err)
	}
	manager, err := New(dataDir)
	if err != nil {
		if !strings.Contains(err.Error(), "not a real directory") {
			t.Fatalf("new manager: %v", err)
		}
		return
	}
	if _, err := manager.List(context.Background()); err == nil {
		t.Fatal("list accepted a symlinked worktree root")
	}
}

func TestListAndReconciliationRejectSymlinkedRepositoryBucket(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if err := os.MkdirAll(manager.root, 0o700); err != nil {
		t.Fatalf("create worktree root: %v", err)
	}
	bucket := filepath.Join(manager.root, "0123456789abcdef")
	if err := os.Symlink(t.TempDir(), bucket); err != nil {
		t.Skipf("create repository bucket symlink: %v", err)
	}
	if _, err := manager.List(context.Background()); err == nil {
		t.Fatal("list silently skipped a symlinked repository bucket")
	}
	if _, err := manager.ReconcileRemovals(context.Background()); err == nil {
		t.Fatal("reconciliation silently skipped a symlinked repository bucket")
	}
}

func TestReconcileStartedRemovalDoesNotRollbackAfterWorkspaceBecomesDirty(
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
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read workspace record: exists=%v err=%v", exists, err)
	}
	record.Removal = &workspaceRemovalRecord{
		OperationID:    "12121212-1212-4212-8212-121212121212",
		DirectoryToken: "13131313-1313-4313-8313-131313131313",
		Started:        true,
		Quarantined:    true,
	}
	root, err := openRealPathRoot(prepared.Path)
	if err != nil {
		t.Fatalf("open prepared workspace: %v", err)
	}
	if err := ensureRemovalMarker(root, record); err != nil {
		_ = root.Close()
		t.Fatalf("install removal marker: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close prepared workspace: %v", err)
	}
	quarantinePath := filepath.Join(
		filepath.Dir(prepared.Path),
		removalQuarantinePrefix(record)+"14141414-1414-4414-8414-141414141414",
	)
	if err := os.Rename(prepared.Path, quarantinePath); err != nil {
		t.Fatalf("quarantine prepared workspace: %v", err)
	}
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("write started removal: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(quarantinePath, "tracked.txt"),
		[]byte("changed after physical removal started\n"),
		0o600,
	); err != nil {
		t.Fatalf("dirty started removal: %v", err)
	}

	removals, err := manager.ReconcileRemovals(context.Background())
	if err != nil {
		t.Fatalf("reconcile started removal: %v", err)
	}
	if len(removals) != 1 ||
		removals[0].Workspace.Path != prepared.Path {
		t.Fatalf("reconciled removals = %+v", removals)
	}
	if _, err := os.Lstat(prepared.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("started removal retained path or inspect failed: %v", err)
	}
	if _, err := os.Lstat(quarantinePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("started removal retained quarantine or inspect failed: %v", err)
	}
	if err := manager.AcknowledgeRemoval(removals[0]); err != nil {
		t.Fatalf("acknowledge started removal: %v", err)
	}
}

func TestRemoveUsesPruneWithoutPassingManagedPathToGit(t *testing.T) {
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
	realGit := manager.git
	logPath := filepath.Join(t.TempDir(), "git-arguments")
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$DROVE_TEST_GIT_LOG"
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	t.Setenv("DROVE_TEST_GIT_LOG", logPath)
	manager.git = wrapper

	result, err := manager.Remove(
		context.Background(),
		prepared.AgentID,
		true,
	)
	if err != nil || result.State != RemovalComplete {
		t.Fatalf("remove workspace = %+v, %v", result, err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read Git arguments: %v", err)
	}
	for _, arguments := range strings.Split(string(raw), "\n") {
		if strings.Contains(arguments, "worktree remove") {
			t.Fatalf("Git received path deletion command: %q", arguments)
		}
		if strings.Contains(arguments, "worktree prune") &&
			strings.Contains(arguments, prepared.Path) {
			t.Fatalf("Git prune received managed path: %q", arguments)
		}
	}
	if err := manager.AcknowledgeRemoval(result.Removal); err != nil {
		t.Fatalf("acknowledge removal: %v", err)
	}
}

func TestPreparePreservesTrailingSpaceInRepositoryPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows paths cannot end in a space")
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve repository parent: %v", err)
	}
	repository := filepath.Join(parent, "repository ")
	initTestRepositoryAt(t, repository)
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
		t.Fatalf("prepare trailing-space repository: %v", err)
	}
	if prepared.Repository != repository {
		t.Fatalf(
			"prepared repository = %q, want %q",
			prepared.Repository,
			repository,
		)
	}
	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("discard trailing-space repository: %v", err)
	}
}

func TestPrepareSupportsNewlineInRepositoryPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows paths cannot contain a newline")
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve repository parent: %v", err)
	}
	repository := filepath.Join(parent, "repository\nline")
	initTestRepositoryAt(t, repository)
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
		t.Fatalf("prepare newline repository: %v", err)
	}
	if prepared.Repository != repository {
		t.Fatalf(
			"prepared repository = %q, want %q",
			prepared.Repository,
			repository,
		)
	}
	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("discard newline repository: %v", err)
	}
}

func TestPrepareRejectsMissingRepositoryWithoutRunningGit(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	manager.git = filepath.Join(t.TempDir(), "git-must-not-run")
	missing := filepath.Join(t.TempDir(), "missing")
	_, err = manager.Prepare(
		context.Background(),
		missing,
		"",
		testAgentID,
	)
	if !errors.Is(err, ErrNotRepository) {
		t.Fatalf("prepare missing repository error = %v, want ErrNotRepository", err)
	}
}

func TestValidateBranchPreservesRuntimeGitFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test requires a POSIX shell")
	}
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexit 2\n"), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	manager.git = wrapper

	err = manager.validateBranch(context.Background(), "valid-name")
	if err == nil {
		t.Fatal("branch validation ignored Git runtime failure")
	}
	if errors.Is(err, ErrInvalidBranch) {
		t.Fatalf("runtime branch validation error = %v, want non-request error", err)
	}
}

func TestValidateBranchRejectsHEAD(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	err = manager.validateBranch(context.Background(), "HEAD")
	if !errors.Is(err, ErrInvalidBranch) {
		t.Fatalf("validate HEAD error = %v, want ErrInvalidBranch", err)
	}
}

func TestValidateBranchRejectsPreviousCheckoutExpression(t *testing.T) {
	repository := newTestRepository(t)
	runGit(t, repository, "checkout", "-b", "current")
	t.Chdir(repository)

	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	err = manager.validateBranch(context.Background(), "@{-1}")
	if !errors.Is(err, ErrInvalidBranch) {
		t.Fatalf(
			"validate previous checkout expression error = %v, want ErrInvalidBranch",
			err,
		)
	}
}

func newTestRepository(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	repository := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(filepath.Join(repository, "subdirectory"), 0o700); err != nil {
		t.Fatalf("create repository: %v", err)
	}
	runGit(t, repository, "init", "--initial-branch=main")
	runGit(t, repository, "config", "core.autocrlf", "false")
	runGit(t, repository, "config", "core.longpaths", "true")
	runGit(t, repository, "config", "user.name", "Drove Test")
	runGit(t, repository, "config", "user.email", "drove@example.invalid")
	files := map[string]string{
		".gitignore":            ".env\nignored.key\nlocal/\n",
		".worktreeinclude":      ".env\nlocal/*.pem\n",
		"subdirectory/keep.txt": "keep\n",
		"tracked.txt":           "tracked\n",
	}
	for name, contents := range files {
		if err := os.WriteFile(
			filepath.Join(repository, name),
			[]byte(contents),
			0o600,
		); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	runGit(t, repository, "add", ".")
	runGit(t, repository, "commit", "-m", "initial")
	resolved, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatalf("resolve repository: %v", err)
	}
	return resolved
}

func initTestRepositoryAt(t *testing.T, path string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("create repository: %v", err)
	}
	runGit(t, path, "init", "--initial-branch=main")
	runGit(t, path, "config", "core.autocrlf", "false")
	runGit(t, path, "config", "user.name", "Drove Test")
	runGit(t, path, "config", "user.email", "drove@example.invalid")
	if err := os.WriteFile(
		filepath.Join(path, "tracked.txt"),
		[]byte("tracked\n"),
		0o600,
	); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	runGit(t, path, "add", ".")
	runGit(t, path, "commit", "-m", "initial")
}

func runGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf(
			"git %s failed: %v\n%s",
			strings.Join(arguments, " "),
			err,
			output,
		)
	}
	return string(output)
}

func assertFileContents(t *testing.T, path string, want string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(contents) != want {
		t.Fatalf("%s contents = %q, want %q", path, contents, want)
	}
}

func removeWorkspace(
	t *testing.T,
	manager *Manager,
	ctx context.Context,
	agentID string,
	force bool,
) (Workspace, error) {
	t.Helper()
	result, err := manager.Remove(ctx, agentID, force)
	if err != nil {
		return result.Removal.Workspace, err
	}
	if result.State != RemovalComplete {
		return result.Removal.Workspace, errors.New(
			"workspace removal did not complete",
		)
	}
	if err := manager.AcknowledgeRemoval(result.Removal); err != nil {
		return result.Removal.Workspace, err
	}
	return result.Removal.Workspace, nil
}
