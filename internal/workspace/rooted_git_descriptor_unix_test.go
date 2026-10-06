//go:build linux

package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func inheritedDirectoryPath(fd int) string {
	return "/proc/self/fd/" + fmt.Sprint(fd)
}

func TestRootedPrivateGitCommandBindsCommonDirectory(t *testing.T) {
	parent := t.TempDir()
	gitPath := filepath.Join(parent, "git")
	commonPath := filepath.Join(parent, "common")
	for _, path := range []string{gitPath, commonPath} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("create directory %q: %v", path, err)
		}
	}
	gitRoot, err := openRealPathRoot(gitPath)
	if err != nil {
		t.Fatalf("open Git root: %v", err)
	}
	defer gitRoot.Close()
	commonRoot, err := openRealPathRoot(commonPath)
	if err != nil {
		t.Fatalf("open common root: %v", err)
	}
	defer commonRoot.Close()

	command, cleanup, err := rootedPrivateGitCommand(
		context.Background(),
		"git",
		filepath.Join(parent, "worktree"),
		gitPath,
		gitRoot,
		commonPath,
		commonRoot,
		[]string{"version"},
	)
	if err != nil {
		t.Fatalf("create rooted private command: %v", err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup rooted private command: %v", err)
		}
	}()
	if len(command.ExtraFiles) != 2 {
		t.Fatalf(
			"private command inherited files = %d, want 2",
			len(command.ExtraFiles),
		)
	}
	want := "GIT_COMMON_DIR=" + inheritedDirectoryPath(4)
	if !slices.Contains(command.Env, want) {
		t.Fatalf("private command environment lacks %q: %q", want, command.Env)
	}
}

func TestRootedPreparedWorktreeGitCommandBindsAllDirectories(t *testing.T) {
	parent := t.TempDir()
	repositoryPath := filepath.Join(parent, "repository")
	commonPath := filepath.Join(parent, "common")
	worktreePath := filepath.Join(parent, "worktree")
	for _, path := range []string{repositoryPath, commonPath, worktreePath} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("create directory %q: %v", path, err)
		}
	}
	repositoryRoot, err := openRealPathRoot(repositoryPath)
	if err != nil {
		t.Fatalf("open repository root: %v", err)
	}
	defer repositoryRoot.Close()
	commonRoot, err := openRealPathRoot(commonPath)
	if err != nil {
		t.Fatalf("open common root: %v", err)
	}
	defer commonRoot.Close()
	worktreeRoot, err := openRealPathRoot(worktreePath)
	if err != nil {
		t.Fatalf("open worktree root: %v", err)
	}
	defer worktreeRoot.Close()

	command, cleanup, err := rootedPreparedWorktreeGitCommand(
		context.Background(),
		"git",
		repositoryPath,
		repositoryRoot,
		commonPath,
		commonRoot,
		worktreePath,
		worktreeRoot,
		[]string{"version"},
	)
	if err != nil {
		t.Fatalf("create rooted prepared command: %v", err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup rooted prepared command: %v", err)
		}
	}()
	if len(command.ExtraFiles) != 3 {
		t.Fatalf("prepared command inherited files = %d, want 3", len(command.ExtraFiles))
	}
	for _, want := range []string{
		"GIT_DIR=" + inheritedDirectoryPath(4),
		"GIT_WORK_TREE=" + inheritedDirectoryPath(5),
	} {
		if !slices.Contains(command.Env, want) {
			t.Fatalf("prepared command environment lacks %q: %q", want, command.Env)
		}
	}
}

func TestRootedWorktreeGitCommandBindsAllDirectories(t *testing.T) {
	parent := t.TempDir()
	worktreePath := filepath.Join(parent, "worktree")
	gitPath := filepath.Join(parent, "git")
	commonPath := filepath.Join(parent, "common")
	for _, path := range []string{worktreePath, gitPath, commonPath} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("create directory %q: %v", path, err)
		}
	}
	worktreeRoot, err := openRealPathRoot(worktreePath)
	if err != nil {
		t.Fatalf("open worktree root: %v", err)
	}
	defer worktreeRoot.Close()
	gitRoot, err := openRealPathRoot(gitPath)
	if err != nil {
		t.Fatalf("open Git root: %v", err)
	}
	defer gitRoot.Close()
	commonRoot, err := openRealPathRoot(commonPath)
	if err != nil {
		t.Fatalf("open common root: %v", err)
	}
	defer commonRoot.Close()

	command, cleanup, err := rootedWorktreeGitCommand(
		context.Background(),
		"git",
		worktreePath,
		worktreeRoot,
		gitPath,
		gitRoot,
		commonPath,
		commonRoot,
		[]string{"version"},
	)
	if err != nil {
		t.Fatalf("create rooted worktree command: %v", err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup rooted worktree command: %v", err)
		}
	}()
	if len(command.ExtraFiles) != 3 {
		t.Fatalf("worktree command inherited files = %d, want 3", len(command.ExtraFiles))
	}
	for _, want := range []string{
		"GIT_DIR=" + inheritedDirectoryPath(4),
		"GIT_WORK_TREE=" + inheritedDirectoryPath(3),
		"GIT_COMMON_DIR=" + inheritedDirectoryPath(5),
	} {
		if !slices.Contains(command.Env, want) {
			t.Fatalf("worktree command environment lacks %q: %q", want, command.Env)
		}
	}
}
