//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package workspace

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

func bsdPreparedWorktreeStageName(worktreePath string) (string, error) {
	if !filepath.IsAbs(worktreePath) ||
		filepath.Clean(worktreePath) != worktreePath {
		return "", errors.New(
			"workspace: prepared worktree path is not a clean absolute path",
		)
	}
	base := filepath.Base(worktreePath)
	if base == "." || base == string(filepath.Separator) {
		return "", errors.New("workspace: prepared worktree name is invalid")
	}
	name := base
	if len(name) > 200 {
		return "", errors.New("workspace: prepared worktree name is too long")
	}
	return name, nil
}

func validateBSDPreparedWorktreeNamesAvailable(
	commonRoot *os.Root,
	stageName string,
) (result error) {
	if debris, err := findRootDeletionDebris(
		commonRoot,
		stageName,
		"",
	); err != nil {
		return err
	} else if debris != "" {
		return fmt.Errorf(
			"workspace: prepared registration has deletion debris %q",
			debris,
		)
	}
	if _, err := commonRoot.Lstat(stageName); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return fmt.Errorf(
				"workspace: prepared registration stage %q already exists",
				stageName,
			)
		}
		return err
	}
	worktrees, err := openRealRootFromRoot(commonRoot, "worktrees")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, worktrees.Close())
	}()
	entries, err := readRootDirectory(worktrees)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == stageName ||
			strings.HasPrefix(entry.Name(), stageName) {
			return fmt.Errorf(
				"workspace: prepared private Git directory %q already exists",
				entry.Name(),
			)
		}
	}
	return nil
}

func finalizePreparedWorktreeAdd(
	ctx context.Context,
	repository repositoryCapability,
	worktreePath string,
	worktreeRoot *os.Root,
	commandSucceeded bool,
) (result error) {
	stageName, err := bsdPreparedWorktreeStageName(worktreePath)
	if err != nil {
		return err
	}
	if !commandSucceeded {
		return cleanupBSDPreparedWorktreeStage(
			ctx,
			repository,
			stageName,
			"",
		)
	}
	if err := errors.Join(
		repository.verifyBinding(ctx),
		verifyRealPathRoot(worktreePath, worktreeRoot),
	); err != nil {
		return errors.Join(
			err,
			cleanupBSDPreparedWorktreeStage(
				ctx,
				repository,
				stageName,
				"",
			),
		)
	}
	privatePath := filepath.Join(
		repository.commonPath,
		"worktrees",
		stageName,
	)
	internalPath := filepath.Join(repository.commonPath, stageName)
	stageRoot, err := openRealRootFromRoot(repository.commonRoot, stageName)
	if err != nil {
		return fmt.Errorf(
			"workspace: open prepared registration stage: %w",
			err,
		)
	}
	stageOwned := true
	defer func() {
		if stageOwned {
			result = errors.Join(result, stageRoot.Close())
		}
	}()
	if err := validateBoundGitPointer(
		stageRoot,
		".git",
		"gitdir: "+privatePath+"\n",
	); err != nil {
		return fmt.Errorf(
			"workspace: validate prepared registration stage: %w",
			err,
		)
	}
	privateRoot, err := openRealRootFromRoot(
		repository.commonRoot,
		filepath.Join("worktrees", stageName),
	)
	if err != nil {
		return fmt.Errorf(
			"workspace: open prepared private Git directory: %w",
			err,
		)
	}
	defer func() {
		result = errors.Join(result, privateRoot.Close())
	}()
	if err := validateBSDPrivateAdminSet(
		repository.commonRoot,
		stageName,
	); err != nil {
		return err
	}
	if err := ensureBoundGitPointer(
		worktreeRoot,
		".git",
		boundGitPointerTempPrefix(stageName, ".git"),
		"gitdir: "+privatePath+"\n",
		"",
	); err != nil {
		return fmt.Errorf(
			"workspace: install prepared worktree Git pointer: %w",
			err,
		)
	}
	if err := ensureBoundGitPointer(
		privateRoot,
		"gitdir",
		boundGitPointerTempPrefix(stageName, "gitdir"),
		worktreePath+string(filepath.Separator)+".git\n",
		internalPath+string(filepath.Separator)+".git\n",
	); err != nil {
		return fmt.Errorf(
			"workspace: rebind prepared private Git directory: %w",
			err,
		)
	}
	_, exists, err := repository.worktreeRegistration(
		ctx,
		worktreePath,
	)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New(
			"workspace: relocated prepared worktree registration is missing",
		)
	}
	if err := validateBSDStageContents(stageRoot); err != nil {
		return err
	}
	if err := removeRootDeletionDebris(
		repository.commonRoot,
		stageName,
	); err != nil {
		return err
	}
	stageOwned = false
	if err := removeOpenedDirectoryFromRoot(
		repository.commonRoot,
		stageName,
		stageRoot,
	); err != nil {
		return fmt.Errorf(
			"workspace: remove prepared registration stage: %w",
			err,
		)
	}
	return errors.Join(
		repository.verifyBinding(ctx),
		verifyRealPathRoot(worktreePath, worktreeRoot),
	)
}

