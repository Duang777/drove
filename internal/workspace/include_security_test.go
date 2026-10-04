package workspace

import (
	"errors"
	"os"
	"path/filepath"
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
	destination, err := openRealPathRoot(destinationPath)
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

	if err := copyIncludedPath(source, destination, "secret"); err != nil {
		t.Fatalf("copy included path: %v", err)
	}
	assertFileContents(
		t,
		filepath.Join(destinationPath, "secret"),
		"original\n",
	)
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
	destination, err := openRealPathRoot(destinationPath)
	if err != nil {
		t.Fatalf("open destination root: %v", err)
	}
	defer destination.Close()

	err = copyIncludedPath(source, destination, "linked/secret")
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
	destination, err := openRealPathRoot(destinationPath)
	if err != nil {
		t.Fatalf("open destination root: %v", err)
	}
	defer destination.Close()

	if err := copyIncludedPath(
		source,
		destination,
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
