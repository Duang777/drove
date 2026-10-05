//go:build !windows

package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPrepareRollsBackRootedBranchAfterPostSuccessSourceSwap(
	t *testing.T,
) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	replacement := filepath.Join(parent, "replacement")
	openedSource := source + "-opened"
	initSourceSwapRepository(t, source)
	initSourceSwapRepository(t, replacement)

	manager, err := New(filepath.Join(parent, "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	manager.git = postSuccessSwapGitWrapper(
		t,
		parent,
		source,
		replacement,
		openedSource,
		"owned-branch",
	)
	if _, err := manager.Prepare(
		context.Background(),
		source,
		"",
		testAgentID,
	); err == nil {
		t.Fatal("prepare accepted a replaced source repository")
	}

	branch := "drove/" + testAgentID
	assertPreparationRefsAbsent(t, openedSource, branch)
	assertPreparationRefsAbsent(t, source, branch)
}

func TestPrepareRollsBackRootedWorktreeAfterPostSuccessSourceSwap(
	t *testing.T,
) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	replacement := filepath.Join(parent, "replacement")
	openedSource := source + "-opened"
	initSourceSwapRepository(t, source)
	initSourceSwapRepository(t, replacement)

	manager, err := New(filepath.Join(parent, "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	canonicalSource, err := resolvePath(source)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	targetPath := filepath.Join(
		manager.root,
		repositoryHash(canonicalSource),
		testAgentID,
	)
	manager.git = postSuccessSwapGitWrapper(
		t,
		parent,
		source,
		replacement,
		openedSource,
		"worktree-add",
	)
	if _, err := manager.Prepare(
		context.Background(),
		source,
		"",
		testAgentID,
	); err == nil {
		t.Fatal("prepare accepted a replaced source repository")
	}

	if _, err := os.Lstat(targetPath); !os.IsNotExist(err) {
		t.Fatalf("prepared worktree remains or inspect failed: %v", err)
	}
	if _, err := os.Lstat(workspaceRecordPath(targetPath)); !os.IsNotExist(err) {
		t.Fatalf("preparation record remains or inspect failed: %v", err)
	}
	assertWorktreeUnregistered(t, openedSource, targetPath)
	branch := "drove/" + testAgentID
	assertPreparationRefsAbsent(t, openedSource, branch)
	assertPreparationRefsAbsent(t, source, branch)
}

