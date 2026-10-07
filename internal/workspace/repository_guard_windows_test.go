//go:build windows

package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenRepositoryGuardRejectsDirectoryJunction(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	junction := filepath.Join(parent, "junction")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("create target directory: %v", err)
	}
	command := exec.Command("cmd", "/c", "mklink", "/J", junction, target)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf(
			"create directory junction: %v: %s",
			err,
			strings.TrimSpace(string(output)),
		)
	}
	root, err := openRealPathRoot(target)
	if err != nil {
		t.Fatalf("open target root: %v", err)
	}
	defer root.Close()

	guard, err := openRepositoryGuard(junction, root)
	if guard != nil {
		_ = guard.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "reparse point") {
		t.Fatalf("guard directory junction error = %v", err)
	}
}
