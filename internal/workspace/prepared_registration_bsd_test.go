//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBSDPrepareDoesNotWriteThroughReplacedCommonGitPath(t *testing.T) {
	parent := t.TempDir()
	repository := filepath.Join(parent, "repository")
	initSourceSwapRepository(t, repository)
	commonPath := filepath.Join(repository, ".git")
	openedCommon := commonPath + "-opened"
	replacement := filepath.Join(parent, "replacement-common")
	if err := os.Mkdir(replacement, 0o700); err != nil {
		t.Fatalf("create replacement common directory: %v", err)
	}
	sentinel := filepath.Join(replacement, "must-remain")
	if err := os.WriteFile(sentinel, []byte("replacement\n"), 0o600); err != nil {
		t.Fatalf("write replacement sentinel: %v", err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	swapped := filepath.Join(parent, "common-swapped")
	stageFile := filepath.Join(parent, "common-stage")
	wrapper := filepath.Join(parent, "git-wrapper-common-swap")
	script := `#!/bin/sh
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
if [ "$status" -eq 0 ] && [ -n "$target" ] &&
   [ ! -e "$DROVE_TEST_SWAPPED" ]; then
  printf '%s/%s' "$PWD" "$target" > "$DROVE_TEST_STAGE" || exit 90
  mv "$DROVE_TEST_COMMON" "$DROVE_TEST_OPENED_COMMON" || exit 91
  mv "$DROVE_TEST_REPLACEMENT" "$DROVE_TEST_COMMON" || exit 92
  : > "$DROVE_TEST_SWAPPED"
fi
exit "$status"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	t.Setenv("DROVE_TEST_COMMON", commonPath)
	t.Setenv("DROVE_TEST_OPENED_COMMON", openedCommon)
	t.Setenv("DROVE_TEST_REPLACEMENT", replacement)
	t.Setenv("DROVE_TEST_SWAPPED", swapped)
	t.Setenv("DROVE_TEST_STAGE", stageFile)

	dataDir := filepath.Join(parent, "data")
	manager, err := New(dataDir)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	manager.git = wrapper
	if _, err := manager.Prepare(
		context.Background(),
		repository,
		"",
		testAgentID,
	); err == nil {
		t.Fatal("prepare accepted a replaced common Git path")
	}
	if _, err := os.Stat(swapped); err != nil {
		t.Fatalf("worktree add did not reach the swap: %v", err)
	}
	assertFileContents(
		t,
		filepath.Join(commonPath, "must-remain"),
		"replacement\n",
	)
	if _, err := os.Lstat(filepath.Join(commonPath, "worktrees")); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("replacement common directory received Git writes: %v", err)
	}

	if err := os.Rename(commonPath, replacement); err != nil {
		t.Fatalf("restore replacement directory: %v", err)
	}
	if err := os.Rename(openedCommon, commonPath); err != nil {
		t.Fatalf("restore common Git directory: %v", err)
	}
	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	if err := restarted.ReconcilePreparations(
		context.Background(),
		nil,
	); err != nil {
		t.Fatalf("reconcile failed preparation: %v", err)
	}
	rawStage, err := os.ReadFile(stageFile)
	if err != nil {
		t.Fatalf("read internal stage path: %v", err)
	}
	stagePath := filepath.Clean(string(rawStage))
	if _, err := os.Lstat(stagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("internal common-root stage remains: %v", err)
	}
	if _, err := os.Lstat(
		filepath.Join(commonPath, "worktrees", filepath.Base(stagePath)),
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("internal private Git directory remains: %v", err)
	}
	assertFileContents(t, sentinel, "replacement\n")
}

func TestBSDPrepareDoesNotInvokePublicWorktreeRepair(t *testing.T) {
	parent := t.TempDir()
	repository := filepath.Join(parent, "repository")
	initSourceSwapRepository(t, repository)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	repairInvoked := filepath.Join(parent, "repair-invoked")
	wrapper := filepath.Join(parent, "git-wrapper-reject-repair")
	script := `#!/bin/sh
case " $* " in
  *" worktree repair "*)
    : > "$DROVE_TEST_REPAIR_INVOKED"
    exit 91
    ;;
esac
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	t.Setenv("DROVE_TEST_REPAIR_INVOKED", repairInvoked)
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
		t.Fatalf("prepare worktree: %v", err)
	}
	if _, err := os.Lstat(repairInvoked); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("public worktree repair was invoked: %v", err)
	}
	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("discard prepared worktree: %v", err)
	}
}

