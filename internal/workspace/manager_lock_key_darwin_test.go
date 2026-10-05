//go:build darwin

package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceManagerLockKeyFoldsDarwinPathCase(t *testing.T) {
	root := t.TempDir()
	lower := filepath.Join(root, "data", "worktrees")
	upper := filepath.Join(root, "DATA", "WORKTREES")
	if workspaceManagerLockKey(lower) != workspaceManagerLockKey(upper) {
		t.Fatal("case aliases received different manager lock keys")
	}
	if workspaceManagerLock(lower) != workspaceManagerLock(upper) {
		t.Fatal("case aliases received different manager locks")
	}
}

func TestRegisteredWorktreePathAcceptsDarwinCaseAlias(t *testing.T) {
	root := t.TempDir()
	actual := filepath.Join(root, "Worktree")
	if err := os.Mkdir(actual, 0o700); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	alias := filepath.Join(root, "worktree")
	if _, err := os.Stat(alias); err != nil {
		t.Skip("test volume is case-sensitive")
	}
	if !sameRegisteredWorktreePath(actual, alias) {
		t.Fatal("case aliases were treated as different worktree registrations")
	}
}
