package workspace

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestRootedPrivateGitCommandBindsCommonDirectoryForPlatform(
	t *testing.T,
) {
	switch runtime.GOOS {
	case "darwin", "dragonfly", "freebsd", "linux", "netbsd", "openbsd",
		"windows":
	default:
		t.Skip("rooted private Git execution is unsupported")
	}
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
	wantExtraFiles := 1
	if runtime.GOOS == "linux" {
		wantExtraFiles = 2
	} else if runtime.GOOS == "windows" {
		wantExtraFiles = 0
	}
	if len(command.ExtraFiles) != wantExtraFiles {
		t.Fatalf(
			"private command inherited files = %d, want %d",
			len(command.ExtraFiles),
			wantExtraFiles,
		)
	}
	switch runtime.GOOS {
	case "linux":
		want := "GIT_COMMON_DIR=/proc/self/fd/4"
		if !slices.Contains(command.Env, want) {
			t.Fatalf("private command environment lacks %q", want)
		}
	case "windows":
		want := "GIT_COMMON_DIR=" + commonPath
		if !slices.Contains(command.Env, want) {
			t.Fatalf("private command environment lacks %q", want)
		}
	default:
		for _, entry := range command.Env {
			if strings.HasPrefix(entry, "GIT_COMMON_DIR=") {
				t.Fatalf(
					"private command reopens common Git directory: %q",
					entry,
				)
			}
		}
	}
}

func TestBSDWorktreeRepairUsesPrivateGitRoot(t *testing.T) {
	switch runtime.GOOS {
	case "darwin", "dragonfly", "freebsd", "netbsd", "openbsd":
	default:
		t.Skip("test covers the BSD rooted Git helper")
	}
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

	command, cleanup, err := rootedRepairWorktreeGitCommand(
		context.Background(),
		"git",
		worktreePath,
		worktreeRoot,
		gitPath,
		gitRoot,
		commonPath,
		commonRoot,
	)
	if err != nil {
		t.Fatalf("create rooted repair command: %v", err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup rooted repair command: %v", err)
		}
	}()
	if len(command.ExtraFiles) != 1 {
		t.Fatalf("repair command inherited files = %d, want 1", len(command.ExtraFiles))
	}
	for _, want := range []string{
		"GIT_DIR=.",
		"GIT_WORK_TREE=" + worktreePath,
	} {
		if !slices.Contains(command.Env, want) {
			t.Fatalf("repair command environment lacks %q", want)
		}
	}
	for _, entry := range command.Env {
		if strings.HasPrefix(entry, "GIT_COMMON_DIR=") {
			t.Fatalf("repair command reopens common Git directory: %q", entry)
		}
	}
	wantArguments := []string{"worktree", "repair", worktreePath}
	if len(command.Args) < len(wantArguments) ||
		!slices.Equal(
			command.Args[len(command.Args)-len(wantArguments):],
			wantArguments,
		) {
		t.Fatalf("repair command arguments = %q", command.Args)
	}
}

func TestBoundGitEnvironmentRemovesInheritedGitConfiguration(t *testing.T) {
	environment := boundGitEnvironment(
		[]string{
			"PATH=/usr/bin",
			"GIT_COMMON_DIR=/replacement",
			"git_index_file=/replacement/index",
			"HOME=/tmp/home",
		},
		".",
		"/source",
	)

	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, "GIT_INDEX_FILE") ||
			strings.EqualFold(name, "GIT_COMMON_DIR") {
			t.Fatalf("bound environment retained %q", entry)
		}
	}
	for _, want := range []string{
		"PATH=/usr/bin",
		"HOME=/tmp/home",
		"GIT_DIR=.",
		"GIT_WORK_TREE=/source",
	} {
		if !slices.Contains(environment, want) {
			t.Fatalf("bound environment %q does not contain %q", environment, want)
		}
	}
}

func TestBoundGitEnvironmentWithCommonSetsExplicitCommonDirectory(
	t *testing.T,
) {
	environment := boundGitEnvironmentWithCommon(
		[]string{"GIT_COMMON_DIR=/replacement"},
		"/git",
		"/worktree",
		"/common",
	)
	for _, want := range []string{
		"GIT_DIR=/git",
		"GIT_WORK_TREE=/worktree",
		"GIT_COMMON_DIR=/common",
	} {
		if !slices.Contains(environment, want) {
			t.Fatalf("bound environment %q does not contain %q", environment, want)
		}
	}
}
