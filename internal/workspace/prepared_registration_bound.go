//go:build linux || windows

package workspace

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/google/uuid"
)

func finalizePreparedWorktreeAdd(
	_ context.Context,
	_ repositoryCapability,
	_ string,
	_ *os.Root,
	_ bool,
) error {
	return nil
}

func repairBoundPreparedWorktreeRegistration(
	ctx context.Context,
	target Workspace,
	prepared *preparedWorktreeTarget,
	repository repositoryCapability,
	stalePath string,
) (handled bool, result error) {
	if err := errors.Join(
		repository.verifyBinding(ctx),
		verifyRealPathRoot(target.Path, prepared.root),
	); err != nil {
		return true, err
	}
	relativeGitDirectory, err := repository.boundGitDirectory(
		prepared.gitDirectory,
		target.AgentID,
	)
	if err != nil {
		return true, err
	}
	gitRoot, err := openRealRootFromRoot(
		repository.commonRoot,
		relativeGitDirectory,
	)
	if err != nil {
		return true, fmt.Errorf(
			"workspace: open prepared private Git directory: %w",
			err,
		)
	}
	defer func() {
		result = errors.Join(result, gitRoot.Close())
	}()
	if err := validateGitPointerTarget(
		prepared.root,
		".git",
		"gitdir: ",
		prepared.gitDirectory,
	); err != nil {
		return true, fmt.Errorf(
			"workspace: validate prepared worktree Git pointer: %w",
			err,
		)
	}
	oldPointer, err := readBoundRegularFile(gitRoot, "gitdir")
	if err != nil {
		return true, err
	}
	if err := validateGitPointerContent(
		oldPointer,
		"",
		filepath.Join(stalePath, ".git"),
	); err != nil {
		return true, fmt.Errorf(
			"workspace: validate prepared private Git pointer: %w",
			err,
		)
	}
	newPointer := filepath.ToSlash(filepath.Join(target.Path, ".git")) + "\n"
	if err := ensureBoundGitPointer(
		gitRoot,
		"gitdir",
		boundGitPointerTempPrefix(
			filepath.Base(relativeGitDirectory),
			"gitdir",
		),
		newPointer,
		oldPointer,
	); err != nil {
		return true, fmt.Errorf(
			"workspace: rebind prepared private Git directory: %w",
			err,
		)
	}
	if err := validateGitPointerTarget(
		prepared.root,
		".git",
		"gitdir: ",
		prepared.gitDirectory,
	); err != nil {
		return true, fmt.Errorf(
			"workspace: revalidate prepared worktree Git pointer: %w",
			err,
		)
	}
	privateRepository := repository.withPrivateGitBinding(
		prepared.gitDirectory,
		gitRoot,
	)
	registered, exists, err := repository.worktreeRegistration(
		ctx,
		target.Path,
	)
	if err != nil {
		return true, err
	}
	if !exists {
		return true, errors.New(
			"workspace: promoted worktree registration is missing",
		)
	}
	if err := privateRepository.verifyPreparedWorktree(
		ctx,
		target,
		prepared.gitDirectory,
		registered,
	); err != nil {
		return true, err
	}
	return true, errors.Join(
		repository.verifyBinding(ctx),
		verifyRealPathRoot(target.Path, prepared.root),
		verifyRealPathRoot(prepared.gitDirectory, gitRoot),
	)
}

func cleanupPreparedWorktreeAddDebris(
	_ context.Context,
	_ repositoryCapability,
	_ Workspace,
	_ workspaceRecord,
) error {
	return nil
}

func validateGitPointerTarget(
	root *os.Root,
	name string,
	prefix string,
	expectedPath string,
) error {
	content, err := readBoundRegularFile(root, name)
	if err != nil {
		return err
	}
	return validateGitPointerContent(content, prefix, expectedPath)
}

func validateGitPointerContent(
	content string,
	prefix string,
	expectedPath string,
) error {
	value, found := strings.CutPrefix(content, prefix)
	if !found || !strings.HasSuffix(value, "\n") {
		return errors.New("workspace: Git pointer has invalid contents")
	}
	value = strings.TrimSuffix(value, "\n")
	if value == "" {
		return errors.New("workspace: Git pointer has invalid contents")
	}
	actual := filepath.Clean(filepath.FromSlash(value))
	expected := filepath.Clean(expectedPath)
	matches := actual == expected
	if runtime.GOOS == "windows" {
		matches = strings.EqualFold(actual, expected)
	}
	if !matches {
		return errors.New("workspace: Git pointer has an unexpected target")
	}
	return nil
}

func boundGitPointerTempPrefix(stageName string, targetName string) string {
	digest := sha256.Sum256([]byte(stageName + "\x00" + targetName))
	return fmt.Sprintf(".drove-git-pointer-%x-", digest[:8])
}

