//go:build dragonfly || freebsd || netbsd || openbsd

package workspace

import (
	"os"
	"strings"
	"testing"
)

func TestCleanupRecordDeletionNamespaceFailsWithoutNoReplaceRename(
	t *testing.T,
) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("open record root: %v", err)
	}
	defer root.Close()
	directory, err := openRecordDirectory(root)
	if err != nil {
		t.Fatalf("open record directory: %v", err)
	}
	namespace, err := openRecordDeletionNamespace(directory)
	if err != nil {
		t.Fatalf("open deletion namespace: %v", err)
	}
	expected, err := namespace.Stat()
	if err != nil {
		t.Fatalf("inspect deletion namespace: %v", err)
	}
	if err := namespace.Close(); err != nil {
		t.Fatalf("close deletion namespace: %v", err)
	}
	if err := directory.Close(); err != nil {
		t.Fatalf("close record directory: %v", err)
	}

	err = cleanupRecordDeletionNamespace(root)
	if err == nil ||
		!strings.Contains(
			err.Error(),
			"atomic no-replace directory rename is unsupported",
		) {
		t.Fatalf("cleanup without no-replace rename error = %v", err)
	}
	current, err := root.Stat(recordDeletionNamespace)
	if err != nil {
		t.Fatalf("deletion namespace was removed: %v", err)
	}
	if !os.SameFile(expected, current) {
		t.Fatal("deletion namespace changed identity")
	}
}
