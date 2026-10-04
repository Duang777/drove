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

const testAgentID = "11111111-1111-4111-8111-111111111111"
const secondTestAgentID = "22222222-2222-4222-8222-222222222222"

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
	if _, err := manager.Cleanup(context.Background(), testAgentID, false); !errors.Is(err, ErrDirty) {
		t.Fatalf("cleanup dirty worktree error = %v, want ErrDirty", err)
	}
	removed, err := manager.Cleanup(context.Background(), testAgentID, true)
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
	if _, err := manager.Cleanup(
		context.Background(),
		first.AgentID,
		true,
	); err != nil {
		t.Fatalf("cleanup first worktree: %v", err)
	}
	if _, err := manager.Cleanup(
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

	listed, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("list detached worktree: %v", err)
	}
	if len(listed) != 1 ||
		!listed[0].Detached ||
		listed[0].Branch != prepared.Branch {
		t.Fatalf("detached worktrees = %+v", listed)
	}
	if _, err := manager.Cleanup(
		context.Background(),
		testAgentID,
		false,
	); err != nil {
		t.Fatalf("cleanup detached worktree: %v", err)
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
	if _, err := manager.Cleanup(
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
	runGit(t, repository, "worktree", "remove", "--force", prepared.Path)

	bucket := filepath.Dir(prepared.Path)
	savedBucket := bucket + ".saved"
	if err := os.Rename(bucket, savedBucket); err != nil {
		t.Fatalf("move repository bucket: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove(bucket)
		_ = os.Rename(savedBucket, bucket)
	})

	outside := t.TempDir()
	outsideWorkspace := filepath.Join(outside, prepared.AgentID)
	if err := os.Mkdir(outsideWorkspace, 0o700); err != nil {
		t.Fatalf("create outside workspace: %v", err)
	}
	sentinel := filepath.Join(outsideWorkspace, "must-remain.txt")
	if err := os.WriteFile(sentinel, []byte("outside\n"), 0o600); err != nil {
		t.Fatalf("write outside sentinel: %v", err)
	}
	if err := os.Symlink(outside, bucket); err != nil {
		t.Fatalf("replace repository bucket with symlink: %v", err)
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
  *" ls-files --others --ignored "*)
    if [ ! -e "$DROVE_TEST_INJECT_MARKER" ]; then
      "$DROVE_TEST_REAL_GIT" "$@"
      status=$?
      : > "$DROVE_TEST_INJECT_MARKER"
      printf 'late local value\n' > "$DROVE_TEST_INJECT_FILE"
      exit "$status"
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

	if _, err := manager.Cleanup(
		context.Background(),
		prepared.AgentID,
		false,
	); !errors.Is(err, ErrDirty) {
		t.Fatalf("cleanup after late include error = %v, want ErrDirty", err)
	}
	assertFileContents(t, injected, "late local value\n")

	manager.git = realGit
	if _, err := manager.Cleanup(
		context.Background(),
		prepared.AgentID,
		true,
	); err != nil {
		t.Fatalf("force cleanup retained worktree: %v", err)
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

func TestCleanupRejectsInvalidAndUnknownAgentIDs(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if _, err := manager.Cleanup(context.Background(), "not-an-id", false); err == nil {
		t.Fatal("cleanup accepted an invalid Agent ID")
	}
	if _, err := manager.Cleanup(context.Background(), testAgentID, false); !errors.Is(err, ErrNotFound) {
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
		t.Fatalf("new manager: %v", err)
	}
	if _, err := manager.List(context.Background()); err == nil {
		t.Fatal("list accepted a symlinked worktree root")
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
