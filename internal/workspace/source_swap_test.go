//go:build !windows

package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	targetPath := filepath.Join(manager.root, repositoryHash(canonicalSource), testAgentID)
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
	if runtime.GOOS != "linux" {
		t.Skip("BSD registration uses a separate common-root stage")
	}
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
command_root=
next_root=
next_target=
for argument in "$@"; do
  if [ -n "$next_root" ]; then
    command_root=$argument
    next_root=
    continue
  fi
  if [ -n "$next_target" ]; then
    test -d "$argument" || exit 98
    if [ -n "$command_root" ]; then
      target=$(CDPATH= cd -- "$command_root/$argument" && pwd -P) || exit 99
    else
      target=$(CDPATH= cd -- "$argument" && pwd -P) || exit 99
    fi
    printf '%s' "$target" > "$DROVE_TEST_INVOKED"
    exit 97
  fi
  [ "$argument" = "-C" ] && next_root=1
  [ "$argument" = "--no-checkout" ] && next_target=1
done
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
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
	stagingPath, err := os.ReadFile(invoked)
	if err != nil {
		t.Fatalf("read worktree add target: %v", err)
	}
	if _, err := os.Lstat(string(stagingPath)); !os.IsNotExist(err) {
		t.Fatalf("staging target remains or inspect failed: %v", err)
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
	if runtime.GOOS != "linux" {
		t.Skip("BSD registration does not run Git from the managed target")
	}
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	initSourceSwapRepository(t, source)

	manager, err := New(filepath.Join(parent, "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	replacementPath := filepath.Join(parent, "replacement-target")
	if err := os.Mkdir(replacementPath, 0o700); err != nil {
		t.Fatalf("create replacement target: %v", err)
	}
	sentinel := filepath.Join(replacementPath, "must-remain")
	if err := os.WriteFile(sentinel, []byte("replacement\n"), 0o600); err != nil {
		t.Fatalf("write replacement sentinel: %v", err)
	}
	swappedPath := filepath.Join(parent, "swapped-target")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	wrapper := filepath.Join(parent, "git-wrapper-target-swap")
	script := `#!/bin/sh
command_root=
next_root=
target=
next_target=
for argument in "$@"; do
  if [ -n "$next_root" ]; then
    command_root=$argument
    next_root=
    continue
  fi
  if [ -n "$next_target" ]; then
    if [ -n "$command_root" ]; then
      target=$(CDPATH= cd -- "$command_root/$argument" && pwd -P) || exit 90
    else
      target=$(CDPATH= cd -- "$argument" && pwd -P) || exit 90
    fi
    next_target=
  fi
  [ "$argument" = "-C" ] && next_root=1
  [ "$argument" = "--no-checkout" ] && next_target=1
done
"$DROVE_TEST_REAL_GIT" "$@"
status=$?
if [ "$status" -eq 0 ] && [ -n "$target" ]; then
  mv "$target" "$target-opened" || exit 91
  mv "$DROVE_TEST_REPLACEMENT" "$target" || exit 92
  printf '%s' "$target" > "$DROVE_TEST_SWAPPED_PATH" || exit 93
fi
exit "$status"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_REPLACEMENT", replacementPath)
	t.Setenv("DROVE_TEST_SWAPPED_PATH", swappedPath)
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
	rawPath, err := os.ReadFile(swappedPath)
	if err != nil {
		t.Fatalf("read swapped target path: %v", err)
	}
	assertFileContents(
		t,
		filepath.Join(string(rawPath), "must-remain"),
		"replacement\n",
	)
	manager.git = realGit
	stagingPath := string(rawPath)
	registered, err := manager.worktreeRegistered(
		context.Background(),
		source,
		stagingPath,
	)
	if err != nil || !registered {
		t.Fatalf("original staging registration = %v, err=%v", registered, err)
	}
	if err := os.Rename(stagingPath, replacementPath); err != nil {
		t.Fatalf("restore replacement target: %v", err)
	}
	if err := os.Rename(stagingPath+"-opened", stagingPath); err != nil {
		t.Fatalf("restore original staging target: %v", err)
	}
	_, repository, err := manager.repositoryPaths(
		context.Background(),
		source,
	)
	if err != nil {
		t.Fatalf("resolve restored source repository: %v", err)
	}
	registered, err = manager.worktreeRegistered(
		context.Background(),
		repository,
		stagingPath,
	)
	if err != nil || !registered {
		t.Fatalf("restored staging registration = %v, err=%v", registered, err)
	}
	if _, err := os.Lstat(workspaceRecordPath(filepath.Join(
		manager.root,
		repositoryHash(repository),
		testAgentID,
	))); err != nil {
		t.Fatalf("preparation record was removed: %v", err)
	}
	runGit(t, stagingPath, "status", "--porcelain")
	assertFileContents(t, sentinel, "replacement\n")
}

func TestPrepareDoesNotWriteThroughReplacedStagingPath(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	initSourceSwapRepository(t, source)
	outside := filepath.Join(parent, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatalf("create outside target: %v", err)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	wrapper := filepath.Join(parent, "git-wrapper-replace-staging")
	script := `#!/bin/sh
command_root=
next_root=
target=
next_target=
for argument in "$@"; do
  if [ -n "$next_root" ]; then
    command_root=$argument
    next_root=
    continue
  fi
  if [ -n "$next_target" ]; then
    if [ -n "$command_root" ]; then
      target=$(CDPATH= cd -- "$command_root/$argument" && pwd -P) || exit 90
    else
      target=$(CDPATH= cd -- "$argument" && pwd -P) || exit 90
    fi
    next_target=
  fi
  [ "$argument" = "-C" ] && next_root=1
  [ "$argument" = "--no-checkout" ] && next_target=1
done
if [ -n "$target" ]; then
  mv "$target" "$target-opened" || exit 91
  ln -s "$DROVE_TEST_OUTSIDE" "$target" || exit 92
fi
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_OUTSIDE", outside)
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)

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
		t.Fatal("prepare accepted a replaced staging path")
	}
	if _, err := os.Lstat(filepath.Join(outside, ".git")); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("Git wrote through the replaced staging path: %v", err)
	}
}

