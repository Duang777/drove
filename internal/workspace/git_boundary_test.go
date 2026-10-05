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

func TestPrepareClearsInheritedGitEnvironment(t *testing.T) {
	repository := newTestRepository(t)
	replacement := newTestRepository(t)
	if err := os.WriteFile(
		filepath.Join(replacement, "tracked.txt"),
		[]byte("replacement\n"),
		0o600,
	); err != nil {
		t.Fatalf("write replacement file: %v", err)
	}
	runGit(t, replacement, "add", "tracked.txt")
	runGit(t, replacement, "commit", "-m", "replacement")

	t.Setenv("GIT_DIR", filepath.Join(replacement, ".git"))
	t.Setenv("GIT_WORK_TREE", replacement)
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
		t.Fatalf("prepare with inherited Git environment: %v", err)
	}
	if err := os.Unsetenv("GIT_DIR"); err != nil {
		t.Fatalf("unset GIT_DIR: %v", err)
	}
	if err := os.Unsetenv("GIT_WORK_TREE"); err != nil {
		t.Fatalf("unset GIT_WORK_TREE: %v", err)
	}
	assertFileContents(
		t,
		filepath.Join(prepared.Path, "tracked.txt"),
		"tracked\n",
	)
	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("discard prepared workspace: %v", err)
	}
}

func TestPrepareRejectsNULBranchAsInvalid(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	_, err = manager.Prepare(
		context.Background(),
		newTestRepository(t),
		"invalid\x00branch",
		testAgentID,
	)
	if !errors.Is(err, ErrInvalidBranch) {
		t.Fatalf("prepare NUL branch error = %v, want ErrInvalidBranch", err)
	}
}

func TestPreparePinsLinkedWorktreeHEAD(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test requires a POSIX shell")
	}
	repository := newTestRepository(t)
	sourceOID := strings.TrimSpace(runGit(t, repository, "rev-parse", "HEAD"))
	source := filepath.Join(t.TempDir(), "source")
	runGit(
		t,
		repository,
		"worktree",
		"add",
		"-b",
		"source-branch",
		source,
		sourceOID,
	)
	if err := os.WriteFile(
		filepath.Join(repository, "second.txt"),
		[]byte("second\n"),
		0o600,
	); err != nil {
		t.Fatalf("write second commit: %v", err)
	}
	runGit(t, repository, "add", "second.txt")
	runGit(t, repository, "commit", "-m", "second")
	replacementOID := strings.TrimSpace(
		runGit(t, repository, "rev-parse", "HEAD"),
	)
	sibling := filepath.Join(t.TempDir(), "sibling")
	runGit(
		t,
		repository,
		"worktree",
		"add",
		"-b",
		"sibling-branch",
		sibling,
		"main",
	)

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := `#!/bin/sh
matched=
previous=
for argument in "$@"; do
  if [ "$previous" = "rev-parse" ] && [ "$argument" = "--verify" ]; then
    matched=1
  fi
  previous=$argument
done
case " $* " in
  *" rev-parse --verify HEAD "*) matched=1 ;;
esac
if [ -n "$matched" ]; then
  mv "$DROVE_TEST_SOURCE/.git" "$DROVE_TEST_SOURCE/.git.original" || exit 91
  cp "$DROVE_TEST_SIBLING/.git" "$DROVE_TEST_SOURCE/.git" || exit 92
fi
"$DROVE_TEST_REAL_GIT" "$@"
status=$?
if [ -n "$matched" ]; then
  rm -f "$DROVE_TEST_SOURCE/.git"
  mv "$DROVE_TEST_SOURCE/.git.original" "$DROVE_TEST_SOURCE/.git" || exit 93
fi
exit "$status"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	t.Setenv("DROVE_TEST_SOURCE", source)
	t.Setenv("DROVE_TEST_SIBLING", sibling)

	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	manager.git = wrapper
	prepared, err := manager.Prepare(
		context.Background(),
		source,
		"prepared-from-source",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare linked worktree: %v", err)
	}
	gotOID := strings.TrimSpace(
		runGit(t, prepared.Path, "rev-parse", "HEAD"),
	)
	if gotOID != sourceOID {
		t.Fatalf(
			"prepared HEAD = %q, want source %q, replacement was %q",
			gotOID,
			sourceOID,
			replacementOID,
		)
	}
	manager.git = realGit
	if err := manager.Discard(context.Background(), prepared); err != nil {
		t.Fatalf("discard prepared workspace: %v", err)
	}
}

func TestIncludeEnumerationUsesPinnedSourceRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test requires a POSIX shell")
	}
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	initTestRepositoryAt(t, source)
	if err := os.WriteFile(
		filepath.Join(source, ".gitignore"),
		[]byte(".env\n"),
		0o600,
	); err != nil {
		t.Fatalf("write gitignore: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(source, worktreeIncludeFile),
		[]byte(".env\n"),
		0o600,
	); err != nil {
		t.Fatalf("write include manifest: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(source, ".env"),
		[]byte("source\n"),
		0o600,
	); err != nil {
		t.Fatalf("write included file: %v", err)
	}
	replacement := filepath.Join(parent, "replacement")
	if err := os.Mkdir(replacement, 0o700); err != nil {
		t.Fatalf("create replacement: %v", err)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	wrapper := filepath.Join(parent, "git-wrapper")
	script := `#!/bin/sh
