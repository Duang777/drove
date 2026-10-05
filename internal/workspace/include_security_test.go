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

func TestCopyIncludedPathUsesOpenedSourceRoot(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(sourcePath, "secret"),
		[]byte("original\n"),
		0o600,
	); err != nil {
		t.Fatalf("write source: %v", err)
	}
	destinationPath := filepath.Join(t.TempDir(), "destination")
	if err := os.Mkdir(destinationPath, 0o700); err != nil {
		t.Fatalf("create destination: %v", err)
	}
	source, err := openRealPathRoot(sourcePath)
	if err != nil {
		t.Fatalf("open source root: %v", err)
	}
	defer source.Close()
	destinationBucket, err := openRealPathRoot(filepath.Dir(destinationPath))
	if err != nil {
		t.Fatalf("open destination bucket: %v", err)
	}
	defer destinationBucket.Close()
	destination, err := openRealRootFromRoot(
		destinationBucket,
		filepath.Base(destinationPath),
	)
	if err != nil {
		t.Fatalf("open destination root: %v", err)
	}
	defer destination.Close()

	openedSourcePath := sourcePath + "-opened"
	if err := os.Rename(sourcePath, openedSourcePath); err != nil {
		t.Fatalf("move opened source: %v", err)
	}
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatalf("replace source directory: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(sourcePath, "secret"),
		[]byte("replacement\n"),
		0o600,
	); err != nil {
		t.Fatalf("write replacement source: %v", err)
	}

	if err := copyIncludedPath(
		source,
		destinationBucket,
		destination,
		filepath.Base(destinationPath),
		"secret",
	); err != nil {
		t.Fatalf("copy included path: %v", err)
	}
	assertFileContents(
		t,
		filepath.Join(destinationPath, "secret"),
		"original\n",
	)
}

func TestCopyIncludedFilesUsesPinnedSourceRoot(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(sourcePath, "secret"),
		[]byte("original\n"),
		0o600,
	); err != nil {
		t.Fatalf("write source: %v", err)
	}
	source, err := openRealPathRoot(sourcePath)
	if err != nil {
		t.Fatalf("open source root: %v", err)
	}
	defer source.Close()

	repository := filepath.Join(t.TempDir(), "repository")
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if err := manager.ensureManagedRoot(); err != nil {
		t.Fatalf("ensure managed root: %v", err)
	}
	if err := manager.ensureManagedBucket(repository); err != nil {
		t.Fatalf("ensure managed bucket: %v", err)
	}
	target := Workspace{
		AgentID:    testAgentID,
		Repository: repository,
		Path: filepath.Join(
			manager.root,
			repositoryHash(repository),
			testAgentID,
		),
		Branch: "pinned-source",
	}
	if err := os.Mkdir(target.Path, 0o700); err != nil {
		t.Fatalf("create destination: %v", err)
	}

	openedSourcePath := sourcePath + "-opened"
	if err := os.Rename(sourcePath, openedSourcePath); err != nil {
		t.Fatalf("move opened source: %v", err)
	}
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatalf("replace source directory: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(sourcePath, "secret"),
		[]byte("replacement\n"),
		0o600,
	); err != nil {
		t.Fatalf("write replacement source: %v", err)
	}

	if err := manager.copyIncludedFiles(
		source,
		target,
		[]string{"secret"},
	); err != nil {
		t.Fatalf("copy included files: %v", err)
	}
	assertFileContents(t, filepath.Join(target.Path, "secret"), "original\n")
	if err := verifyRealPathRoot(sourcePath, source); err == nil {
		t.Fatal("source replacement passed final identity verification")
	}
}

