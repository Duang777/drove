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

func TestAcknowledgeUsesRetainedRepositoryAfterPrepareSourceSwap(
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
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge through retained repository: %v", err)
	}

	assertBranchExists(t, openedSource, prepared.Branch)
	assertOwnershipRefsAbsent(t, openedSource)
	assertPreparationRefsAbsent(t, source, prepared.Branch)
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil {
		t.Fatalf("read acknowledged record: %v", err)
	}
	if !exists || !record.PreparationCommitted {
		t.Fatalf("acknowledged record = %+v, exists=%v", record, exists)
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
	if err := restarted.ReconcilePreparations(
		context.Background(),
		nil,
	); err == nil {
		t.Fatal("reconciliation accepted a replacement common Git directory")
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