func TestPrepareRollsBackSuffixedWorktreeAfterPostSuccessSourceSwap(
	t *testing.T,
) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	replacement := filepath.Join(parent, "replacement")
	openedSource := source + "-opened"
	initSourceSwapRepository(t, source)
	initSourceSwapRepository(t, replacement)
	stalePath := filepath.Join(parent, "stale", testAgentID)
	if err := os.MkdirAll(filepath.Dir(stalePath), 0o700); err != nil {
		t.Fatalf("create stale worktree parent: %v", err)
	}
	runGit(
		t,
		source,
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

	manager, err := New(filepath.Join(parent, "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	canonicalSource, err := resolvePath(source)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	targetPath := filepath.Join(
		manager.root,
		repositoryHash(canonicalSource),
		testAgentID,
	)
	manager.git = postSuccessSwapGitWrapper(
		t,
		parent,
		source,
		replacement,
		openedSource,
		"worktree-add",
	)
	if _, err := manager.Prepare(
		context.Background(),
		source,
		"",
		testAgentID,
	); err == nil {
		t.Fatal("prepare accepted a replaced source repository")
	}

	if _, err := os.Lstat(targetPath); !os.IsNotExist(err) {
		t.Fatalf("prepared worktree remains or inspect failed: %v", err)
	}
	if _, err := os.Lstat(workspaceRecordPath(targetPath)); !os.IsNotExist(err) {
		t.Fatalf("preparation record remains or inspect failed: %v", err)
	}
	assertWorktreeUnregistered(t, openedSource, targetPath)
	branch := "drove/" + testAgentID
	assertPreparationRefsAbsent(t, openedSource, branch)
	assertPreparationRefsAbsent(t, source, branch)
}

func TestPrepareRollsBackAfterUnlistedAdminDirectoryCollisions(
	t *testing.T,
) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	replacement := filepath.Join(parent, "replacement")
	openedSource := source + "-opened"
	initSourceSwapRepository(t, source)
	initSourceSwapRepository(t, replacement)
	for index := 0; index <= 1000; index++ {
		name := testAgentID
		if index != 0 {
			name += strconv.Itoa(index)
		}
		if err := os.MkdirAll(
			filepath.Join(source, ".git", "worktrees", name),
			0o700,
		); err != nil {
			t.Fatalf("create stale admin directory %q: %v", name, err)
		}
	}

	manager, err := New(filepath.Join(parent, "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	canonicalSource, err := resolvePath(source)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	targetPath := filepath.Join(
		manager.root,
		repositoryHash(canonicalSource),
		testAgentID,
	)
	manager.git = postSuccessSwapGitWrapper(
		t,
		parent,
		source,
		replacement,
		openedSource,
		"worktree-add",
	)
	if _, err := manager.Prepare(
		context.Background(),
		source,
		"",
		testAgentID,
	); err == nil {
		t.Fatal("prepare accepted a replaced source repository")
	}

	if _, err := os.Lstat(targetPath); !os.IsNotExist(err) {
		t.Fatalf("prepared worktree remains or inspect failed: %v", err)
	}
	if _, err := os.Lstat(workspaceRecordPath(targetPath)); !os.IsNotExist(err) {
		t.Fatalf("preparation record remains or inspect failed: %v", err)
	}
	assertWorktreeUnregistered(t, openedSource, targetPath)
	branch := "drove/" + testAgentID
	assertPreparationRefsAbsent(t, openedSource, branch)
	assertPreparationRefsAbsent(t, source, branch)
}

func TestPrepareRemovesPrecreatedTargetWhenWorktreeAddFails(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	initSourceSwapRepository(t, source)

	manager, err := New(filepath.Join(parent, "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	canonicalSource, err := resolvePath(source)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	targetPath := filepath.Join(
		manager.root,
		repositoryHash(canonicalSource),
		testAgentID,
	)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	invoked := filepath.Join(parent, "worktree-add-invoked")
	wrapper := filepath.Join(parent, "git-wrapper-add-failure")
	script := `#!/bin/sh
saw_worktree=
for argument in "$@"; do
  if [ "$saw_worktree" = "1" ] && [ "$argument" = "add" ]; then
    test -d "$DROVE_TEST_TARGET" || exit 98
    : > "$DROVE_TEST_INVOKED"
    exit 97
  fi
  [ "$argument" = "worktree" ] && saw_worktree=1
done
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_TARGET", targetPath)
	t.Setenv("DROVE_TEST_INVOKED", invoked)
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	manager.git = wrapper

	if _, err := manager.Prepare(
		context.Background(),
		source,
		"",
		testAgentID,
	); err == nil {
		t.Fatal("prepare succeeded when worktree add failed")
	}
	if _, err := os.Lstat(invoked); err != nil {
		t.Fatalf("worktree add wrapper was not invoked: %v", err)
	}
	if _, err := os.Lstat(targetPath); !os.IsNotExist(err) {
		t.Fatalf("precreated target remains or inspect failed: %v", err)
	}
	if _, err := os.Lstat(workspaceRecordPath(targetPath)); !os.IsNotExist(err) {
		t.Fatalf("preparation record remains or inspect failed: %v", err)
	}
	branch := "drove/" + testAgentID
	assertPreparationRefsAbsent(t, source, branch)
}

func TestPreparePreservesReplacedPrecreatedTarget(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	initSourceSwapRepository(t, source)

	manager, err := New(filepath.Join(parent, "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	canonicalSource, err := resolvePath(source)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	targetPath := filepath.Join(
		manager.root,
		repositoryHash(canonicalSource),
		testAgentID,
	)
	originalPath := targetPath + "-original"
	replacementPath := filepath.Join(parent, "replacement-target")
	if err := os.Mkdir(replacementPath, 0o700); err != nil {
		t.Fatalf("create replacement target: %v", err)
	}
	sentinel := filepath.Join(replacementPath, "must-remain")
	if err := os.WriteFile(sentinel, []byte("replacement\n"), 0o600); err != nil {
		t.Fatalf("write replacement sentinel: %v", err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	wrapper := filepath.Join(parent, "git-wrapper-target-swap")
	script := `#!/bin/sh
matched=
saw_worktree=
for argument in "$@"; do
  if [ "$saw_worktree" = "1" ] && [ "$argument" = "add" ]; then
    matched=1
    break
  fi
  [ "$argument" = "worktree" ] && saw_worktree=1
done
"$DROVE_TEST_REAL_GIT" "$@"
status=$?
if [ "$status" -eq 0 ] && [ -n "$matched" ]; then
  mv "$DROVE_TEST_TARGET" "$DROVE_TEST_ORIGINAL" || exit 91
  mv "$DROVE_TEST_REPLACEMENT" "$DROVE_TEST_TARGET" || exit 92
fi
exit "$status"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_TARGET", targetPath)
	t.Setenv("DROVE_TEST_ORIGINAL", originalPath)
	t.Setenv("DROVE_TEST_REPLACEMENT", replacementPath)
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	manager.git = wrapper

	if _, err := manager.Prepare(
		context.Background(),
		source,
		"",
		testAgentID,
	); err == nil {
		t.Fatal("prepare accepted a replaced precreated target")
	}
	assertFileContents(t, filepath.Join(targetPath, "must-remain"), "replacement\n")
}

func TestPrepareRollsBackExistingBranchMovedBeforeWorktreeAdd(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	initSourceSwapRepository(t, source)
	oldOID := strings.TrimSpace(runGit(t, source, "rev-parse", "HEAD"))
	runGit(t, source, "branch", "existing", oldOID)
	if err := os.WriteFile(
		filepath.Join(source, "second.txt"),
		[]byte("second\n"),
		0o600,
	); err != nil {
		t.Fatalf("write second commit: %v", err)
	}
	runGit(t, source, "add", "second.txt")
	runGit(t, source, "commit", "-m", "second")
	newOID := strings.TrimSpace(runGit(t, source, "rev-parse", "HEAD"))

	manager, err := New(filepath.Join(parent, "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	canonicalSource, err := resolvePath(source)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	targetPath := filepath.Join(
		manager.root,
		repositoryHash(canonicalSource),
		testAgentID,
	)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	wrapper := filepath.Join(parent, "git-wrapper-move-existing-branch")
	script := `#!/bin/sh
matched=
saw_worktree=
for argument in "$@"; do
  if [ "$saw_worktree" = "1" ] && [ "$argument" = "add" ]; then
    matched=1
    break
  fi
  [ "$argument" = "worktree" ] && saw_worktree=1
done
if [ -n "$matched" ]; then
  "$DROVE_TEST_REAL_GIT" --git-dir="$DROVE_TEST_GIT_DIR" update-ref \
    refs/heads/existing "$DROVE_TEST_NEW_OID" "$DROVE_TEST_OLD_OID" || exit 96
fi
"$DROVE_TEST_REAL_GIT" "$@"
status=$?
if [ "$status" -eq 0 ] && [ -n "$matched" ]; then
  exit 97
fi
exit "$status"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_GIT_DIR", filepath.Join(source, ".git"))
	t.Setenv("DROVE_TEST_OLD_OID", oldOID)
	t.Setenv("DROVE_TEST_NEW_OID", newOID)
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	manager.git = wrapper

	if _, err := manager.Prepare(
		context.Background(),
		source,
		"existing",
		testAgentID,
	); err == nil {
		t.Fatal("prepare succeeded when worktree add reported failure")
	}
	if _, err := os.Lstat(targetPath); !os.IsNotExist(err) {
		t.Fatalf("failed preparation target remains or inspect failed: %v", err)
	}
	if _, err := os.Lstat(workspaceRecordPath(targetPath)); !os.IsNotExist(err) {
		t.Fatalf("failed preparation record remains or inspect failed: %v", err)
	}
	assertWorktreeUnregistered(t, source, targetPath)
	if branchOID := strings.TrimSpace(runGit(
		t,
		source,
		"rev-parse",
		"refs/heads/existing",
	)); branchOID != newOID {
		t.Fatalf("existing branch OID = %q, want %q", branchOID, newOID)
	}
}

func TestDiscardUsesRetainedRepositoryAfterPrepareSourceSwap(
	t *testing.T,
) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	replacement := filepath.Join(parent, "replacement")
	openedSource := source + "-opened"
	initSourceSwapRepository(t, source)
	initSourceSwapRepository(t, replacement)

	manager, err := New(filepath.Join(parent, "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	prepared, err := manager.Prepare(
		context.Background(),
		source,
		"",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare workspace: %v", err)
	}
	if err := os.Rename(source, openedSource); err != nil {
		t.Fatalf("move prepared source: %v", err)
	}
	if err := os.Rename(replacement, source); err != nil {
		t.Fatalf("install replacement source: %v", err)
	}
	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("discard through retained repository: %v", err)
	}

	if _, err := os.Lstat(prepared.Path); !os.IsNotExist(err) {
		t.Fatalf("prepared worktree remains or inspect failed: %v", err)
	}
	if _, err := os.Lstat(
		workspaceRecordPath(prepared.Path),
	); !os.IsNotExist(err) {
		t.Fatalf("preparation record remains or inspect failed: %v", err)
	}
	assertWorktreeUnregistered(t, openedSource, prepared.Path)
	assertPreparationRefsAbsent(t, openedSource, prepared.Branch)
	assertPreparationRefsAbsent(t, source, prepared.Branch)
}

func TestAcknowledgeRejectsBrokenWorktreeAfterPrepareSourceSwap(
	t *testing.T,
) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	replacement := filepath.Join(parent, "replacement")
	openedSource := source + "-opened"
	initSourceSwapRepository(t, source)
	initSourceSwapRepository(t, replacement)

	manager, err := New(filepath.Join(parent, "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	prepared, err := manager.Prepare(
		context.Background(),
		source,
		"",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare workspace: %v", err)
	}
	if err := os.Rename(source, openedSource); err != nil {
		t.Fatalf("move prepared source: %v", err)
	}
	if err := os.Rename(replacement, source); err != nil {
		t.Fatalf("install replacement source: %v", err)
	}
	if err := manager.AcknowledgePreparation(prepared); err == nil {
		t.Fatal("acknowledgement accepted a broken worktree Git pointer")
	}

	assertBranchExists(t, openedSource, prepared.Branch)
	assertGitRefExists(
		t,
		filepath.Join(openedSource, ".git"),
		branchOwnershipRef(prepared.branchOperationID),
	)
	assertPreparationRefsAbsent(t, source, prepared.Branch)
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil {
		t.Fatalf("read acknowledged record: %v", err)
	}
	if !exists ||
		record.PreparationCommitted ||
		record.BranchOperationID == "" {
		t.Fatalf("failed acknowledgement record = %+v, exists=%v", record, exists)
	}
	if err := os.Rename(source, replacement); err != nil {
		t.Fatalf("remove replacement source: %v", err)
	}
	if err := os.Rename(openedSource, source); err != nil {
		t.Fatalf("restore source repository: %v", err)
	}
	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("discard restored preparation: %v", err)
	}
}

func TestReconcileRemovalRejectsReplacementSourceRepository(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	replacement := filepath.Join(parent, "replacement")
	openedSource := source + "-opened"
	initSourceSwapRepository(t, source)
	initSourceSwapRepository(t, replacement)

	manager, err := New(filepath.Join(parent, "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	prepared, err := manager.Prepare(
		context.Background(),
		source,
		"",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare workspace: %v", err)
	}
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge workspace: %v", err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	wrapper := filepath.Join(parent, "git-wrapper-prune-failure")
	script := `#!/bin/sh
saw_worktree=
for argument in "$@"; do
  if [ "$saw_worktree" = "1" ] && [ "$argument" = "prune" ]; then
    exit 97
  fi
  [ "$argument" = "worktree" ] && saw_worktree=1
done
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	manager.git = wrapper

	result, err := manager.Remove(
		context.Background(),
		prepared.AgentID,
		true,
	)
	if err == nil || result.State != RemovalPending {
		t.Fatalf("removal with prune failure = %+v, err=%v", result, err)
	}
	manager.git = realGit
	if err := os.Rename(source, openedSource); err != nil {
		t.Fatalf("move source repository: %v", err)
	}
	if err := os.Rename(replacement, source); err != nil {
		t.Fatalf("install replacement repository: %v", err)
	}

	if _, err := manager.ReconcileRemovals(context.Background()); err == nil {
		t.Fatal("removal reconciliation accepted a replacement repository")
	}
	if listing := runGit(
		t,
		openedSource,
		"worktree",
		"list",
		"--porcelain",
	); !strings.Contains(listing, prepared.Path) {
		t.Fatal("original repository registration was unexpectedly pruned")
	}
	assertPreparationRefsAbsent(t, source, prepared.Branch)
}

func TestAcknowledgeUsesRetainedCommonGitDirectoryAfterReplacement(
	t *testing.T,
) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	initSourceSwapRepository(t, source)

	manager, err := New(filepath.Join(parent, "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	prepared, err := manager.Prepare(
		context.Background(),
		source,
		"",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare workspace: %v", err)
	}
	openedGitDirectory := replaceCommonGitDirectoryWithCopy(t, source)
	replacementGitDirectory := filepath.Join(source, ".git")
	t.Setenv("GIT_COMMON_DIR", replacementGitDirectory)

	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge through retained common Git directory: %v", err)
	}

	assertGitRefAbsent(
		t,
		openedGitDirectory,
		branchOwnershipRef(prepared.branchOperationID),
	)
	assertGitRefExists(
		t,
		replacementGitDirectory,
		branchOwnershipRef(prepared.branchOperationID),
	)
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read acknowledged record: exists=%v err=%v", exists, err)
	}
	if !record.PreparationCommitted || record.BranchOperationID != "" {
		t.Fatalf("acknowledged record = %+v", record)
	}
}

func TestDiscardUsesRetainedCommonGitDirectoryAfterReplacement(
	t *testing.T,
) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	initSourceSwapRepository(t, source)

	manager, err := New(filepath.Join(parent, "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	prepared, err := manager.Prepare(
		context.Background(),
		source,
		"",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare workspace: %v", err)
	}
	openedGitDirectory := replaceCommonGitDirectoryWithCopy(t, source)
	replacementGitDirectory := filepath.Join(source, ".git")
	t.Setenv("GIT_COMMON_DIR", replacementGitDirectory)

	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("discard through retained common Git directory: %v", err)
	}

	if _, err := os.Lstat(prepared.Path); !os.IsNotExist(err) {
		t.Fatalf("prepared worktree remains or inspect failed: %v", err)
	}
	if _, err := os.Lstat(
		workspaceRecordPath(prepared.Path),
	); !os.IsNotExist(err) {
		t.Fatalf("preparation record remains or inspect failed: %v", err)
	}
	assertGitWorktreeRegistration(t, openedGitDirectory, prepared.Path, false)
	assertGitRefAbsent(t, openedGitDirectory, "refs/heads/"+prepared.Branch)
	assertGitRefAbsent(
		t,
		openedGitDirectory,
		branchOwnershipRef(prepared.branchOperationID),
	)
	assertGitWorktreeRegistration(
		t,
		replacementGitDirectory,
		prepared.Path,
		true,
	)
	assertGitRefExists(
		t,
		replacementGitDirectory,
		"refs/heads/"+prepared.Branch,
	)
	assertGitRefExists(
		t,
		replacementGitDirectory,
		branchOwnershipRef(prepared.branchOperationID),
	)
}

