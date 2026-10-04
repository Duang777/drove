package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
	if len(listed) != 1 || listed[0].Dirty || listed[0].Branch != prepared.Branch {
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