case " $* " in
  *" ls-files "*)
    mv "$DROVE_TEST_SOURCE" "$DROVE_TEST_OPENED" || exit 91
    mv "$DROVE_TEST_REPLACEMENT" "$DROVE_TEST_SOURCE" || exit 92
    "$DROVE_TEST_REAL_GIT" "$@"
    status=$?
    mv "$DROVE_TEST_SOURCE" "$DROVE_TEST_REPLACEMENT" || exit 93
    mv "$DROVE_TEST_OPENED" "$DROVE_TEST_SOURCE" || exit 94
    exit "$status"
    ;;
esac
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	t.Setenv("DROVE_TEST_SOURCE", source)
	t.Setenv("DROVE_TEST_OPENED", source+"-opened")
	t.Setenv("DROVE_TEST_REPLACEMENT", replacement)

	manager, err := New(filepath.Join(parent, "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	manager.git = wrapper
	canonicalSource, err := resolvePath(source)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	targetPath := filepath.Join(
		manager.root,
		repositoryHash(canonicalSource),
		testAgentID,
	)
	_, err = manager.Prepare(
		context.Background(),
		source,
		"",
		testAgentID,
	)
	if err == nil {
		t.Fatal("prepare succeeded while source path was replaced")
	}
	if _, err := os.Lstat(targetPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed preparation path remains or inspect failed: %v", err)
	}
	if _, err := os.Lstat(
		workspaceRecordPath(targetPath),
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed preparation record remains or inspect failed: %v", err)
	}
}

func TestPrepareRejectsIncludeSelectionThatBecomesTracked(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test requires a POSIX shell")
	}
	source := newTestRepository(t)
	if err := os.WriteFile(
		filepath.Join(source, ".gitignore"),
		[]byte(".env\n"),
		0o600,
	); err != nil {
		t.Fatalf("write gitignore: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(source, worktreeIncludeFile),
		[]byte(".env\n"),
		0o600,
	); err != nil {
		t.Fatalf("write include manifest: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(source, ".env"),
		[]byte("local\n"),
		0o600,
	); err != nil {
		t.Fatalf("write include candidate: %v", err)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	marker := filepath.Join(t.TempDir(), "tracked")
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := `#!/bin/sh
case " $* " in
  *" worktree add "*)
    if [ ! -e "$DROVE_TEST_MARKER" ]; then
      env -u GIT_DIR -u GIT_COMMON_DIR -u GIT_WORK_TREE -u GIT_INDEX_FILE \
        "$DROVE_TEST_REAL_GIT" -C "$DROVE_TEST_SOURCE" add -f .env || exit 91
      env -u GIT_DIR -u GIT_COMMON_DIR -u GIT_WORK_TREE -u GIT_INDEX_FILE \
        "$DROVE_TEST_REAL_GIT" -C "$DROVE_TEST_SOURCE" commit -m track-include ||
        exit 92
      : > "$DROVE_TEST_MARKER"
    fi
    ;;
esac
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	t.Setenv("DROVE_TEST_SOURCE", source)
	t.Setenv("DROVE_TEST_MARKER", marker)

	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	manager.git = wrapper
	canonicalSource, err := resolvePath(source)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	targetPath := filepath.Join(
		manager.root,
		repositoryHash(canonicalSource),
		testAgentID,
	)
	_, err = manager.Prepare(
		context.Background(),
		source,
		"",
		testAgentID,
	)
	if err == nil ||
		!strings.Contains(err.Error(), "included paths changed") {
		t.Fatalf("prepare after tracked transition error = %v", err)
	}
	if _, err := os.Lstat(targetPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed preparation path remains or inspect failed: %v", err)
	}
	if _, err := os.Lstat(
		workspaceRecordPath(targetPath),
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed preparation record remains or inspect failed: %v", err)
	}
}
