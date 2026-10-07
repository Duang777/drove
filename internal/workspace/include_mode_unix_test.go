//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package workspace

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestCopyIncludedPathPreservesModeUnderRestrictiveUmask(t *testing.T) {
	sourcePath := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(sourcePath, "shared"),
		[]byte("contents\n"),
		0o644,
	); err != nil {
		t.Fatalf("write source: %v", err)
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

	previousUmask := syscall.Umask(0o077)
	defer syscall.Umask(previousUmask)
	if err := copyIncludedPath(
		source,
		destinationBucket,
		destination,
		filepath.Base(destinationPath),
		"shared",
	); err != nil {
		t.Fatalf("copy included path: %v", err)
	}
	info, err := os.Stat(filepath.Join(destinationPath, "shared"))
	if err != nil {
		t.Fatalf("inspect copied destination: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o644); got != want {
		t.Fatalf("copied destination mode = %o, want %o", got, want)
	}
}