func TestPrepareCheckoutAndPruneFailuresPreserveRecoveryRecord(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("BSD checkout uses a different implementation")
	}
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	initSourceSwapRepository(t, source)
	dataDir := filepath.Join(parent, "data")
	manager, err := New(dataDir)
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
	checkoutFailed := filepath.Join(parent, "checkout-failed")
	pruneFailed := filepath.Join(parent, "prune-failed")
	wrapper := filepath.Join(parent, "git-wrapper-checkout-prune-failure")
	script := `#!/bin/sh
case " $* " in
  *" read-tree "*)
    : > "$DROVE_TEST_CHECKOUT_FAILED"
    exit 91
    ;;
  *" worktree prune "*)
    : > "$DROVE_TEST_PRUNE_FAILED"
    exit 92
    ;;
esac
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_CHECKOUT_FAILED", checkoutFailed)
	t.Setenv("DROVE_TEST_PRUNE_FAILED", pruneFailed)
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	manager.git = wrapper

	if _, err := manager.Prepare(
		context.Background(),
		source,
		"",
		testAgentID,
	); err == nil {
		t.Fatal("prepare succeeded after checkout and prune failures")
	}
	if _, err := os.Lstat(checkoutFailed); err != nil {
		t.Fatalf("checkout failure was not reached: %v", err)
	}
	if _, err := os.Lstat(pruneFailed); err != nil {
		t.Fatalf("prune failure was not reached: %v", err)
	}
	record, exists, err := manager.readWorkspaceRecord(targetPath)
	if err != nil || !exists {
		t.Fatalf("recovery record: exists=%v err=%v", exists, err)
	}
	stagingName, err := preparedWorktreeStagingName(
		record.AgentID,
		record.BranchOperationID,
	)
	if err != nil {
		t.Fatalf("derive staging name: %v", err)
	}
	stagingPath := filepath.Join(filepath.Dir(targetPath), stagingName)
	registered, err := manager.worktreeRegistered(
		context.Background(),
		record.Repository,
		stagingPath,
	)
	if err != nil || !registered {
		t.Fatalf("staging registration = %v, err=%v", registered, err)
	}
	marker, err := manager.branchOwnershipMarkerExists(
		context.Background(),
		record.Repository,
		record.BranchOperationID,
	)
	if err != nil || !marker {
		t.Fatalf("branch ownership marker = %v, err=%v", marker, err)
	}

	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	if err := restarted.ReconcilePreparations(
		context.Background(),
		nil,
	); err != nil {
		t.Fatalf("reconcile prunable registration: %v", err)
	}
	if _, err := os.Lstat(workspaceRecordPath(targetPath)); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("reconciled preparation record remains: %v", err)
	}
	registered, err = restarted.worktreeRegistered(
		context.Background(),
		record.Repository,
		stagingPath,
	)
	if err != nil || registered {
		t.Fatalf("reconciled staging registration = %v, err=%v", registered, err)
	}
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

func TestPrepareRejectsCreatedBranchMovedBeforeWorktreeAdd(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	initSourceSwapRepository(t, source)
	startOID := strings.TrimSpace(runGit(t, source, "rev-parse", "HEAD"))
	if err := os.WriteFile(
		filepath.Join(source, "replacement.txt"),
		[]byte("replacement\n"),
		0o600,
	); err != nil {
		t.Fatalf("write replacement commit: %v", err)
	}
	runGit(t, source, "add", "replacement.txt")
	runGit(t, source, "commit", "-m", "replacement")
	replacementOID := strings.TrimSpace(runGit(t, source, "rev-parse", "HEAD"))
	runGit(t, source, "reset", "--hard", startOID)

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	wrapper := filepath.Join(parent, "git-wrapper-move-created-branch")
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
    "$DROVE_TEST_BRANCH_REF" "$DROVE_TEST_REPLACEMENT_OID" \
    "$DROVE_TEST_START_OID" || exit 96
fi
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	branch := "drove/" + testAgentID
	t.Setenv("DROVE_TEST_GIT_DIR", filepath.Join(source, ".git"))
	t.Setenv("DROVE_TEST_BRANCH_REF", "refs/heads/"+branch)
	t.Setenv("DROVE_TEST_REPLACEMENT_OID", replacementOID)
	t.Setenv("DROVE_TEST_START_OID", startOID)
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)

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
		t.Fatal("prepare accepted a moved created branch")
	}
	if got := strings.TrimSpace(runGit(
		t,
		source,
		"rev-parse",
		"refs/heads/"+branch,
	)); got != replacementOID {
		t.Fatalf("moved branch OID = %q, want %q", got, replacementOID)
	}
	if markers := strings.TrimSpace(runGit(
		t,
		source,
		"for-each-ref",
		"--format=%(refname)",
		branchOwnershipRefPrefix,
	)); markers != "" {
		t.Fatalf("branch ownership marker remains: %q", markers)
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

func TestReconcileUpgradedRemovalRejectsReplacementSourceRepository(
	t *testing.T,
) {
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
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge workspace: %v", err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists || record.RepositoryEvidence == nil {
		t.Fatalf("read workspace record: exists=%v err=%v", exists, err)
	}
	record.Version = repositoryWorkspaceRecordVersion
	record.RepositoryEvidence.GitDirectory = ""
	record.RepositoryEvidence.GitDirectoryIdentity = ""
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("write version 4 workspace record: %v", err)
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
	upgraded, exists, recordErr := manager.readWorkspaceRecord(prepared.Path)
	if recordErr != nil || !exists ||
		upgraded.Version != workspaceRecordVersion ||
		upgraded.RepositoryEvidence == nil {
		t.Fatalf(
			"upgraded removal record = %+v, exists=%v err=%v",
			upgraded,
			exists,
			recordErr,
		)
	}
	manager.git = realGit
	if err := os.Rename(source, openedSource); err != nil {
		t.Fatalf("move source repository: %v", err)
	}
	if err := os.Rename(replacement, source); err != nil {
		t.Fatalf("install replacement repository: %v", err)
	}

	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	if _, err := restarted.ReconcileRemovals(
		context.Background(),
	); err == nil {
		t.Fatal("upgraded removal accepted a replacement repository")
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
}

func TestRemoveLegacyRecordRejectsReplacementSourceBeforeUpgrade(
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
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge workspace: %v", err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists || record.RepositoryEvidence == nil {
		t.Fatalf("read workspace record: exists=%v err=%v", exists, err)
	}
	record.Version = repositoryWorkspaceRecordVersion
	record.RepositoryEvidence.GitDirectory = ""
	record.RepositoryEvidence.GitDirectoryIdentity = ""
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("write version 4 workspace record: %v", err)
	}
	if err := os.Rename(source, openedSource); err != nil {
		t.Fatalf("move source repository: %v", err)
	}
	if err := os.Rename(replacement, source); err != nil {
		t.Fatalf("install replacement repository: %v", err)
	}

	if _, err := manager.Remove(
		context.Background(),
		prepared.AgentID,
		true,
	); err == nil || !strings.Contains(
		err.Error(),
		"legacy removal repository identity evidence changed",
	) {
		t.Fatalf("remove replacement source error = %v", err)
	}
	current, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists ||
		current.Version != repositoryWorkspaceRecordVersion ||
		current.Removal != nil {
		t.Fatalf(
			"failed upgrade record = %+v, exists=%v err=%v",
			current,
			exists,
			err,
		)
	}
	if listing := runGit(
		t,
		openedSource,
		"worktree",
		"list",
		"--porcelain",
	); !strings.Contains(listing, prepared.Path) {
		t.Fatal("original repository registration was unexpectedly changed")
	}
}

func TestListRunsStatusFromOpenedWorktreeRoot(t *testing.T) {
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
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge workspace: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(prepared.Path, "tracked.txt"),
		[]byte("dirty\n"),
		0o600,
	); err != nil {
		t.Fatalf("dirty worktree: %v", err)
	}
	replacement := filepath.Join(parent, "replacement-worktree")
	if err := os.Mkdir(replacement, 0o700); err != nil {
		t.Fatalf("create replacement worktree: %v", err)
	}
	gitPointer, err := os.ReadFile(filepath.Join(prepared.Path, ".git"))
	if err != nil {
		t.Fatalf("read worktree Git pointer: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(replacement, ".git"),
		gitPointer,
		0o600,
	); err != nil {
		t.Fatalf("write replacement Git pointer: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(replacement, "tracked.txt"),
		[]byte("tracked\n"),
		0o600,
	); err != nil {
		t.Fatalf("write clean replacement: %v", err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	opened := prepared.Path + "-opened"
	swapped := filepath.Join(parent, "status-swapped")
	wrapper := filepath.Join(parent, "git-wrapper")
	script := `#!/bin/sh