func TestIncludedPathsUsesOpenedManifestContents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX Git wrapper")
	}
	repository := newTestRepository(t)
	if err := os.WriteFile(
		filepath.Join(repository, "allowed.txt"),
		[]byte("allowed\n"),
		0o600,
	); err != nil {
		t.Fatalf("write allowed file: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repository, "secret.txt"),
		[]byte("secret\n"),
		0o600,
	); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	manifestPath := filepath.Join(repository, worktreeIncludeFile)
	if err := os.WriteFile(
		manifestPath,
		[]byte("allowed.txt\n"),
		0o600,
	); err != nil {
		t.Fatalf("write include manifest: %v", err)
	}
	replacementRules := filepath.Join(t.TempDir(), "replacement-rules")
	if err := os.WriteFile(
		replacementRules,
		[]byte("secret.txt\n"),
		0o600,
	); err != nil {
		t.Fatalf("write replacement rules: %v", err)
	}
	source, err := openRealPathRoot(repository)
	if err != nil {
		t.Fatalf("open source root: %v", err)
	}
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		_ = source.Close()
		t.Fatalf("new manager: %v", err)
	}
	lease, err := newPreparationLease(
		context.Background(),
		manager,
		repository,
		source,
	)
	if err != nil {
		_ = source.Close()
		t.Fatalf("retain source repository: %v", err)
	}
	defer lease.Close()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := `#!/bin/sh
case " $* " in
  *" ls-files "*)
    rm -f "$DROVE_TEST_MANIFEST" || exit 91
    ln -s "$DROVE_TEST_REPLACEMENT_RULES" "$DROVE_TEST_MANIFEST" || exit 92
    ;;
esac
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_MANIFEST", manifestPath)
	t.Setenv("DROVE_TEST_REPLACEMENT_RULES", replacementRules)
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	manager.git = wrapper

	paths, err := manager.includedPaths(
		context.Background(),
		repository,
		source,
		lease.repository,
	)
	if err != nil {
		t.Fatalf("evaluate pinned include manifest: %v", err)
	}
	if len(paths) != 1 || paths[0] != "allowed.txt" {
		t.Fatalf("included paths = %q, want [allowed.txt]", paths)
	}
}

func TestCopyIncludedPathRejectsSymlinkedParent(t *testing.T) {
	sourcePath := t.TempDir()
	outsideSource := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(outsideSource, "secret"),
		[]byte("outside\n"),
		0o600,
	); err != nil {
		t.Fatalf("write outside source: %v", err)
	}
	if err := os.Symlink(
		outsideSource,
		filepath.Join(sourcePath, "linked"),
	); err != nil {
		t.Skipf("create source symlink: %v", err)
	}
	destinationPath := t.TempDir()
	source, err := openRealPathRoot(sourcePath)
	if err != nil {
		t.Fatalf("open source root: %v", err)
	}
	defer source.Close()
	destinationBucket, err := openRealPathRoot(filepath.Dir(destinationPath))
	if err != nil {
		t.Fatalf("open destination bucket: %v", err)
	}
	defer destinationBucket.Close()
	destination, err := openRealRootFromRoot(
		destinationBucket,
		filepath.Base(destinationPath),
	)
	if err != nil {
		t.Fatalf("open destination root: %v", err)
	}
	defer destination.Close()

	err = copyIncludedPath(
		source,
		destinationBucket,
		destination,
		filepath.Base(destinationPath),
		"linked/secret",
	)
	if err == nil || !strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("copy through source symlink error = %v", err)
	}
	if _, err := os.Lstat(
		filepath.Join(destinationPath, "linked", "secret"),
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("copy through source symlink created destination: %v", err)
	}
}

func TestCopyIncludedPathRejectsSymlinkedDestinationParent(t *testing.T) {
	sourcePath := t.TempDir()
	if err := os.Mkdir(filepath.Join(sourcePath, "nested"), 0o700); err != nil {
		t.Fatalf("create source parent: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(sourcePath, "nested", "secret"),
		[]byte("inside\n"),
		0o600,
	); err != nil {
		t.Fatalf("write source: %v", err)
	}
	destinationPath := t.TempDir()
	outsideDestination := t.TempDir()
	if err := os.Symlink(
		outsideDestination,
		filepath.Join(destinationPath, "nested"),
	); err != nil {
		t.Skipf("create destination symlink: %v", err)
	}
	source, err := openRealPathRoot(sourcePath)
	if err != nil {
		t.Fatalf("open source root: %v", err)
	}
	defer source.Close()
	destinationBucket, err := openRealPathRoot(filepath.Dir(destinationPath))
	if err != nil {
		t.Fatalf("open destination bucket: %v", err)
	}
	defer destinationBucket.Close()
	destination, err := openRealRootFromRoot(
		destinationBucket,
		filepath.Base(destinationPath),
	)
	if err != nil {
		t.Fatalf("open destination root: %v", err)
	}
	defer destination.Close()

	if err := copyIncludedPath(
		source,
		destinationBucket,
		destination,
		filepath.Base(destinationPath),
		"nested/secret",
	); err == nil {
		t.Fatal("copy accepted a symlinked destination parent")
	}
	if _, err := os.Lstat(
		filepath.Join(outsideDestination, "secret"),
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("copy escaped destination root or inspect failed: %v", err)
	}
}

func TestCopyIncludedPathDoesNotFollowMovedDestinationRoot(t *testing.T) {
	sourcePath := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(sourcePath, "secret"),
		[]byte("inside\n"),
		0o600,
	); err != nil {
		t.Fatalf("write source: %v", err)
	}
	bucketPath := t.TempDir()
	destinationName := "workspace"
	destinationPath := filepath.Join(bucketPath, destinationName)
	if err := os.Mkdir(destinationPath, 0o700); err != nil {
		t.Fatalf("create destination: %v", err)
	}
	source, err := openRealPathRoot(sourcePath)
	if err != nil {
		t.Fatalf("open source root: %v", err)
	}
	defer source.Close()
	destinationBucket, err := openRealPathRoot(bucketPath)
	if err != nil {
		t.Fatalf("open destination bucket: %v", err)
	}
	defer destinationBucket.Close()
	destination, err := openRealRootFromRoot(
		destinationBucket,
		destinationName,
	)
	if err != nil {
		t.Fatalf("open destination root: %v", err)
	}
	defer destination.Close()

	outsidePath := filepath.Join(t.TempDir(), "moved-workspace")
	if err := os.Rename(destinationPath, outsidePath); err != nil {
		t.Fatalf("move destination outside bucket: %v", err)
	}
	err = copyIncludedPath(
		source,
		destinationBucket,
		destination,
		destinationName,
		"secret",
	)
	if err == nil {
		t.Fatal("copy accepted a moved destination workspace")
	}
	if _, err := os.Lstat(filepath.Join(outsidePath, "secret")); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("copy wrote into moved destination: %v", err)
	}
}
