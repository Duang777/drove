package daemon

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectStoragePathsWarnsWithoutChangingBroadModes(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dataDir, 0o755); err != nil {
		t.Fatalf("create data directory: %v", err)
	}
	if err := os.Chmod(dataDir, 0o755); err != nil {
		t.Fatalf("set data directory mode: %v", err)
	}
	databasePath := filepath.Join(dataDir, "drove.db")
	if err := os.WriteFile(databasePath, nil, 0o644); err != nil {
		t.Fatalf("write database: %v", err)
	}
	if err := os.Chmod(databasePath, 0o644); err != nil {
		t.Fatalf("set database mode: %v", err)
	}

	var output bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&output, nil))
	if err := inspectStoragePaths(log, dataDir, databasePath); err != nil {
		t.Fatalf("inspect storage paths: %v", err)
	}
	logged := output.String()
	if strings.Count(logged, "storage path grants group or other permissions") != 2 ||
		!strings.Contains(logged, dataDir) ||
		!strings.Contains(logged, databasePath) ||
		!strings.Contains(logged, `"mode":"0755"`) ||
		!strings.Contains(logged, `"mode":"0644"`) {
		t.Fatalf("permission warnings = %q", logged)
	}
	for path, want := range map[string]os.FileMode{
		dataDir:      0o755,
		databasePath: 0o644,
	} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("inspect %s: %v", path, err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("mode for %s = %04o, want unchanged %04o", path, info.Mode().Perm(), want)
		}
	}
}

func TestInspectStoragePathsAcceptsSecurePathsAndMissingDatabase(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatalf("create data directory: %v", err)
	}
	var output bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&output, nil))
	if err := inspectStoragePaths(
		log,
		dataDir,
		filepath.Join(dataDir, "missing.db"),
	); err != nil {
		t.Fatalf("inspect secure storage paths: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("secure paths logged warnings: %q", output.String())
	}
}

func TestInspectStoragePathsRejectsInvalidTypes(t *testing.T) {
	t.Run("data file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "data")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("write data file: %v", err)
		}
		err := inspectStoragePaths(slog.Default(), path, filepath.Join(path, "drove.db"))
		if err == nil || !strings.Contains(err.Error(), "must be a directory") {
			t.Fatalf("inspect error = %v, want directory error", err)
		}
	})

	t.Run("database directory", func(t *testing.T) {
		dataDir := t.TempDir()
		databasePath := filepath.Join(dataDir, "drove.db")
		if err := os.Mkdir(databasePath, 0o700); err != nil {
			t.Fatalf("create database directory: %v", err)
		}
		err := inspectStoragePaths(slog.Default(), dataDir, databasePath)
		if err == nil || !strings.Contains(err.Error(), "must be a regular file") {
			t.Fatalf("inspect error = %v, want regular file error", err)
		}
	})

	t.Run("database symlink", func(t *testing.T) {
		dataDir := t.TempDir()
		target := filepath.Join(dataDir, "target")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatalf("write target: %v", err)
		}
		databasePath := filepath.Join(dataDir, "drove.db")
		if err := os.Symlink(target, databasePath); err != nil {
			t.Fatalf("create database symlink: %v", err)
		}
		err := inspectStoragePaths(slog.Default(), dataDir, databasePath)
		if err == nil || !strings.Contains(err.Error(), "must be a regular file") {
			t.Fatalf("inspect error = %v, want regular file error", err)
		}
	})
}