matched=
for argument in "$@"; do
  [ "$argument" = "status" ] && matched=1
done
if [ -n "$matched" ] && [ ! -e "$DROVE_TEST_SWAPPED" ]; then
  mv "$DROVE_TEST_TARGET" "$DROVE_TEST_OPENED" || exit 91
  mv "$DROVE_TEST_REPLACEMENT" "$DROVE_TEST_TARGET" || exit 92
  "$DROVE_TEST_REAL_GIT" "$@"
  status=$?
  mv "$DROVE_TEST_TARGET" "$DROVE_TEST_REPLACEMENT" || exit 93
  mv "$DROVE_TEST_OPENED" "$DROVE_TEST_TARGET" || exit 94
  : > "$DROVE_TEST_SWAPPED"
  exit "$status"
fi
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_TARGET", prepared.Path)
	t.Setenv("DROVE_TEST_OPENED", opened)
	t.Setenv("DROVE_TEST_REPLACEMENT", replacement)
	t.Setenv("DROVE_TEST_SWAPPED", swapped)
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	manager.git = wrapper

	listed, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	if len(listed) != 1 || !listed[0].Dirty {
		t.Fatalf("listed workspaces = %+v, want dirty original", listed)
	}
	if _, err := os.Stat(swapped); err != nil {
		t.Fatalf("status swap hook was not invoked: %v", err)
	}
	manager.git = realGit
	result, err := manager.Remove(context.Background(), prepared.AgentID, true)
	if err != nil {
		t.Fatalf("remove workspace: %v", err)
	}
	if err := manager.AcknowledgeRemoval(result.Removal); err != nil {
		t.Fatalf("acknowledge removal: %v", err)
	}
}

