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

func TestNewFoldsDarwinUnicodePathAliases(t *testing.T) {
	parent := t.TempDir()
	composed := filepath.Join(parent, "\u00e9")
	decomposed := filepath.Join(parent, "e\u0301")
	if err := os.Mkdir(composed, 0o700); err != nil {
		t.Fatalf("create composed data directory: %v", err)
	}
	if _, err := os.Stat(decomposed); err != nil {
		t.Skip("test volume does not canonicalize Unicode path aliases")
	}
	first, err := New(composed)
	if err != nil {
		t.Fatalf("new composed manager: %v", err)
	}
	second, err := New(decomposed)
	if err != nil {
		t.Fatalf("new decomposed manager: %v", err)
	}
	if first.mu != second.mu {
		t.Fatal("Unicode path aliases received different manager locks")
	}
}

func TestNewFoldsDarwinUnicodeCaseAliases(t *testing.T) {
	parent := t.TempDir()
	lower := filepath.Join(parent, "\u00df")
	upper := filepath.Join(parent, "\u1e9e")
	if err := os.Mkdir(lower, 0o700); err != nil {
		t.Fatalf("create lowercase data directory: %v", err)
	}
	if _, err := os.Stat(upper); err != nil {
		t.Skip("test volume does not fold Unicode path case")
	}
	first, err := New(lower)
	if err != nil {
		t.Fatalf("new lowercase manager: %v", err)
	}
	second, err := New(upper)
	if err != nil {
		t.Fatalf("new uppercase manager: %v", err)
	}
	if first.mu != second.mu {
		t.Fatal("Unicode case aliases received different manager locks")
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