func repairBoundPreparedWorktreeRegistration(
	ctx context.Context,
	target Workspace,
	prepared *preparedWorktreeTarget,
	repository repositoryCapability,
	stalePath string,
) (handled bool, result error) {
	stageName, err := bsdPreparedWorktreeStageName(stalePath)
	if err != nil {
		return true, err
	}
	privatePath := filepath.Join(
		repository.commonPath,
		"worktrees",
		stageName,
	)
	if prepared.gitDirectory != privatePath {
		return true, errors.New(
			"workspace: prepared private Git directory does not match its operation",
		)
	}
	if err := errors.Join(
		repository.verifyBinding(ctx),
		verifyRealPathRoot(target.Path, prepared.root),
	); err != nil {
		return true, err
	}
	privateRoot, err := openRealRootFromRoot(
		repository.commonRoot,
		filepath.Join("worktrees", stageName),
	)
	if err != nil {
		return true, err
	}
	defer func() {
		result = errors.Join(result, privateRoot.Close())
	}()
	if err := validateBoundGitPointer(
		prepared.root,
		".git",
		"gitdir: "+privatePath+"\n",
	); err != nil {
		return true, err
	}
	if err := ensureBoundGitPointer(
		privateRoot,
		"gitdir",
		boundGitPointerTempPrefix(stageName, "gitdir"),
		target.Path+string(filepath.Separator)+".git\n",
		stalePath+string(filepath.Separator)+".git\n",
	); err != nil {
		return true, err
	}
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
	if err := repository.verifyPreparedWorktree(
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
	)
}

func cleanupPreparedWorktreeAddDebris(
	ctx context.Context,
	repository repositoryCapability,
	target Workspace,
	record workspaceRecord,
) error {
	if record.BranchOperationID == "" {
		return nil
	}
	stagingName, err := preparedWorktreeStagingName(
		target.AgentID,
		record.BranchOperationID,
	)
	if err != nil {
		return err
	}
	stageName, err := bsdPreparedWorktreeStageName(
		filepath.Join(filepath.Dir(target.Path), stagingName),
	)
	if err != nil {
		return err
	}
	return cleanupBSDPreparedWorktreeStage(
		ctx,
		repository,
		stageName,
		target.Path,
	)
}

func cleanupBSDPreparedWorktreeStage(
	ctx context.Context,
	repository repositoryCapability,
	stageName string,
	allowedTargetPath string,
) (result error) {
	if err := removeRootDeletionDebris(
		repository.commonRoot,
		stageName,
	); err != nil {
		return err
	}
	stageRoot, err := openRealRootFromRoot(repository.commonRoot, stageName)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	stageOwned := true
	defer func() {
		if stageOwned {
			result = errors.Join(result, stageRoot.Close())
		}
	}()
	privatePath := filepath.Join(
		repository.commonPath,
		"worktrees",
		stageName,
	)
	if err := validateBoundGitPointer(
		stageRoot,
		".git",
		"gitdir: "+privatePath+"\n",
	); err != nil {
		return err
	}
	privateRoot, err := openRealRootFromRoot(
		repository.commonRoot,
		filepath.Join("worktrees", stageName),
	)
	if err != nil {
		return err
	}
	adminTarget, err := readBoundRegularFile(privateRoot, "gitdir")
	closeErr := privateRoot.Close()
	if err := errors.Join(err, closeErr); err != nil {
		return err
	}
	internalTarget := filepath.Join(
		repository.commonPath,
		stageName,
		".git",
	) + "\n"
	allowedTarget := ""
	if allowedTargetPath != "" {
		allowedTarget = filepath.Join(allowedTargetPath, ".git") + "\n"
	}
	if adminTarget != internalTarget && adminTarget != allowedTarget {
		return errors.New(
			"workspace: prepared private Git directory has an unexpected target",
		)
	}
	if err := validateBSDStageContents(stageRoot); err != nil {
		return err
	}
	stageOwned = false
	if err := removeOpenedDirectoryFromRoot(
		repository.commonRoot,
		stageName,
		stageRoot,
	); err != nil {
		return err
	}
	return repository.pruneWorktrees(ctx)
}

func validateBSDPrivateAdminSet(
	commonRoot *os.Root,
	stageName string,
) (result error) {
	worktrees, err := openRealRootFromRoot(commonRoot, "worktrees")
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, worktrees.Close())
	}()
	entries, err := readRootDirectory(worktrees)
	if err != nil {
		return err
	}
	found := false
	for _, entry := range entries {
		if entry.Name() == stageName {
			found = true
			continue
		}
		if strings.HasPrefix(entry.Name(), stageName) {
			return fmt.Errorf(
				"workspace: Git selected unexpected private directory %q",
				entry.Name(),
			)
		}
	}
	if !found {
		return errors.New(
			"workspace: expected prepared private Git directory is missing",
		)
	}
	return nil
}

func validateBSDStageContents(stageRoot *os.Root) error {
	if err := recoverRecordRenameDebris(stageRoot); err != nil {
		return err
	}
	entries, err := readRootDirectory(stageRoot)
	if err != nil {
		return err
	}
	if len(entries) != 1 || entries[0].Name() != ".git" {
		return errors.New(
			"workspace: prepared registration stage contains unexpected entries",
		)
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