func TestAcknowledgeRejectsReplacementCommonGitDirectory(
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

	if err := manager.AcknowledgePreparation(prepared); err == nil {
		t.Fatal("acknowledgement accepted a replacement common Git directory")
	}

	assertGitRefExists(
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
	if record.PreparationCommitted || record.BranchOperationID == "" {
		t.Fatalf("failed acknowledgement record = %+v", record)
	}

	displaced := filepath.Join(parent, "replacement-git")
	if err := os.Rename(replacementGitDirectory, displaced); err != nil {
		t.Fatalf("remove replacement common Git directory: %v", err)
	}
	if err := os.Rename(openedGitDirectory, replacementGitDirectory); err != nil {
		t.Fatalf("restore common Git directory: %v", err)
	}
	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("discard restored preparation: %v", err)
	}
}

func TestAcknowledgeRejectsReplacementSourceGitPointer(t *testing.T) {
	parent := t.TempDir()
	repository := filepath.Join(parent, "repository")
	source := filepath.Join(parent, "source")
	replacement := filepath.Join(parent, "replacement")
	initSourceSwapRepository(t, repository)
	runGit(t, repository, "worktree", "add", "-b", "source", source)
	runGit(t, repository, "worktree", "add", "-b", "replacement", replacement)

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
	sourcePointer := filepath.Join(source, ".git")
	originalPointer, err := os.ReadFile(sourcePointer)
	if err != nil {
		t.Fatalf("read source Git pointer: %v", err)
	}
	replacementPointer, err := os.ReadFile(filepath.Join(replacement, ".git"))
	if err != nil {
		t.Fatalf("read replacement Git pointer: %v", err)
	}
	if err := os.WriteFile(sourcePointer, replacementPointer, 0o600); err != nil {
		t.Fatalf("replace source Git pointer: %v", err)
	}

	if err := manager.AcknowledgePreparation(prepared); err == nil {
		t.Fatal("acknowledgement accepted a replacement source Git pointer")
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read failed acknowledgement: exists=%v err=%v", exists, err)
	}
	if record.PreparationCommitted || record.BranchOperationID == "" {
		t.Fatalf("failed acknowledgement record = %+v", record)
	}

	if err := os.WriteFile(sourcePointer, originalPointer, 0o600); err != nil {
		t.Fatalf("restore source Git pointer: %v", err)
	}
	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("discard restored preparation: %v", err)
	}
}

func TestPrepareDoesNotExposeCanonicalTargetToWorktreeAdd(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	dataDir := filepath.Join(parent, "data")
	outside := filepath.Join(parent, "outside")
	initSourceSwapRepository(t, source)
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatalf("create outside directory: %v", err)
	}
	canonicalSource, err := resolvePath(source)
	if err != nil {
		t.Fatalf("resolve source repository: %v", err)
	}
	targetPath := filepath.Join(
		dataDir,
		worktreeDirectory,
		repositoryHash(canonicalSource),
		testAgentID,
	)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	swapped := filepath.Join(parent, "swapped")
	wrapper := filepath.Join(parent, "git-wrapper")
	script := `#!/bin/sh
case " $* " in
  *" worktree add "*)
    if [ -d "$DROVE_TEST_TARGET" ]; then
      mv "$DROVE_TEST_TARGET" "$DROVE_TEST_TARGET-opened" || exit 91
      ln -s "$DROVE_TEST_OUTSIDE" "$DROVE_TEST_TARGET" || exit 92
      : > "$DROVE_TEST_SWAPPED"
    fi
    ;;
esac
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_TARGET", targetPath)
	t.Setenv("DROVE_TEST_OUTSIDE", outside)
	t.Setenv("DROVE_TEST_SWAPPED", swapped)
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	manager, err := New(dataDir)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	manager.git = wrapper

	prepared, err := manager.Prepare(
		context.Background(),
		source,
		"",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare workspace: %v", err)
	}
	if _, err := os.Lstat(swapped); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canonical target was exposed during worktree add: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, ".git")); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("Git wrote outside the managed bucket: %v", err)
	}
	manager.git = realGit
	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("discard prepared workspace: %v", err)
	}
}

func TestAcknowledgeRejectsTargetSwapDuringOwnershipCleanup(t *testing.T) {
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
	openedTarget := prepared.Path + "-opened"
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	swapped := filepath.Join(parent, "swapped")
	wrapper := filepath.Join(parent, "git-wrapper")
	script := `#!/bin/sh
