//go:build !darwin && !windows

package workspace

import "path/filepath"

func workspaceManagerLockKey(path string) string {
	return filepath.Clean(path)
}