func TestEnsureBoundGitPointerRecoversTemporary(t *testing.T) {
	path := t.TempDir()
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatalf("open pointer root: %v", err)
	}
	defer root.Close()
	const (
		target  = "gitdir"
		old     = "old-target/.git\n"
		desired = "new-target/.git\n"
	)
	if err := os.WriteFile(filepath.Join(path, target), []byte(old), 0o600); err != nil {
		t.Fatalf("write old pointer: %v", err)
	}
	prefix := boundGitPointerTempPrefix("stage", target)
	tempName := prefix + "new-" +
		"78787878-7878-4787-8787-787878787878"
	if err := os.WriteFile(
		filepath.Join(path, tempName),
		[]byte(desired),
		0o600,
	); err != nil {
		t.Fatalf("write pointer temporary: %v", err)
	}

	if err := ensureBoundGitPointer(
		root,
		target,
		prefix,
		desired,
		old,
	); err != nil {
		t.Fatalf("recover pointer temporary: %v", err)
	}
	if err := ensureBoundGitPointer(
		root,
		target,
		prefix,
		desired,
		old,
	); err != nil {
		t.Fatalf("repeat pointer recovery: %v", err)
	}
	assertFileContents(t, filepath.Join(path, target), desired)
	if _, err := os.Lstat(filepath.Join(path, tempName)); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("pointer temporary remains: %v", err)
	}
}

func TestIsolateBoundGitPointerPreservesReplacement(t *testing.T) {
	path := t.TempDir()
	targetPath := filepath.Join(path, "gitdir")
	originalPath := filepath.Join(path, "gitdir-original")
	if err := os.WriteFile(
		targetPath,
		[]byte("original/.git\n"),
		0o600,
	); err != nil {
		t.Fatalf("write pointer: %v", err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatalf("open pointer root: %v", err)
	}
	defer root.Close()

	err = isolateBoundGitPointer(
		root,
		"gitdir",
		".drove-pointer-old-78787878-7878-4787-8787-787878787878",
		"original/.git\n",
		func() {
			if err := os.Rename(targetPath, originalPath); err != nil {
				t.Fatalf("move validated pointer: %v", err)
			}
			if err := os.WriteFile(
				targetPath,
				[]byte("replacement/.git\n"),
				0o600,
			); err != nil {
				t.Fatalf("write replacement pointer: %v", err)
			}
		},
	)
	if err == nil {
		t.Fatal("pointer isolation accepted a replacement")
	}
	assertFileContents(t, targetPath, "replacement/.git\n")
	assertFileContents(t, originalPath, "original/.git\n")
}

func TestValidateBSDPreparedWorktreeNamesRejectsAdminCollision(t *testing.T) {
	path := t.TempDir()
	if err := os.Mkdir(filepath.Join(path, "worktrees"), 0o700); err != nil {
		t.Fatalf("create worktrees directory: %v", err)
	}
	const stageName = "11111111-1111-4111-8111-111111111111123"
	if err := os.Mkdir(
		filepath.Join(path, "worktrees", stageName),
		0o700,
	); err != nil {
		t.Fatalf("create private Git collision: %v", err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatalf("open common root: %v", err)
	}
	defer root.Close()

	if err := validateBSDPreparedWorktreeNamesAvailable(
		root,
		stageName,
	); err == nil {
		t.Fatal("validation accepted a deterministic admin collision")
	}
}