func ensureBoundGitPointer(
	root *os.Root,
	targetName string,
	tempPrefix string,
	content string,
	allowedOld string,
) (result error) {
	if err := recoverRecordRenameDebris(root); err != nil {
		return err
	}
	newPrefix := tempPrefix + "new-"
	oldPrefix := tempPrefix + "old-"
	if err := validateBoundGitPointerNamespace(
		root,
		tempPrefix,
		newPrefix,
		oldPrefix,
	); err != nil {
		return err
	}
	current, exists, err := readOptionalBoundRegularFile(root, targetName)
	if err != nil {
		return err
	}
	_, oldFile, err := findBoundGitPointerTemp(
		root,
		oldPrefix,
		allowedOld,
	)
	if err != nil {
		return err
	}
	if oldFile != nil {
		defer func() {
			result = errors.Join(result, oldFile.Close())
		}()
	}
	if exists && current == content {
		return errors.Join(
			cleanupBoundGitPointerTemps(root, newPrefix, content),
			cleanupBoundGitPointerTemps(root, oldPrefix, allowedOld),
		)
	}
	switch {
	case exists && current != allowedOld:
		return fmt.Errorf(
			"workspace: Git pointer %q has unexpected contents",
			targetName,
		)
	case allowedOld == "" && oldFile != nil:
		return fmt.Errorf(
			"workspace: Git pointer %q has an unexpected backup",
			targetName,
		)
	case !exists && allowedOld != "" && oldFile == nil:
		return fmt.Errorf(
			"workspace: Git pointer %q is missing",
			targetName,
		)
	}
	if exists {
		if oldFile == nil {
			if err := isolateBoundGitPointer(
				root,
				targetName,
				oldPrefix+uuid.NewString(),
				allowedOld,
				nil,
			); err != nil {
				return err
			}
		} else {
			currentFile, err := openBoundRegularFile(root, targetName)
			if err != nil {
				return err
			}
			currentInfo, currentErr := currentFile.Stat()
			oldInfo, oldErr := oldFile.Stat()
			if err := errors.Join(currentErr, oldErr); err != nil {
				return errors.Join(err, currentFile.Close())
			}
			if !os.SameFile(currentInfo, oldInfo) {
				return errors.Join(
					fmt.Errorf(
						"workspace: Git pointer %q conflicts with its backup",
						targetName,
					),
					currentFile.Close(),
				)
			}
			removeErr := removeOwnedRecordPath(
				root,
				targetName,
				currentFile,
				oldPrefix+uuid.NewString(),
				func(candidate *os.File) error {
					info, err := candidate.Stat()
					if err != nil {
						return err
					}
					if !os.SameFile(info, oldInfo) {
						return errors.New(
							"workspace: Git pointer changed before isolation",
						)
					}
					return nil
				},
				nil,
			)
			closeErr := currentFile.Close()
			if err := errors.Join(removeErr, closeErr); err != nil {
				return err
			}
		}
	}
	tempName, tempFile, err := findBoundGitPointerTemp(
		root,
		newPrefix,
		content,
	)
	if err != nil {
		return err
	}
	if tempFile == nil {
		tempName = newPrefix + uuid.NewString()
		tempFile, err = root.OpenFile(
			tempName,
			os.O_RDWR|os.O_CREATE|os.O_EXCL,
			0o600,
		)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(tempFile, content); err != nil {
			return errors.Join(err, tempFile.Close())
		}
		if err := tempFile.Sync(); err != nil {
			return errors.Join(err, tempFile.Close())
		}
	}
	defer func() {
		result = errors.Join(result, tempFile.Close())
	}()
	directory, err := openRecordDirectory(root)
	if err != nil {
		return err
	}
	installed, renameErr := renameRecordFile(
		directory,
		tempFile,
		tempName,
		targetName,
		false,
	)
	syncErr := syncRecordDirectory(directory)
	closeErr := directory.Close()
	if err := errors.Join(renameErr, syncErr, closeErr); err != nil {
		return err
	}
	if !installed {
		return fmt.Errorf(
			"workspace: Git pointer %q was not installed",
			targetName,
		)
	}
	if err := validateBoundGitPointer(root, targetName, content); err != nil {
		return err
	}
	return errors.Join(
		cleanupBoundGitPointerTemps(root, newPrefix, content),
		cleanupBoundGitPointerTemps(root, oldPrefix, allowedOld),
	)
}