func TestIncludedPathsUsesRetainedCommonGitDirectoryAfterReplacement(
	t *testing.T,
) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	replacement := filepath.Join(parent, "replacement")
	initSourceSwapRepository(t, source)
	initSourceSwapRepository(t, replacement)

	const includedPath = "included.txt"
	if err := os.WriteFile(
		filepath.Join(source, includedPath),
		[]byte("source\n"),
		0o600,
	); err != nil {
		t.Fatalf("write source include file: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(source, worktreeIncludeFile),
		[]byte(includedPath+"\n"),
		0o600,
	); err != nil {
		t.Fatalf("write source include manifest: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(replacement, includedPath),
		[]byte("replacement\n"),
		0o600,
	); err != nil {
		t.Fatalf("write replacement tracked file: %v", err)
	}
	runGit(t, replacement, "add", includedPath)
	runGit(t, replacement, "commit", "-m", "track replacement include file")

	manager, err := New(filepath.Join(parent, "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	sourceRoot, err := openRealPathRoot(source)
	if err != nil {
		t.Fatalf("open source root: %v", err)
	}
	lease, err := newPreparationLease(
		context.Background(),
		manager,
		source,
		sourceRoot,
	)
	if err != nil {
		_ = sourceRoot.Close()
		t.Fatalf("retain source repository: %v", err)
	}
	defer lease.Close()

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	wrapper := filepath.Join(parent, "git-wrapper")
	script := `#!/bin/sh
case " $* " in
  *" ls-files "*)
    mv "$DROVE_TEST_SOURCE_GIT" "$DROVE_TEST_OPENED_GIT" || exit 91
    mv "$DROVE_TEST_REPLACEMENT_GIT" "$DROVE_TEST_SOURCE_GIT" || exit 92
    ;;
esac
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_SOURCE_GIT", filepath.Join(source, ".git"))
	t.Setenv("DROVE_TEST_OPENED_GIT", filepath.Join(parent, "source-git-opened"))
	t.Setenv(
		"DROVE_TEST_REPLACEMENT_GIT",
		filepath.Join(replacement, ".git"),
	)
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	manager.git = wrapper

	paths, err := manager.includedPaths(
		context.Background(),
		source,
		sourceRoot,
		lease.repository,
	)
	if err != nil {
		t.Fatalf("evaluate include manifest through retained repository: %v", err)
	}
	if len(paths) != 1 || paths[0] != includedPath {
		t.Fatalf("included paths = %q, want [%s]", paths, includedPath)
	}
}

func TestReconcileRejectsReplacementSourceBeforeRunningGit(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	replacement := filepath.Join(parent, "replacement")
	openedSource := source + "-opened"
	dataDir := filepath.Join(parent, "data")
	initSourceSwapRepository(t, source)
	initSourceSwapRepository(t, replacement)

	manager, err := New(dataDir)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	prepared, err := manager.Prepare(
		context.Background(),
		source,
		"",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare workspace: %v", err)
	}
	if err := prepared.preparation.Close(); err != nil {
		t.Fatalf("close preparation lease: %v", err)
	}
	if err := os.Rename(source, openedSource); err != nil {
		t.Fatalf("move prepared source: %v", err)
	}
	if err := os.Rename(replacement, source); err != nil {
		t.Fatalf("install replacement source: %v", err)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	invoked := filepath.Join(parent, "git-invoked")
	wrapper := filepath.Join(parent, "git-reconcile-wrapper")
	script := `#!/bin/sh
: > "$DROVE_TEST_GIT_INVOKED"
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_GIT_INVOKED", invoked)
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	restarted.git = wrapper

	if err := restarted.ReconcilePreparations(
		context.Background(),
		nil,
	); err == nil {
		t.Fatal("reconciliation accepted a replacement source repository")
	}
	if _, err := os.Lstat(invoked); !os.IsNotExist(err) {
		t.Fatalf("reconciliation ran Git against the replacement source: %v", err)
	}
	if _, err := os.Lstat(prepared.Path); err != nil {
		t.Fatalf("prepared worktree was changed: %v", err)
	}
	if _, err := os.Lstat(workspaceRecordPath(prepared.Path)); err != nil {
		t.Fatalf("preparation record was changed: %v", err)
	}
	assertBranchExists(t, openedSource, prepared.Branch)
	runGit(
		t,
		openedSource,
		"show-ref",
		"--verify",
		branchOwnershipRef(prepared.branchOperationID),
	)
	assertPreparationRefsAbsent(t, source, prepared.Branch)
}

func TestReconcileRejectsReplacementCommonGitDirectory(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	replacement := filepath.Join(parent, "replacement")
	openedGitDirectory := filepath.Join(source, ".git-opened")
	dataDir := filepath.Join(parent, "data")
	initSourceSwapRepository(t, source)
	initSourceSwapRepository(t, replacement)

	manager, err := New(dataDir)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	prepared, err := manager.Prepare(
		context.Background(),
		source,
		"",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare workspace: %v", err)
	}
	if err := prepared.preparation.Close(); err != nil {
		t.Fatalf("close preparation lease: %v", err)
	}
	if err := os.Rename(
		filepath.Join(source, ".git"),
		openedGitDirectory,
	); err != nil {
		t.Fatalf("move common Git directory: %v", err)
	}
	if err := os.Rename(
		filepath.Join(replacement, ".git"),
		filepath.Join(source, ".git"),
	); err != nil {
		t.Fatalf("install replacement common Git directory: %v", err)
	}

	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	err = restarted.ReconcilePreparations(
		context.Background(),
		nil,
	)
	if err == nil || !strings.Contains(
		err.Error(),
		"repository identity evidence changed after restart",
	) {
		t.Fatalf("reconcile replacement common Git directory error = %v", err)
	}
	if _, err := os.Lstat(prepared.Path); err != nil {
		t.Fatalf("prepared worktree was changed: %v", err)
	}
	if _, err := os.Lstat(workspaceRecordPath(prepared.Path)); err != nil {
		t.Fatalf("preparation record was changed: %v", err)
	}
	for _, ref := range []string{
		"refs/heads/" + prepared.Branch,
		branchOwnershipRef(prepared.branchOperationID),
	} {
		command := exec.Command(
			"git",
			"--git-dir="+openedGitDirectory,
			"show-ref",
			"--verify",
			ref,
		)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("original ref %q was changed: %v\n%s", ref, err, output)
		}
	}
	assertPreparationRefsAbsent(t, source, prepared.Branch)
}

func TestReconcileRejectsReplacementLinkedWorktreeGitPointer(
	t *testing.T,
) {
	parent := t.TempDir()
	repository := filepath.Join(parent, "repository")
	source := filepath.Join(parent, "source")
	replacement := filepath.Join(parent, "replacement")
	dataDir := filepath.Join(parent, "data")
	initSourceSwapRepository(t, repository)
	runGit(
		t,
		repository,
		"worktree",
		"add",
		"-b",
		"source",
		source,
	)
	runGit(
		t,
		repository,
		"worktree",
		"add",
		"-b",
		"replacement",
		replacement,
	)

	manager, err := New(dataDir)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	prepared, err := manager.Prepare(
		context.Background(),
		source,
		"",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare workspace: %v", err)
	}
	if err := prepared.preparation.Close(); err != nil {
		t.Fatalf("close preparation lease: %v", err)
	}
	replacementPointer, err := os.ReadFile(filepath.Join(replacement, ".git"))
	if err != nil {
		t.Fatalf("read replacement Git pointer: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(source, ".git"),
		replacementPointer,
		0o600,
	); err != nil {
		t.Fatalf("replace source Git pointer: %v", err)
	}

	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	err = restarted.ReconcilePreparations(context.Background(), nil)
	if err == nil || !strings.Contains(
		err.Error(),
		"repository identity evidence changed after restart",
	) {
		t.Fatalf("reconcile replacement Git pointer error = %v", err)
	}
	if _, err := os.Lstat(prepared.Path); err != nil {
		t.Fatalf("prepared worktree was changed: %v", err)
	}
	if _, err := os.Lstat(workspaceRecordPath(prepared.Path)); err != nil {
		t.Fatalf("preparation record was changed: %v", err)
	}
	runGit(
		t,
		repository,
		"show-ref",
		"--verify",
		"refs/heads/"+prepared.Branch,
	)
	runGit(
		t,
		repository,
		"show-ref",
		"--verify",
		branchOwnershipRef(prepared.branchOperationID),
	)
}

func TestPrepareDoesNotRunCheckoutFromReplacementSource(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	replacement := filepath.Join(parent, "replacement")
	initSourceSwapRepository(t, source)
	initSourceSwapRepository(t, replacement)

	sentinel := filepath.Join(parent, "filter-ran")
	filter := filepath.Join(parent, "filter")
	script := "#!/bin/sh\nprintf ran > \"$DROVE_TEST_SENTINEL\"\ncat\n"
	if err := os.WriteFile(filter, []byte(script), 0o700); err != nil {
		t.Fatalf("write filter: %v", err)
	}
	runGit(t, replacement, "config", "filter.review.smudge", filter)
	runGit(t, replacement, "config", "filter.review.clean", "cat")
	runGit(t, replacement, "config", "filter.review.required", "true")
	if err := os.WriteFile(
		filepath.Join(replacement, ".gitattributes"),
		[]byte("tracked.txt filter=review\n"),
		0o600,
	); err != nil {
		t.Fatalf("write replacement attributes: %v", err)
	}
	runGit(t, replacement, "add", ".gitattributes")
	runGit(t, replacement, "commit", "-m", "configure filter")

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	openedSource := source + "-opened"
	wrapper := filepath.Join(parent, "git-wrapper")
	wrapperScript := `#!/bin/sh
if [ "$1" = "check-ref-format" ] && [ ! -e "$DROVE_TEST_SWAPPED" ]; then
  mv "$DROVE_TEST_SOURCE" "$DROVE_TEST_OPENED" || exit 91
  mv "$DROVE_TEST_REPLACEMENT" "$DROVE_TEST_SOURCE" || exit 92
  : > "$DROVE_TEST_SWAPPED"
fi
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(wrapperScript), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_SOURCE", source)
	t.Setenv("DROVE_TEST_OPENED", openedSource)
	t.Setenv("DROVE_TEST_REPLACEMENT", replacement)
	t.Setenv("DROVE_TEST_SWAPPED", filepath.Join(parent, "swapped"))
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	t.Setenv("DROVE_TEST_SENTINEL", sentinel)

	manager, err := New(filepath.Join(parent, "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	manager.git = wrapper
	if _, err := manager.Prepare(
		context.Background(),
		source,
		"",
		testAgentID,
	); err == nil {
		t.Fatal("prepare accepted a replaced source repository")
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("replacement checkout filter ran: %v", err)
	}
}

func postSuccessSwapGitWrapper(
	t *testing.T,
	parent string,
	source string,
	replacement string,
	openedSource string,
	mode string,
) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	wrapper := filepath.Join(parent, "git-wrapper-"+mode)
	script := `#!/bin/sh
matched=
case "$DROVE_TEST_SWAP_MODE" in
  owned-branch)
    has_update_ref=
    has_create_reflog=
    for argument in "$@"; do
      [ "$argument" = "update-ref" ] && has_update_ref=1
      [ "$argument" = "--create-reflog" ] && has_create_reflog=1
    done
    [ -n "$has_update_ref" ] && [ -n "$has_create_reflog" ] && matched=1
    ;;
  worktree-add)
    saw_worktree=
    for argument in "$@"; do
      if [ "$saw_worktree" = "1" ] && [ "$argument" = "add" ]; then
        matched=1
        break
      fi
      [ "$argument" = "worktree" ] && saw_worktree=1
    done
    ;;
esac
"$DROVE_TEST_REAL_GIT" "$@"
status=$?
if [ "$status" -eq 0 ] && [ -n "$matched" ] && [ ! -e "$DROVE_TEST_SWAPPED" ]; then
  mv "$DROVE_TEST_SOURCE" "$DROVE_TEST_OPENED" || exit 91
  mv "$DROVE_TEST_REPLACEMENT" "$DROVE_TEST_SOURCE" || exit 92
  : > "$DROVE_TEST_SWAPPED"
fi
exit "$status"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_SWAP_MODE", mode)
	t.Setenv("DROVE_TEST_SOURCE", source)
	t.Setenv("DROVE_TEST_REPLACEMENT", replacement)
	t.Setenv("DROVE_TEST_OPENED", openedSource)
	t.Setenv("DROVE_TEST_SWAPPED", filepath.Join(parent, "swapped-"+mode))
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	return wrapper
}

func replaceCommonGitDirectoryWithCopy(
	t *testing.T,
	source string,
) string {
	t.Helper()
	commonGitDirectory := filepath.Join(source, ".git")
	replacement := filepath.Join(t.TempDir(), "replacement-git")
	if err := os.CopyFS(replacement, os.DirFS(commonGitDirectory)); err != nil {
		t.Fatalf("copy common Git directory: %v", err)
	}
	opened := filepath.Join(source, ".git-opened")
	if err := os.Rename(commonGitDirectory, opened); err != nil {
		t.Fatalf("move common Git directory: %v", err)
	}
	if err := os.Rename(replacement, commonGitDirectory); err != nil {
		t.Fatalf("install replacement common Git directory: %v", err)
	}
	return opened
}

func assertGitRefExists(t *testing.T, gitDirectory string, ref string) {
	t.Helper()
	command := exec.Command(
		"git",
		"--git-dir="+gitDirectory,
		"show-ref",
		"--verify",
		"--quiet",
		ref,
	)
	command.Env = rootedGitEnvironment(command.Environ())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Git ref %q is missing: %v\n%s", ref, err, output)
	}
}

