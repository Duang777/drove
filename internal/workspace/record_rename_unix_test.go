//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenameRecordRejectsSourceReplacementAfterValidation(t *testing.T) {
	rootPath := t.TempDir()
	sourcePath := filepath.Join(rootPath, "source")
	source, err := os.OpenFile(
		sourcePath,
		os.O_RDWR|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	defer source.Close()
	if _, err := source.WriteString("original\n"); err != nil {
		t.Fatalf("write source: %v", err)
	}
	directory, err := os.Open(rootPath)
	if err != nil {
		t.Fatalf("open directory: %v", err)
	}
	defer directory.Close()
	movedPath := filepath.Join(rootPath, "source-original")

	installed, err := renameRecordPathAfterValidation(
		directory,
		source,
		"source",
		"target",
		false,
		func() {
			if err := os.Rename(sourcePath, movedPath); err != nil {
				t.Fatalf("move validated source: %v", err)
			}
			if err := os.WriteFile(
				sourcePath,
				[]byte("replacement\n"),
				0o600,
			); err != nil {
				t.Fatalf("install replacement source: %v", err)
			}
		},
	)
	if err == nil || installed {
		t.Fatalf(
			"rename replacement = installed %v, err=%v",
			installed,
			err,
		)
	}
	if _, err := os.Lstat(filepath.Join(rootPath, "target")); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("target exists after rejected replacement: %v", err)
	}
	assertFileContents(t, sourcePath, "replacement\n")
	assertFileContents(t, movedPath, "original\n")
	entries, err := os.ReadDir(rootPath)
	if err != nil {
		t.Fatalf("read record directory: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".drove-install-") {
			t.Fatalf("staged alias remains after rejected replacement: %q", entry.Name())
		}
	}
}

func TestUnlinkRecordPreservesReplacementAfterValidation(t *testing.T) {
	rootPath := t.TempDir()
	sourcePath := filepath.Join(rootPath, "source")
	source, err := os.OpenFile(
		sourcePath,
		os.O_RDWR|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	defer source.Close()
	if _, err := source.WriteString("original\n"); err != nil {
		t.Fatalf("write source: %v", err)
	}
	directory, err := os.Open(rootPath)
	if err != nil {
		t.Fatalf("open directory: %v", err)
	}
	defer directory.Close()
	movedPath := filepath.Join(rootPath, "source-original")

	err = unlinkRecordPathAfterValidation(
		directory,
		source,
		"source",
		func() {
			if err := os.Rename(sourcePath, movedPath); err != nil {
				t.Fatalf("move validated source: %v", err)
			}
			if err := os.WriteFile(
				sourcePath,
				[]byte("replacement\n"),
				0o600,
			); err != nil {
				t.Fatalf("install replacement source: %v", err)
			}
		},
	)
	if err == nil {
		t.Fatal("unlink accepted a replacement source")
	}
	assertFileContents(t, sourcePath, "replacement\n")
	assertFileContents(t, movedPath, "original\n")
}
