//go:build darwin

package workspace

import (
	"path/filepath"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

func workspaceManagerLockKey(path string) string {
	return norm.NFD.String(cases.Fold().String(filepath.Clean(path)))
}