func assertGitRefAbsent(t *testing.T, gitDirectory string, ref string) {
	t.Helper()
	command := exec.Command(
		"git",
		"--git-dir="+gitDirectory,
		"show-ref",
		"--verify",
		"--quiet",
		ref,
	)
	command.Env = rootedGitEnvironment(command.Environ())
	if output, err := command.CombinedOutput(); !isExitCode(err, 1) {
		t.Fatalf("Git ref %q remains: %v\n%s", ref, err, output)
	}
}

func assertGitWorktreeRegistration(
	t *testing.T,
	gitDirectory string,
	path string,
	want bool,
) {
	t.Helper()
	command := exec.Command(
		"git",
		"--git-dir="+gitDirectory,
		"worktree",
		"list",
		"--porcelain",
		"-z",
	)
	command.Env = rootedGitEnvironment(command.Environ())
	output, err := command.Output()
	if err != nil {
		t.Fatalf("list Git worktrees in %q: %v", gitDirectory, err)
	}
	got := strings.Contains(string(output), "worktree "+path+"\x00")
	if got != want {
		t.Fatalf(
			"Git worktree registration for %q in %q = %v, want %v",
			path,
			gitDirectory,
			got,
			want,
		)
	}
}

func assertPreparationRefsAbsent(
	t *testing.T,
	repository string,
	branch string,
) {
	t.Helper()
	command := exec.Command(
		"git",
		"-C",
		repository,
		"show-ref",
		"--verify",
		"--quiet",
		"refs/heads/"+branch,
	)
	if err := command.Run(); !isExitCode(err, 1) {
		t.Fatalf("owned branch remains in %q: %v", repository, err)
	}
	assertOwnershipRefsAbsent(t, repository)
}