case " $* " in
  *" update-ref -d refs/drove/preparations/"*)
    if [ ! -e "$DROVE_TEST_SWAPPED" ]; then
      mv "$DROVE_TEST_TARGET" "$DROVE_TEST_OPENED_TARGET" || exit 91
      mkdir "$DROVE_TEST_TARGET" || exit 92
      printf 'replacement\n' > "$DROVE_TEST_TARGET/must-remain" || exit 93
      : > "$DROVE_TEST_SWAPPED"
    fi
    ;;
esac
"$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_TARGET", prepared.Path)
	t.Setenv("DROVE_TEST_OPENED_TARGET", openedTarget)
	t.Setenv("DROVE_TEST_SWAPPED", swapped)
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	manager.git = wrapper

	err = manager.AcknowledgePreparation(prepared)
	if err == nil || !strings.Contains(
		err.Error(),
		"prepared worktree directory identity changed",
	) {
		t.Fatalf("acknowledgement target swap error = %v", err)
	}
	assertFileContents(
		t,
		filepath.Join(prepared.Path, "must-remain"),
		"replacement\n",
	)
	record, exists, recordErr := manager.readWorkspaceRecord(prepared.Path)
	if recordErr != nil || !exists ||
		!record.PreparationCommitted ||
		record.BranchOperationID == "" {
		t.Fatalf(
			"failed acknowledgement record = %+v, exists=%v err=%v",
			record,
			exists,
			recordErr,
		)
	}

	if err := os.RemoveAll(prepared.Path); err != nil {
		t.Fatalf("remove replacement target: %v", err)
	}
	if err := os.Rename(openedTarget, prepared.Path); err != nil {
		t.Fatalf("restore prepared target: %v", err)
	}
	manager.git = realGit
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("retry acknowledgement: %v", err)
	}
	if _, err := removeWorkspace(
		t,
		manager,
		context.Background(),
		prepared.AgentID,
		true,
	); err != nil {
		t.Fatalf("remove acknowledged workspace: %v", err)
	}
}

