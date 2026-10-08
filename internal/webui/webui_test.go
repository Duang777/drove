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
	for _, path := range []string{
		"manifest.webmanifest",
		"service-worker.js",
		"icons/drove-192.png",
		"icons/drove-512.png",
	} {
		info, err := fs.Stat(build, path)
		if err != nil {
			t.Fatalf("stat embedded PWA file %q: %v", path, err)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			t.Fatalf("embedded PWA file %q is empty or not regular", path)
		}
	}
	worker, err := fs.ReadFile(build, "service-worker.js")
	if err != nil {
		t.Fatalf("read embedded service worker: %v", err)
	}
	if strings.Contains(string(worker), "addEventListener('fetch'") ||
		!strings.Contains(string(worker), "notificationclick") {
		t.Fatalf("embedded service worker has unsafe cache or no click handler")
	}
}
