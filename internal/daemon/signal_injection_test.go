package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExecutableFileRequiresRegularExecutableWithoutSymlink(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "drove")
	if err := os.WriteFile(executable, []byte("binary"), 0o700); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	if got := executableFile(executable); got != executable {
		t.Fatalf("executable file = %q, want %q", got, executable)
	}

	nonExecutable := filepath.Join(dir, "not-executable")
	if err := os.WriteFile(nonExecutable, []byte("binary"), 0o600); err != nil {
		t.Fatalf("write non-executable: %v", err)
	}
	if got := executableFile(nonExecutable); got != "" {
		t.Fatalf("non-executable resolved to %q", got)
	}

	symlink := filepath.Join(dir, "drove-link")
	if err := os.Symlink(executable, symlink); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if got := executableFile(symlink); got != "" {
		t.Fatalf("symlink resolved to %q", got)
	}
}
