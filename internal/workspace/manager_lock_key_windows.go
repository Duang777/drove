//go:build windows

package workspace

import (
	"path/filepath"
	"strings"
)

func workspaceManagerLockKey(path string) string {
	return strings.ToUpper(filepath.Clean(normalizeWindowsManagerPath(path)))
}

func normalizeWindowsManagerPath(path string) string {
	path = filepath.Clean(path)
	for _, prefix := range []string{`\\?\`, `\??\`} {
		remainder, found := cutWindowsPrefix(path, prefix)
		if !found {
			continue
		}
		if unc, found := cutWindowsPrefix(remainder, `UNC\`); found {
			return `\\` + unc
		}
		if len(remainder) >= 3 &&
			remainder[1] == ':' &&
			(remainder[2] == '\\' || remainder[2] == '/') {
			return remainder
		}
	}
	return path
}

func cutWindowsPrefix(path string, prefix string) (string, bool) {
	if len(path) < len(prefix) ||
		!strings.EqualFold(path[:len(prefix)], prefix) {
		return "", false
	}
	return path[len(prefix):], true
}
