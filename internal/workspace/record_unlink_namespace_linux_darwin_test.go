//go:build darwin || linux

package workspace

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCleanupRecordDeletionNamespaceWaitsForParentLock(t *testing.T) {
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
	if err := namespace.Close(); err != nil {
		t.Fatalf("close deletion namespace: %v", err)
	}
	if err := directory.Close(); err != nil {
		t.Fatalf("close record directory: %v", err)
	}

	lockHolder, err := os.Open(filepath.Clean(rootPath))
	if err != nil {
		t.Fatalf("open parent lock holder: %v", err)
	}
	defer lockHolder.Close()
	if err := unix.Flock(int(lockHolder.Fd()), unix.LOCK_EX); err != nil {
		t.Fatalf("lock record directory: %v", err)
	}
	locked := true
	defer func() {
		if locked {
			_ = unix.Flock(int(lockHolder.Fd()), unix.LOCK_UN)
		}
	}()

	done := make(chan error, 1)
	go func() {
		done <- cleanupRecordDeletionNamespace(root)
	}()
	select {
	case err := <-done:
		t.Fatalf("cleanup bypassed parent lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := unix.Flock(int(lockHolder.Fd()), unix.LOCK_UN); err != nil {
		t.Fatalf("unlock record directory: %v", err)
	}
	locked = false
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cleanup after parent unlock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup remained blocked after parent unlock")
	}
}

func TestUnlinkLinkedRecordPathLocksParentBeforeSourceCheck(t *testing.T) {
	rootPath := t.TempDir()
	sourcePath := filepath.Join(rootPath, "source")
	if err := os.WriteFile(sourcePath, []byte("record\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	targetPath := filepath.Join(rootPath, "target")
	if err := os.Link(sourcePath, targetPath); err != nil {
		t.Fatalf("link target witness: %v", err)
	}
	expected, err := os.Open(sourcePath)
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	defer expected.Close()
	directory, err := os.Open(rootPath)
	if err != nil {
		t.Fatalf("open record directory: %v", err)
	}
	defer directory.Close()
	lockHolder, err := os.Open(rootPath)
	if err != nil {
		t.Fatalf("open parent lock holder: %v", err)
	}
	defer lockHolder.Close()
	if err := unix.Flock(int(lockHolder.Fd()), unix.LOCK_EX); err != nil {
		t.Fatalf("lock record directory: %v", err)
	}
	locked := true
	defer func() {
		if locked {
			_ = unix.Flock(int(lockHolder.Fd()), unix.LOCK_UN)
		}
	}()

	done := make(chan error, 1)
	go func() {
		done <- unlinkLinkedRecordPath(
			directory,
			expected,
			"source",
			"target",
		)
	}()
	select {
	case err := <-done:
		t.Fatalf("linked unlink bypassed parent lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.Remove(sourcePath); err != nil {
		t.Fatalf("remove source while unlink waits: %v", err)
	}
	if err := unix.Flock(int(lockHolder.Fd()), unix.LOCK_UN); err != nil {
		t.Fatalf("unlock record directory: %v", err)
	}
	locked = false
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("accept missing linked source after lock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("linked unlink remained blocked after parent unlock")
	}
	assertFileContents(t, targetPath, "record\n")
}