func isolateBoundGitPointer(
	root *os.Root,
	targetName string,
	isolatedName string,
	expected string,
	afterValidation func(),
) (result error) {
	file, err := openBoundRegularFile(root, targetName)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, file.Close())
	}()
	raw, err := readOpenedBoundFile(file)
	if err != nil {
		return err
	}
	if raw != expected {
		return fmt.Errorf(
			"workspace: Git pointer %q changed contents",
			targetName,
		)
	}
	if afterValidation != nil {
		afterValidation()
	}
	directory, err := openRecordDirectory(root)
	if err != nil {
		return err
	}
	moved, moveErr := moveRecordFile(
		directory,
		file,
		targetName,
		isolatedName,
	)
	syncErr := syncRecordDirectory(directory)
	closeErr := directory.Close()
	if err := errors.Join(moveErr, syncErr, closeErr); err != nil {
		return err
	}
	if !moved {
		return fmt.Errorf(
			"workspace: Git pointer %q was not isolated",
			targetName,
		)
	}
	return nil
}

func validateBoundGitPointerNamespace(
	root *os.Root,
	basePrefix string,
	newPrefix string,
	oldPrefix string,
) error {
	entries, err := readRootDirectory(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), basePrefix) {
			continue
		}
		prefix := ""
		switch {
		case strings.HasPrefix(entry.Name(), newPrefix):
			prefix = newPrefix
		case strings.HasPrefix(entry.Name(), oldPrefix):
			prefix = oldPrefix
		}
		if prefix == "" ||
			!canonicalUUID(strings.TrimPrefix(entry.Name(), prefix)) {
			return fmt.Errorf(
				"workspace: Git pointer artifact %q has an invalid name",
				entry.Name(),
			)
		}
	}
	return nil
}

func findBoundGitPointerTemp(
	root *os.Root,
	prefix string,
	content string,
) (string, *os.File, error) {
	entries, err := readRootDirectory(root)
	if err != nil {
		return "", nil, err
	}
	var foundName string
	var found *os.File
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		suffix := strings.TrimPrefix(entry.Name(), prefix)
		if !canonicalUUID(suffix) {
			var closeErr error
			if found != nil {
				closeErr = found.Close()
			}
			return "", nil, errors.Join(
				fmt.Errorf(
					"workspace: Git pointer temporary %q has an invalid name",
					entry.Name(),
				),
				closeErr,
			)
		}
		file, err := openBoundRegularFile(root, entry.Name())
		if err != nil {
			if found != nil {
				err = errors.Join(err, found.Close())
			}
			return "", nil, err
		}
		raw, err := readOpenedBoundFile(file)
		if err != nil || raw != content {
			closeErr := file.Close()
			if found != nil {
				closeErr = errors.Join(closeErr, found.Close())
			}
			if err == nil {
				err = errors.New(
					"workspace: Git pointer temporary has unexpected contents",
				)
			}
			return "", nil, errors.Join(err, closeErr)
		}
		if found == nil {
			foundName = entry.Name()
			found = file
			continue
		}
		if err := file.Close(); err != nil {
			return "", nil, errors.Join(err, found.Close())
		}
	}
	return foundName, found, nil
}

func cleanupBoundGitPointerTemps(
	root *os.Root,
	prefix string,
	content string,
) error {
	for {
		name, file, err := findBoundGitPointerTemp(root, prefix, content)
		if err != nil || file == nil {
			return err
		}
		removeErr := removeOwnedRecordPath(
			root,
			name,
			file,
			prefix+uuid.NewString(),
			func(candidate *os.File) error {
				raw, err := readOpenedBoundFile(candidate)
				if err != nil {
					return err
				}
				if raw != content {
					return errors.New(
						"workspace: Git pointer temporary changed contents",
					)
				}
				return nil
			},
			nil,
		)
		closeErr := file.Close()
		if err := errors.Join(removeErr, closeErr); err != nil {
			return err
		}
	}
}

func validateBoundGitPointer(
	root *os.Root,
	name string,
	expected string,
) error {
	raw, err := readBoundRegularFile(root, name)
	if err != nil {
		return err
	}
	if raw != expected {
		return fmt.Errorf(
			"workspace: Git pointer %q has unexpected contents",
			name,
		)
	}
	return nil
}

func readOptionalBoundRegularFile(
	root *os.Root,
	name string,
) (string, bool, error) {
	_, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	raw, err := readBoundRegularFile(root, name)
	return raw, err == nil, err
}

func readBoundRegularFile(root *os.Root, name string) (result string, err error) {
	file, err := openBoundRegularFile(root, name)
	if err != nil {
		return "", err
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()
	return readOpenedBoundFile(file)
}

func openBoundRegularFile(root *os.Root, name string) (*os.File, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("workspace: %q is not a regular file", name)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errors.Join(
			fmt.Errorf("workspace: %q changed while opening", name),
			file.Close(),
		)
	}
	return file, nil
}

func readOpenedBoundFile(file *os.File) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return "", err
	}
	if len(raw) > 4096 {
		return "", errors.New("workspace: Git pointer is too large")
	}
	return string(raw), nil
}
