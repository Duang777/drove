//go:build darwin

package workspace

import (
	"path/filepath"
	"strings"

	"golang.org/x/text/unicode/norm"
)

func workspaceManagerLockKey(path string) string {
	return norm.NFD.String(strings.ToUpper(filepath.Clean(path)))
}
