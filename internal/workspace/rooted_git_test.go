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
	commonDirectory := commonPath
	switch runtime.GOOS {
	case "linux":
		commonDirectory = "/proc/self/fd/4"
	}
	want := "GIT_COMMON_DIR=" + commonDirectory
	if !slices.Contains(command.Env, want) {
		t.Fatalf("private command environment lacks %q: %q", want, command.Env)
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
