package webui

import (
	"io/fs"
	"strings"
	"testing"
)

func TestEmbeddedBuildContainsEntrypointAssets(t *testing.T) {
	build := FS()
	index, err := fs.ReadFile(build, "index.html")
	if err != nil {
		t.Fatalf("read embedded index: %v", err)
	}
	if !strings.Contains(string(index), "/assets/") {
		t.Fatalf("embedded index has no production asset references: %q", index)
	}
	for _, pattern := range []string{"assets/*.js", "assets/*.css"} {
		matches, err := fs.Glob(build, pattern)
		if err != nil {
			t.Fatalf("glob %q: %v", pattern, err)
		}
		if len(matches) == 0 {
			t.Fatalf("embedded build has no files matching %q", pattern)
		}
	}
}
