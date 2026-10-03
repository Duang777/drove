package daemon

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
)

func inspectStoragePaths(log *slog.Logger, dataDir, databasePath string) error {
	if err := inspectStoragePath(log, dataDir, true); err != nil {
		return err
	}
	if err := inspectStoragePath(log, databasePath, false); err != nil {
		return err
	}
	return nil
}

func inspectStoragePath(log *slog.Logger, path string, directory bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && !directory {
		return nil
	}
	if err != nil {
		return fmt.Errorf("daemon: inspect storage path %q: %w", path, err)
	}
	if directory {
		if !info.IsDir() {
			return fmt.Errorf("daemon: data path %q must be a directory", path)
		}
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("daemon: database path %q must be a regular file", path)
	}
	if permissions := info.Mode().Perm(); permissions&0o077 != 0 {
		log.Warn(
			"storage path grants group or other permissions",
			"path",
			path,
			"mode",
			fmt.Sprintf("%04o", permissions),
		)
	}
	return nil
}