func TestAcknowledgeQueriesSourceGitDirectoriesTogether(t *testing.T) {
	parent := t.TempDir()
	repository := filepath.Join(parent, "repository")
	source := filepath.Join(parent, "source")
	replacement := filepath.Join(parent, "replacement")
	initSourceSwapRepository(t, repository)
	runGit(t, repository, "worktree", "add", "-b", "source", source)
	runGit(t, repository, "worktree", "add", "-b", "replacement", replacement)

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
	sourcePointer := filepath.Join(source, ".git")
	originalPointer, err := os.ReadFile(sourcePointer)
	if err != nil {
		t.Fatalf("read source Git pointer: %v", err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	counter := filepath.Join(parent, "separate-directory-queries")
	swapped := filepath.Join(parent, "swapped")
	wrapper := filepath.Join(parent, "git-wrapper")
	script := `#!/bin/sh
has_git=
has_common=
for argument in "$@"; do
  case "$argument" in
    --absolute-git-dir) has_git=1 ;;
    --git-common-dir) has_common=1 ;;
  esac
done
matched=
if [ -n "$has_git" ] && [ -z "$has_common" ] &&
   [ "$GIT_WORK_TREE" = "$DROVE_TEST_SOURCE" ]; then
  count=0
  if [ -f "$DROVE_TEST_COUNTER" ]; then
    count=$(cat "$DROVE_TEST_COUNTER") || exit 90
  fi
  count=$((count + 1))
  printf '%s\n' "$count" > "$DROVE_TEST_COUNTER" || exit 91
  if [ "$count" -eq 5 ]; then
    matched=1
  fi
fi
"$DROVE_TEST_REAL_GIT" "$@"
status=$?
if [ "$status" -eq 0 ] && [ -n "$matched" ]; then
  cp "$DROVE_TEST_REPLACEMENT_POINTER" "$DROVE_TEST_SOURCE_POINTER" || exit 92
  : > "$DROVE_TEST_SWAPPED"
fi
exit "$status"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_COUNTER", counter)
	t.Setenv(
		"DROVE_TEST_REPLACEMENT_POINTER",
		filepath.Join(replacement, ".git"),
	)
	t.Setenv("DROVE_TEST_SOURCE_POINTER", sourcePointer)
	t.Setenv("DROVE_TEST_SOURCE", source)
	t.Setenv("DROVE_TEST_SWAPPED", swapped)
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	manager.git = wrapper

	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge preparation: %v", err)
	}
	if _, err := os.Stat(swapped); !os.IsNotExist(err) {
		t.Fatalf("separate Git directory query window remained: %v", err)
	}
	currentPointer, err := os.ReadFile(sourcePointer)
	if err != nil {
		t.Fatalf("read acknowledged source Git pointer: %v", err)
	}
	if string(currentPointer) != string(originalPointer) {
		t.Fatal("source Git pointer changed between directory queries")
	}
}

func TestAcknowledgeRejectsSourceGitPointerSwapDuringOwnershipCleanup(
	t *testing.T,
) {
	for _, phase := range []string{"before", "after"} {
		t.Run(phase, func(t *testing.T) {
			parent := t.TempDir()
			repository := filepath.Join(parent, "repository")
			source := filepath.Join(parent, "source")
			replacement := filepath.Join(parent, "replacement")
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
			sourcePointer := filepath.Join(source, ".git")
			originalPointer, err := os.ReadFile(sourcePointer)
			if err != nil {
				t.Fatalf("read source Git pointer: %v", err)
			}
			realGit, err := exec.LookPath("git")
			if err != nil {
				t.Fatalf("find Git: %v", err)
			}
			swapped := filepath.Join(parent, "swapped")
			wrapper := filepath.Join(parent, "git-wrapper")
			script := `#!/bin/sh
matched=
case " $* " in
  *" update-ref -d refs/drove/preparations/"*) matched=1 ;;
esac
swap_source_pointer() {
  cp "$DROVE_TEST_REPLACEMENT_POINTER" "$DROVE_TEST_SOURCE_POINTER" || exit 91
  : > "$DROVE_TEST_SWAPPED"
}
if [ -n "$matched" ] && [ "$DROVE_TEST_SWAP_PHASE" = "before" ] &&
   [ ! -e "$DROVE_TEST_SWAPPED" ]; then
  swap_source_pointer
fi
"$DROVE_TEST_REAL_GIT" "$@"
status=$?
if [ "$status" -eq 0 ] && [ -n "$matched" ] &&
   [ "$DROVE_TEST_SWAP_PHASE" = "after" ] &&
   [ ! -e "$DROVE_TEST_SWAPPED" ]; then
  swap_source_pointer
fi
exit "$status"
`
			if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
				t.Fatalf("write Git wrapper: %v", err)
			}
			t.Setenv("DROVE_TEST_SOURCE_POINTER", sourcePointer)
			t.Setenv(
				"DROVE_TEST_REPLACEMENT_POINTER",
				filepath.Join(replacement, ".git"),
			)
			t.Setenv("DROVE_TEST_SWAPPED", swapped)
			t.Setenv("DROVE_TEST_SWAP_PHASE", phase)
			t.Setenv("DROVE_TEST_REAL_GIT", realGit)
			manager.git = wrapper

			err = manager.AcknowledgePreparation(prepared)
			if err == nil || !strings.Contains(
				err.Error(),
				"verify repository after ownership cleanup",
			) {
				t.Fatalf("acknowledgement replacement error = %v", err)
			}
			record, exists, err := manager.readWorkspaceRecord(prepared.Path)
			if err != nil || !exists {
				t.Fatalf(
					"read failed acknowledgement: exists=%v err=%v",
					exists,
					err,
				)
			}
			if !record.PreparationCommitted ||
				record.BranchOperationID != prepared.branchOperationID {
				t.Fatalf("retry state = %+v", record)
			}

			if err := os.WriteFile(
				sourcePointer,
				originalPointer,
				0o600,
			); err != nil {
				t.Fatalf("restore source Git pointer: %v", err)
			}
			manager.git = realGit
			if err := manager.AcknowledgePreparation(prepared); err != nil {
				t.Fatalf("retry acknowledgement: %v", err)
			}
			record, exists, err = manager.readWorkspaceRecord(prepared.Path)
			if err != nil || !exists {
				t.Fatalf(
					"read retried acknowledgement: exists=%v err=%v",
					exists,
					err,
				)
			}
			if !record.PreparationCommitted || record.BranchOperationID != "" {
				t.Fatalf("retried acknowledgement state = %+v", record)
			}
		})
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

func TestIncludedPathsRejectsReplacedCommonGitDirectory(
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

	_, err = manager.includedPaths(
		context.Background(),
		source,
		sourceRoot,
		lease.repository,
	)
	if err == nil || !strings.Contains(err.Error(), "changed while in use") {
		t.Fatalf("replaced common Git directory error = %v", err)
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

func TestPrepareDoesNotCheckoutThroughReplacedWorktreeGitPointer(
	t *testing.T,
) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	initSourceSwapRepository(t, source)
	replacement := filepath.Join(parent, "replacement-worktree")
	runGit(
		t,
		source,
		"worktree",
		"add",
		"-b",
		"replacement-worktree",
		replacement,
	)

	sentinel := filepath.Join(parent, "filter-ran")
	filter := filepath.Join(parent, "filter")
	script := "#!/bin/sh\nprintf ran > \"$DROVE_TEST_SENTINEL\"\ncat\n"
	if err := os.WriteFile(filter, []byte(script), 0o700); err != nil {
		t.Fatalf("write filter: %v", err)
	}
	runGit(t, source, "config", "filter.review.smudge", filter)
	runGit(t, source, "config", "filter.review.clean", "cat")
	runGit(t, source, "config", "filter.review.required", "true")
	if err := os.WriteFile(
		filepath.Join(replacement, ".gitattributes"),
		[]byte("replacement.txt filter=review\n"),
		0o600,
	); err != nil {
		t.Fatalf("write replacement attributes: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(replacement, "replacement.txt"),
		[]byte("replacement\n"),
		0o600,
	); err != nil {
		t.Fatalf("write replacement file: %v", err)
	}
	runGit(t, replacement, "add", ".gitattributes", "replacement.txt")
	runGit(t, replacement, "commit", "-m", "replacement worktree")

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	wrapper := filepath.Join(parent, "git-wrapper-swap-worktree-pointer")
	wrapperScript := `#!/bin/sh
target=
next_target=
for argument in "$@"; do
  if [ -n "$next_target" ]; then
    target=$argument
    next_target=
  fi
  [ "$argument" = "--no-checkout" ] && next_target=1
done
"$DROVE_TEST_REAL_GIT" "$@"
status=$?
if [ "$status" -eq 0 ] && [ -n "$target" ]; then
  mv "$target/.git" "$target/.git-original" || exit 91
  cp "$DROVE_TEST_REPLACEMENT/.git" "$target/.git" || exit 92
fi
exit "$status"
`
	if err := os.WriteFile(wrapper, []byte(wrapperScript), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	t.Setenv("DROVE_TEST_REPLACEMENT", replacement)
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
		t.Fatal("prepare accepted a replaced worktree Git pointer")
	}
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement worktree checkout filter ran: %v", err)
	}
}

func TestPrepareIgnoresTransientPrivateGitReplacementDuringCheckout(
	t *testing.T,
) {
	switch runtime.GOOS {
	case "darwin", "dragonfly", "freebsd", "netbsd", "openbsd":
	default:
		t.Skip("test covers descriptor-rooted BSD checkout")
	}
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	initSourceSwapRepository(t, source)
	if err := os.WriteFile(
		filepath.Join(source, ".gitattributes"),
		[]byte("tracked.txt filter=review\n"),
		0o600,
	); err != nil {
		t.Fatalf("write attributes: %v", err)
	}
	runGit(t, source, "add", ".gitattributes")
	runGit(t, source, "commit", "-m", "add checkout filter attributes")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	sentinel := filepath.Join(parent, "replacement-filter-ran")
	filter := filepath.Join(parent, "replacement-filter")
	filterScript := "#!/bin/sh\nprintf ran > \"$DROVE_TEST_SENTINEL\"\ncat\n"
	if err := os.WriteFile(filter, []byte(filterScript), 0o700); err != nil {
		t.Fatalf("write replacement filter: %v", err)
	}
	maliciousCommon := filepath.Join(parent, "malicious-common")
	if err := os.CopyFS(maliciousCommon, os.DirFS(filepath.Join(source, ".git"))); err != nil {
		t.Fatalf("copy malicious common Git directory: %v", err)
	}
	for _, arguments := range [][]string{
		{"--git-dir=" + maliciousCommon, "config", "filter.review.smudge", filter},
		{"--git-dir=" + maliciousCommon, "config", "filter.review.clean", "cat"},
		{"--git-dir=" + maliciousCommon, "config", "filter.review.required", "true"},
	} {
		command := exec.Command(realGit, arguments...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf(
				"configure malicious common Git directory: %v\n%s",
				err,
				output,
			)
		}
	}
	marker := filepath.Join(parent, "swapped-private-git")
	gitPath := filepath.Join(parent, "prepared-private-git-path")
	wrapper := filepath.Join(parent, "git-wrapper-transient-private-git")
	script := `#!/bin/sh
worktree=
add=
no_checkout=
checkout=
target=
next_target=
for argument in "$@"; do
  if [ -n "$next_target" ]; then
    target=$argument
    next_target=
  fi
  [ "$argument" = "worktree" ] && worktree=1
  [ "$argument" = "add" ] && add=1
  if [ "$argument" = "--no-checkout" ]; then
    no_checkout=1
    next_target=1
  fi
  [ "$argument" = "read-tree" ] && checkout=1
done
if [ -n "$worktree" ] && [ -n "$add" ] && [ -n "$no_checkout" ]; then
  "$DROVE_TEST_REAL_GIT" "$@"
  status=$?
  if [ "$status" -eq 0 ]; then
    sed 's/^gitdir: //' "$target/.git" > "$DROVE_TEST_GIT_PATH" || exit 90
  fi
  exit "$status"
fi
if [ -n "$checkout" ] && [ -s "$DROVE_TEST_GIT_PATH" ] && [ ! -e "$DROVE_TEST_MARKER" ]; then
  public=$(cat "$DROVE_TEST_GIT_PATH") || exit 91
  original="$public.drove-original"
  mv "$public" "$original" || exit 92
  cp -R "$original" "$public" || exit 93
  printf '%s\n' "$DROVE_TEST_MALICIOUS_COMMON" > "$public/commondir" || exit 94
  "$DROVE_TEST_REAL_GIT" "$@"
  status=$?
  if [ "$GIT_DIR" != "." ] && [ -f "$public/index" ]; then
    cp "$public/index" "$original/index" || exit 95
  fi
  rm -rf "$public" || exit 96
  mv "$original" "$public" || exit 97
  : > "$DROVE_TEST_MARKER"
  exit "$status"
fi
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_MARKER", marker)
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	t.Setenv("DROVE_TEST_GIT_PATH", gitPath)
	t.Setenv("DROVE_TEST_MALICIOUS_COMMON", maliciousCommon)
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
	); err == nil || !strings.Contains(
		err.Error(),
		"working-tree filter",
	) {
		t.Fatalf(
			"prepare through transient private Git replacement error = %v",
			err,
		)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("transient private Git replacement did not run: %v", err)
	}
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement checkout filter ran: %v", err)
	}
}

