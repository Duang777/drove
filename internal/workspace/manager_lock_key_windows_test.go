//go:build windows

package workspace

import "testing"

func TestWorkspaceManagerLockKeyFoldsPathCase(t *testing.T) {
	lower := workspaceManagerLockKey(`C:\drove\data\worktrees`)
	upper := workspaceManagerLockKey(`c:\DROVE\DATA\WORKTREES`)
	if lower != upper {
		t.Fatalf("manager lock keys differ: %q != %q", lower, upper)
	}
	if workspaceManagerLock(`C:\drove\data\worktrees`) !=
		workspaceManagerLock(`c:\DROVE\DATA\WORKTREES`) {
		t.Fatal("case aliases received different manager locks")
	}
}