func assertBranchExists(
	t *testing.T,
	repository string,
	branch string,
) {
	t.Helper()
	command := exec.Command(
		"git",
		"-C",
		repository,
		"show-ref",
		"--verify",
		"--quiet",
		"refs/heads/"+branch,
	)
	if err := command.Run(); err != nil {
		t.Fatalf("branch %q is missing from %q: %v", branch, repository, err)
	}
}

func assertOwnershipRefsAbsent(t *testing.T, repository string) {
	t.Helper()
	command := exec.Command(
		"git",
		"-C",
		repository,
		"for-each-ref",
		"--format=%(refname)",
		branchOwnershipRefPrefix,
	)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("list ownership refs in %q: %v", repository, err)
	}
	if refs := strings.TrimSpace(string(output)); refs != "" {
		t.Fatalf("ownership refs remain in %q: %s", repository, refs)
	}
}

func assertWorktreeUnregistered(
	t *testing.T,
	repository string,
	path string,
) {
	t.Helper()
	command := exec.Command(
		"git",
		"-C",
		repository,
		"worktree",
		"list",
		"--porcelain",
		"-z",
	)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("list worktrees in %q: %v", repository, err)
	}
	if strings.Contains(string(output), "worktree "+path+"\x00") {
		t.Fatalf("worktree %q remains registered in %q", path, repository)
	}
}

func initSourceSwapRepository(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("create repository: %v", err)
	}
	runGit(t, path, "init", "--initial-branch=main")
	runGit(t, path, "config", "user.name", "Test")
	runGit(t, path, "config", "user.email", "test@example.invalid")
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
