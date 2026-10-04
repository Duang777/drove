// Package webui exposes the embedded Drove browser console.
package webui

import (
	"embed"
	"io/fs"
)

//go:generate npm --prefix ../../web run build

//go:embed dist
var embedded embed.FS

// FS returns the root of the embedded production build.
func FS() fs.FS {
	dist, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err)
	}
	return dist
}
