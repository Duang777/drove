package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const worktreeIncludeFile = ".worktreeinclude"

func (m *Manager) copyIncludedFiles(ctx context.Context, target Workspace) error {
	sourceRoot := target.sourcePath
	if sourceRoot == "" {
		sourceRoot = target.Repository
	}
	paths, err := m.includedPaths(ctx, sourceRoot)
	if err != nil {
		return err
	}
	for _, relative := range paths {
		if err := copyIncludedPath(
			filepath.Join(sourceRoot, relative),
			filepath.Join(target.Path, relative),
			target.Path,
		); err != nil {
			return fmt.Errorf("workspace: copy included path %q: %w", relative, err)
		}
	}
	return nil
}

func (m *Manager) includedPaths(
	ctx context.Context,
	sourceRoot string,
) ([]string, error) {
	includePath := filepath.Join(sourceRoot, worktreeIncludeFile)
	info, err := os.Lstat(includePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf(
			"workspace: inspect %s: %w",
			worktreeIncludeFile,
			err,
		)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf(
			"workspace: %s must be a regular file",
			worktreeIncludeFile,
		)
	}

	output, err := m.run(
		ctx,
		"-C",
		sourceRoot,
		"ls-files",
		"--others",
		"--ignored",
		"--full-name",
		"-z",
		"--exclude-from="+worktreeIncludeFile,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"workspace: evaluate %s: %w",
			worktreeIncludeFile,
			err,
		)
	}
	var paths []string
	for _, rawPath := range strings.Split(string(output), "\x00") {
		if rawPath == "" {
			continue
		}
		relative, err := validateIncludedPath(rawPath)
		if err != nil {
			return nil, err
		}
		paths = append(paths, relative)
	}
	return paths, nil
}

func validateIncludedPath(path string) (string, error) {
	path = filepath.FromSlash(path)
	clean := filepath.Clean(path)
	if clean == "." ||
		filepath.IsAbs(clean) ||
		clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workspace: invalid included path %q", path)
	}
	return clean, nil
}

func copyIncludedPath(source string, destination string, root string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if err := ensureSafeDestinationParent(root, destination); err != nil {
		return err
	}

	switch {
	case info.Mode().IsRegular():
		return copyIncludedFile(source, destination, info.Mode().Perm())
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(source)
		if err != nil {
			return fmt.Errorf("read symlink: %w", err)
		}
		if err := os.Symlink(target, destination); err != nil {
			return fmt.Errorf("create symlink: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("source mode %s is unsupported", info.Mode())
	}
}

func ensureSafeDestinationParent(root string, destination string) error {
	relative, err := filepath.Rel(root, destination)
	if err != nil {
		return fmt.Errorf("resolve destination path: %w", err)
	}
	relative, err = validateIncludedPath(relative)
	if err != nil {
		return err
	}

	current := root
	for _, component := range strings.Split(
		filepath.Dir(relative),
		string(filepath.Separator),
	) {
		if component == "." || component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := os.Mkdir(current, 0o700); err != nil {
				return fmt.Errorf(
					"create destination directory %q: %w",
					current,
					err,
				)
			}
		case err != nil:
			return fmt.Errorf(
				"inspect destination directory %q: %w",
				current,
				err,
			)
		case !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf(
				"destination parent %q is not a real directory",
				current,
			)
		}
	}
	return nil
}

func copyIncludedFile(source string, destination string, mode os.FileMode) (result error) {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer func() {
		result = errors.Join(result, input.Close())
	}()

	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}
	defer func() {
		result = errors.Join(result, output.Close())
	}()

	if _, err := io.Copy(output, input); err != nil {
		return fmt.Errorf("copy contents: %w", err)
	}
	return nil
}
