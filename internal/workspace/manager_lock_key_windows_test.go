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

func TestWorkspaceManagerLockKeyFoldsNamespaceAliases(t *testing.T) {
	for _, test := range []struct {
		name    string
		regular string
		aliases []string
	}{
		{
			name:    "drive",
			regular: `C:\drove\data\worktrees`,
			aliases: []string{
				`\\?\C:\drove\data\worktrees`,
				`\??\C:\drove\data\worktrees`,
			},
		},
		{
			name:    "unc",
			regular: `\\server\share\drove\data\worktrees`,
			aliases: []string{
				`\\?\UNC\server\share\drove\data\worktrees`,
				`\??\UNC\server\share\drove\data\worktrees`,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			wantKey := workspaceManagerLockKey(test.regular)
			wantLock := workspaceManagerLock(test.regular)
			for _, alias := range test.aliases {
				if got := workspaceManagerLockKey(alias); got != wantKey {
					t.Errorf(
						"manager lock key for %q = %q, want %q",
						alias,
						got,
						wantKey,
					)
				}
				if got := workspaceManagerLock(alias); got != wantLock {
					t.Errorf(
						"manager lock for %q differs from canonical path",
						alias,
					)
				}
			}
		})
	}
}
