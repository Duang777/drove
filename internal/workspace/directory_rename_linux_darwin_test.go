//go:build darwin || linux

package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRenameDirectoryNoReplaceRejectsReplacementSource(t *testing.T) {
	rootPath := t.TempDir()
	sourcePath := filepath.Join(rootPath, "source")
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatalf("create source directory: %v", err)
	}
	expected, err := os.Lstat(sourcePath)
	if err != nil {
		t.Fatalf("inspect source directory: %v", err)
	}
	originalPath := filepath.Join(rootPath, "source-original")
	if err := os.Rename(sourcePath, originalPath); err != nil {
		t.Fatalf("move expected source directory: %v", err)
	}
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatalf("create replacement source directory: %v", err)
	}
	sentinel := filepath.Join(sourcePath, "replacement")
	if err := os.WriteFile(sentinel, []byte("replacement\n"), 0o600); err != nil {
		t.Fatalf("write replacement sentinel: %v", err)
	}
	directory, err := os.Open(rootPath)
	if err != nil {
		t.Fatalf("open parent directory: %v", err)
	}
	defer directory.Close()

	moved, err := renameDirectoryNoReplace(
		directory,
		expected,
		"source",
		"source-isolated",
		"target",
	)
	if err == nil || moved {
		t.Fatalf("rename replacement = moved %v, err=%v", moved, err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("replacement source was not restored: %v", err)
	}
	if _, err := os.Stat(originalPath); err != nil {
		t.Fatalf("expected source changed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(rootPath, "target")); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("target exists after rejected replacement: %v", err)
	}
}
