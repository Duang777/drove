//go:build windows

package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

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
	if err := cleanup(); err != nil {
		t.Fatalf("release manifest guard: %v", err)
	}
	if err := os.Rename(path, movedPath); err != nil {
		t.Fatalf("rename manifest after releasing guard: %v", err)
	}
}
