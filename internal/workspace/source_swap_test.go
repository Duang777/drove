//go:build !windows

package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	command = exec.Command(
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