func TestPreparationHeadUsesPinnedWorktreeRegistration(t *testing.T) {
	parent := t.TempDir()
	repository := filepath.Join(parent, "repository")
	source := filepath.Join(parent, "source")
	initSourceSwapRepository(t, repository)
	runGit(t, repository, "worktree", "add", "-b", "source-branch", source)
	sourceOID := strings.TrimSpace(runGit(t, source, "rev-parse", "HEAD"))
	if err := os.WriteFile(
		filepath.Join(repository, "second.txt"),
		[]byte("second\n"),
		0o600,
	); err != nil {
		t.Fatalf("write second commit file: %v", err)
	}
	runGit(t, repository, "add", "second.txt")
	runGit(t, repository, "commit", "-m", "second")
	replacementOID := strings.TrimSpace(runGit(t, repository, "rev-parse", "HEAD"))
	if replacementOID == sourceOID {
		t.Fatal("source and replacement OIDs unexpectedly match")
	}

	maliciousCommon := filepath.Join(parent, "malicious-common")
	if err := os.CopyFS(
		maliciousCommon,
		os.DirFS(filepath.Join(repository, ".git")),
	); err != nil {
		t.Fatalf("copy malicious common Git directory: %v", err)
	}
	runGit(
		t,
		parent,
		"--git-dir="+maliciousCommon,
		"update-ref",
		"refs/heads/source-branch",
		replacementOID,
	)

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	marker := filepath.Join(parent, "head-commondir-swapped")
	wrapper := filepath.Join(parent, "git-wrapper-head-commondir")
	script := `#!/bin/sh
matched=
has_rev_parse=
has_verify=
has_head=
for argument in "$@"; do
  [ "$argument" = "rev-parse" ] && has_rev_parse=1
  [ "$argument" = "--verify" ] && has_verify=1
  [ "$argument" = "HEAD" ] && has_head=1
done
if [ -n "$has_rev_parse" ] && [ -n "$has_verify" ] && [ -n "$has_head" ] &&
   [ "$GIT_WORK_TREE" = "$DROVE_TEST_SOURCE" ] &&
   [ ! -e "$DROVE_TEST_MARKER" ]; then
  private=$("$DROVE_TEST_REAL_GIT" rev-parse --absolute-git-dir) || exit 90
  original=$(cat "$private/commondir") || exit 91
  printf '%s\n' "$DROVE_TEST_MALICIOUS_COMMON" > "$private/commondir" || exit 92
  "$DROVE_TEST_REAL_GIT" "$@"
  status=$?
  printf '%s\n' "$original" > "$private/commondir" || exit 93
  : > "$DROVE_TEST_MARKER"
  exit "$status"
fi
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_SOURCE", source)
	t.Setenv("DROVE_TEST_MARKER", marker)
	t.Setenv("DROVE_TEST_MALICIOUS_COMMON", maliciousCommon)
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)

	manager, err := New(filepath.Join(parent, "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	manager.git = wrapper
	root, err := openRealPathRoot(source)
	if err != nil {
		t.Fatalf("open source worktree: %v", err)
	}
	lease, err := newPreparationLease(
		context.Background(),
		manager,
		source,
		root,
	)
	if err != nil {
		t.Fatalf("open preparation lease: %v", err)
	}
	if lease.repository.startOID != sourceOID {
		t.Fatalf(
			"preparation start OID = %q, want registered %q",
			lease.repository.startOID,
			sourceOID,
		)
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("close preparation lease: %v", err)
	}
}

func TestPrepareDoesNotUsePublicWorktreeRepair(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux promotion rewrites the bound private pointer directly")
	}
	parent := t.TempDir()
	repository := filepath.Join(parent, "repository")
	sibling := filepath.Join(parent, "sibling")
	initSourceSwapRepository(t, repository)
	runGit(t, repository, "worktree", "add", "-b", "sibling", sibling)
	sibling, err := resolvePath(sibling)
	if err != nil {
		t.Fatalf("resolve sibling worktree: %v", err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	repairResult := filepath.Join(parent, "worktree-repair-result")
	wrapper := filepath.Join(parent, "git-wrapper-reject-worktree-repair")
	script := `#!/bin/sh
saw_worktree=
for argument in "$@"; do
  if [ "$saw_worktree" = "1" ] && [ "$argument" = "repair" ]; then
    printf invoked > "$DROVE_TEST_REPAIR_RESULT"
    exit 94
  fi
  [ "$argument" = "worktree" ] && saw_worktree=1
done
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_REPAIR_RESULT", repairResult)
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)

	manager, err := New(filepath.Join(parent, "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	manager.git = wrapper
	prepared, err := manager.Prepare(
		context.Background(),
		repository,
		"",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare without public worktree repair: %v", err)
	}
	if _, err := os.Lstat(repairResult); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree repair was invoked: %v", err)
	}
	assertGitWorktreeRegistration(
		t,
		filepath.Join(repository, ".git"),
		prepared.Path,
		true,
	)
	registered, err := manager.worktreeRegistered(
		context.Background(),
		repository,
		sibling,
	)
	if err != nil || !registered {
		t.Fatalf(
			"sibling registration restored = %v, err=%v\n%s",
			registered,
			err,
			runGit(t, repository, "worktree", "list", "--porcelain"),
		)
	}
	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("discard prepared worktree: %v", err)
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
