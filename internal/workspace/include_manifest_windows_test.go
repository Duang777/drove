//go:build windows

package workspace

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestConfigureIncludeManifestCommandRejectsReparsePoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest")
	if err := os.WriteFile(path, []byte("allowed.txt\n"), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatalf("open manifest parent: %v", err)
	}
	defer root.Close()
	manifest, err := root.Open(filepath.Base(path))
	if err != nil {
		t.Fatalf("open manifest: %v", err)
	}
	defer manifest.Close()

	movedPath := path + ".moved"
	if err := os.Rename(path, movedPath); err != nil {
		t.Fatalf("rename opened manifest: %v", err)
	}
	if err := os.Symlink(movedPath, path); err != nil {
		if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			t.Skipf("creating symlinks requires additional privilege: %v", err)
		}
		t.Fatalf("replace manifest with symlink: %v", err)
	}

	_, _, cleanup, err := configureIncludeManifestCommand(
		exec.Command("cmd"),
		manifest,
		path,
	)
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil {
		t.Fatal("configure manifest command accepted a reparse point")
	}
}

func TestConfigureIncludeManifestCommandLocksManifestIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest")
	if err := os.WriteFile(path, []byte("allowed.txt\n"), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatalf("open manifest parent: %v", err)
	}
	defer root.Close()
	manifest, err := root.Open(filepath.Base(path))
	if err != nil {
		t.Fatalf("open manifest: %v", err)
	}
	defer manifest.Close()
	_, _, cleanup, err := configureIncludeManifestCommand(
		exec.Command("cmd"),
		manifest,
		path,
	)
	if err != nil {
		t.Fatalf("configure manifest command: %v", err)
	}
	movedPath := path + ".moved"
	if err := os.Rename(path, movedPath); err == nil {
		_ = cleanup()
		t.Fatal("manifest could be replaced while command guard was held")
	}
	if file, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
		_ = file.Close()
		_ = cleanup()
		t.Fatal("manifest could be rewritten while command guard was held")
	}
	if err := cleanup(); err != nil {
		t.Fatalf("release manifest guard: %v", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open manifest for writing after releasing guard: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close writable manifest: %v", err)
	}
	if err := os.Rename(path, movedPath); err != nil {
		t.Fatalf("rename manifest after releasing guard: %v", err)
	}
}
