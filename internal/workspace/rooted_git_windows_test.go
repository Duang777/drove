//go:build windows

package workspace

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestRootedWorktreeGitCommandBindsCommonDirectory(t *testing.T) {
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
		[]string{"read-tree", "--reset", "-u", "HEAD"},
	)
	if err != nil {
		t.Fatalf("create rooted worktree command: %v", err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup rooted worktree command: %v", err)
		}
	}()
	for _, want := range []string{
		"GIT_DIR=" + gitPath,
		"GIT_WORK_TREE=" + worktreePath,
		"GIT_COMMON_DIR=" + commonPath,
	} {
		if !slices.Contains(command.Env, want) {
			t.Fatalf("worktree command environment lacks %q: %q", want, command.Env)
		}
	}
}
