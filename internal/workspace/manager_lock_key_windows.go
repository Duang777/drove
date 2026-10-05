//go:build windows

package workspace

import (
	"path/filepath"
	"strings"
)

func workspaceManagerLockKey(path string) string {
	return strings.ToUpper(filepath.Clean(path))
}
