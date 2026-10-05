package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
)

const worktreeIncludeFile = ".worktreeinclude"

func (m *Manager) copyIncludedFiles(
	target Workspace,
	paths []string,
) (result error) {
	sourceRoot := target.sourcePath
	if sourceRoot == "" {
		sourceRoot = target.Repository
	}
	source, err := openRealPathRoot(sourceRoot)
	if err != nil {
		return fmt.Errorf("workspace: open include source root: %w", err)
	}
	defer func() {
		result = errors.Join(result, source.Close())
	}()
	destinationBucket, err := m.openManagedBucketRoot(target)
	if err != nil {
		return fmt.Errorf("workspace: open include destination bucket: %w", err)
	}
	defer func() {
		result = errors.Join(result, destinationBucket.Close())
	}()
	destination, err := openRealRootFromRoot(
		destinationBucket,
		target.AgentID,
	)
	if err != nil {
		return fmt.Errorf("workspace: open include destination root: %w", err)
	}
	defer func() {
		result = errors.Join(result, destination.Close())
	}()

	for _, relative := range paths {
		if err := copyIncludedPath(
			source,
			destinationBucket,
			destination,
			target.AgentID,
			relative,
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
	sort.Strings(paths)
	unique := paths[:0]
	for _, path := range paths {
		if len(unique) == 0 || path != unique[len(unique)-1] {
			unique = append(unique, path)
		}
	}
	return unique, nil
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

func copyIncludedPath(
	sourceRoot *os.Root,
	destinationBucket *os.Root,
	destinationRoot *os.Root,
	destinationName string,
	relative string,
) (result error) {
	relative, err := validateIncludedPath(relative)
	if err != nil {
		return err
	}
	if filepath.Base(destinationName) != destinationName ||
		destinationName == "." {
		return fmt.Errorf(
			"destination workspace name %q is invalid",
			destinationName,
		)
	}
	directory := filepath.Dir(relative)
	name := filepath.Base(relative)

	sourceParent, sourceOwned, err := openIncludedParent(
		sourceRoot,
		directory,
		false,
	)
	if err != nil {
		return err
	}
	if sourceOwned {
		defer func() {
			result = errors.Join(result, sourceParent.Close())
		}()
	}
	sourceInfo, err := sourceParent.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect source: %w", err)
	}
	if !sourceInfo.Mode().IsRegular() ||
		sourceInfo.Mode()&os.ModeSymlink != 0 {
		if sourceInfo.Mode()&os.ModeSymlink != 0 {
			return errors.New("symbolic links are unsupported")
		}
		return fmt.Errorf("source mode %s is unsupported", sourceInfo.Mode())
	}
	input, err := sourceParent.Open(name)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer func() {
		result = errors.Join(result, input.Close())
	}()
	openedInfo, err := input.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened source: %w", err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(sourceInfo, openedInfo) {
		return errors.New("source changed while opening")
	}

	temporaryName := "." + destinationName + ".include-" + uuid.NewString()
	output, err := destinationBucket.OpenFile(
		temporaryName,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		sourceInfo.Mode().Perm(),
	)
	if err != nil {
		return fmt.Errorf("create staged destination: %w", err)
	}
	outputOpen := true
	defer func() {
		if outputOpen {
			result = errors.Join(result, output.Close())
		}
		if temporaryName != "" {
			if err := destinationBucket.Remove(temporaryName); !errors.Is(
				err,
				os.ErrNotExist,
			) {
				result = errors.Join(result, err)
			}
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return fmt.Errorf("copy staged contents: %w", err)
	}
	if err := output.Sync(); err != nil {
		return fmt.Errorf("sync staged destination: %w", err)
	}

	parentName, err := ensureIncludedParent(
		destinationBucket,
		destinationName,
		directory,
	)
	if err != nil {
		return err
	}
	destinationParent, err := openRealRootFromRoot(
		destinationBucket,
		parentName,
	)
	if err != nil {
		return fmt.Errorf("open destination parent: %w", err)
	}
	defer func() {
		result = errors.Join(result, destinationParent.Close())
	}()
	if err := verifyRootEntryUnchanged(
		destinationBucket,
		destinationName,
		destinationRoot,
	); err != nil {
		return fmt.Errorf("verify destination workspace: %w", err)
	}
	if err := verifyRootEntryUnchanged(
		destinationBucket,
		parentName,
		destinationParent,
	); err != nil {
		return fmt.Errorf("verify destination parent: %w", err)
	}

	directoryFile, err := destinationBucket.Open(".")
	if err != nil {
		return fmt.Errorf("open destination bucket for install: %w", err)
	}
	targetName := filepath.Join(parentName, name)
	_, renameErr := renameRecordFile(
		directoryFile,
		output,
		temporaryName,
		targetName,
		false,
	)
	if renameErr != nil {
		_ = directoryFile.Close()
		return fmt.Errorf("install destination: %w", renameErr)
	}
	closeErr := output.Close()
	outputOpen = false
	syncErr := syncRecordDirectory(directoryFile)
	directoryCloseErr := directoryFile.Close()
	if err := errors.Join(closeErr, syncErr, directoryCloseErr); err != nil {
		return fmt.Errorf("sync installed destination: %w", err)
	}
	if err := verifyRootEntryUnchanged(
		destinationBucket,
		destinationName,
		destinationRoot,
	); err != nil {
		return fmt.Errorf("reverify destination workspace: %w", err)
	}
	if err := verifyRootEntryUnchanged(
		destinationBucket,
		parentName,
		destinationParent,
	); err != nil {
		return fmt.Errorf("reverify destination parent: %w", err)
	}
	return nil
}

func ensureIncludedParent(
	root *os.Root,
	destinationName string,
	directory string,
) (string, error) {
	current := destinationName
	for _, component := range strings.Split(
		directory,
		string(filepath.Separator),
	) {
		if component == "." || component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := root.Lstat(current)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := root.Mkdir(current, 0o700); err != nil &&
				!errors.Is(err, os.ErrExist) {
				return "", fmt.Errorf(
					"create destination directory %q: %w",
					current,
					err,
				)
			}
			info, err = root.Lstat(current)
			if err != nil {
				return "", fmt.Errorf(
					"inspect created destination directory %q: %w",
					current,
					err,
				)
			}
		case err != nil:
			return "", fmt.Errorf(
				"inspect destination directory %q: %w",
				current,
				err,
			)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf(
				"destination parent %q is not a real directory",
				current,
			)
		}
	}
	return current, nil
}

func openIncludedParent(
	root *os.Root,
	directory string,
	create bool,
) (_ *os.Root, _ bool, result error) {
	current := root
	currentOwned := false
	defer func() {
		if result != nil && currentOwned {
			result = errors.Join(result, current.Close())
		}
	}()
	for _, component := range strings.Split(
		directory,
		string(filepath.Separator),
	) {
		if component == "." || component == "" {
			continue
		}
		info, err := current.Lstat(component)
		switch {
		case errors.Is(err, os.ErrNotExist) && create:
			if err := current.Mkdir(component, 0o700); err != nil &&
				!errors.Is(err, os.ErrExist) {
				return nil, false, fmt.Errorf(
					"create destination directory %q: %w",
					component,
					err,
				)
			}
			info, err = current.Lstat(component)
			if err != nil {
				return nil, false, fmt.Errorf(
					"inspect created destination directory %q: %w",
					component,
					err,
				)
			}
		case err != nil:
			return nil, false, fmt.Errorf(
				"inspect included directory %q: %w",
				component,
				err,
			)
		case !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
			return nil, false, fmt.Errorf(
				"included parent %q is not a real directory",
				component,
			)
		}
		next, err := openRealRootFromRoot(current, component)
		if err != nil {
			return nil, false, fmt.Errorf(
				"open included directory %q: %w",
				component,
				err,
			)
		}
		if currentOwned {
			if err := current.Close(); err != nil {
				_ = next.Close()
				return nil, false, fmt.Errorf(
					"close included directory %q: %w",
					component,
					err,
				)
			}
		}
		current = next
		currentOwned = true
	}
	return current, currentOwned, nil
}
