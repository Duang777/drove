package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"
)

const (
	worktreeIncludeFile    = ".worktreeinclude"
	maxWorktreeIncludeSize = 1024 * 1024
)

type includeSelection struct {
	paths    []string
	manifest []byte
	exists   bool
}

func (m *Manager) copyIncludedSelection(
	ctx context.Context,
	sourcePath string,
	sourceRoot *os.Root,
	repository repositoryCapability,
	target Workspace,
	selection includeSelection,
) ([]string, error) {
	if err := m.verifyIncludeSelection(
		ctx,
		sourcePath,
		sourceRoot,
		repository,
		selection,
	); err != nil {
		return nil, err
	}
	copiedPaths, err := m.copyIncludedFiles(
		ctx,
		sourceRoot,
		target,
		selection.paths,
	)
	if err != nil {
		return nil, err
	}
	if err := m.verifyIncludeSelection(
		ctx,
		sourcePath,
		sourceRoot,
		repository,
		selection,
	); err != nil {
		return nil, err
	}
	return copiedPaths, nil
}

func (m *Manager) copyIncludedFiles(
	ctx context.Context,
	source *os.Root,
	target Workspace,
	paths []string,
) (_ []string, result error) {
	destinationBucket, err := m.openManagedBucketRoot(target)
	if err != nil {
		return nil, fmt.Errorf(
			"workspace: open include destination bucket: %w",
			err,
		)
	}
	defer func() {
		result = errors.Join(result, destinationBucket.Close())
	}()
	destination, err := openRealRootFromRoot(
		destinationBucket,
		target.AgentID,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"workspace: open include destination root: %w",
			err,
		)
	}
	defer func() {
		result = errors.Join(result, destination.Close())
	}()
	if target.directoryIdentity == "" {
		return nil, errors.New(
			"workspace: include destination identity is unavailable",
		)
	}
	destinationIdentity, err := openedDirectoryIdentity(destination)
	if err != nil {
		return nil, fmt.Errorf(
			"workspace: inspect include destination identity: %w",
			err,
		)
	}
	if destinationIdentity != target.directoryIdentity {
		return nil, errors.New(
			"workspace: include destination identity changed",
		)
	}
	if err := verifyRootEntryUnchanged(
		destinationBucket,
		target.AgentID,
		destination,
	); err != nil {
		return nil, fmt.Errorf(
			"workspace: verify include destination: %w",
			err,
		)
	}

	copiedPaths := make([]string, 0, len(paths))
	for _, relative := range paths {
		tracked, err := m.worktreePathTracked(
			ctx,
			target.Path,
			destination,
			relative,
		)
		if err != nil {
			return nil, err
		}
		if tracked {
			continue
		}
		if err := copyIncludedPath(
			source,
			destinationBucket,
			destination,
			target.AgentID,
			relative,
		); err != nil {
			return nil, fmt.Errorf(
				"workspace: copy included path %q: %w",
				relative,
				err,
			)
		}
		copiedPaths = append(copiedPaths, relative)
	}
	destinationIdentity, err = openedDirectoryIdentity(destination)
	if err != nil {
		return nil, fmt.Errorf(
			"workspace: reinspect include destination identity: %w",
			err,
		)
	}
	if destinationIdentity != target.directoryIdentity {
		return nil, errors.New(
			"workspace: include destination identity changed while copying",
		)
	}
	if err := verifyRootEntryUnchanged(
		destinationBucket,
		target.AgentID,
		destination,
	); err != nil {
		return nil, fmt.Errorf(
			"workspace: reverify include destination: %w",
			err,
		)
	}
	return copiedPaths, nil
}

func (m *Manager) worktreePathTracked(
	ctx context.Context,
	path string,
	root *os.Root,
	relative string,
) (bool, error) {
	_, err := m.runRootedGit(
		ctx,
		path,
		root,
		"--literal-pathspecs",
		"ls-files",
		"--error-unmatch",
		"--",
		filepath.ToSlash(relative),
	)
	switch {
	case err == nil:
		return true, nil
	case isExitCode(err, 1):
		return false, nil
	default:
		return false, fmt.Errorf(
			"workspace: inspect target include path %q: %w",
			relative,
			err,
		)
	}
}

func (m *Manager) includedPaths(
	ctx context.Context,
	sourcePath string,
	sourceRoot *os.Root,
	repository repositoryCapability,
) ([]string, error) {
	selection, err := m.selectIncludedPaths(
		ctx,
		sourcePath,
		sourceRoot,
		repository,
	)
	return selection.paths, err
}

func (m *Manager) selectIncludedPaths(
	ctx context.Context,
	sourcePath string,
	sourceRoot *os.Root,
	repository repositoryCapability,
) (includeSelection, error) {
	manifest, exists, err := readWorktreeInclude(sourceRoot)
	if err != nil {
		return includeSelection{}, err
	}
	if !exists {
		return includeSelection{}, nil
	}
	paths, err := m.evaluateIncludedPaths(
		ctx,
		sourcePath,
		sourceRoot,
		repository,
		manifest,
	)
	if err != nil {
		return includeSelection{}, err
	}
	return includeSelection{
		paths:    paths,
		manifest: manifest,
		exists:   true,
	}, nil
}

func (m *Manager) evaluateIncludedPaths(
	ctx context.Context,
	sourcePath string,
	sourceRoot *os.Root,
	repository repositoryCapability,
	manifest []byte,
) ([]string, error) {
	output, err := m.runIncludeManifest(
		ctx,
		sourcePath,
		sourceRoot,
		repository,
		manifest,
		"ls-files",
		"--others",
		"--ignored",
		"--full-name",
		"-z",
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

func (m *Manager) verifyIncludeSelection(
	ctx context.Context,
	sourcePath string,
	sourceRoot *os.Root,
	repository repositoryCapability,
	selection includeSelection,
) error {
	if !selection.exists {
		return nil
	}
	current, err := m.evaluateIncludedPaths(
		ctx,
		sourcePath,
		sourceRoot,
		repository,
		selection.manifest,
	)
	if err != nil {
		return err
	}
	if !slices.Equal(current, selection.paths) {
		return errors.New(
			"workspace: included paths changed while preparing worktree",
		)
	}
	return nil
}

func (m *Manager) runIncludeManifest(
	ctx context.Context,
	sourcePath string,
	sourceRoot *os.Root,
	repository repositoryCapability,
	manifest []byte,
	arguments ...string,
) (_ []byte, result error) {
	directory, err := os.MkdirTemp("", "drove-worktreeinclude-")
	if err != nil {
		return nil, fmt.Errorf("workspace: create include rule directory: %w", err)
	}
	root, err := openRealPathRoot(directory)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("workspace: open include rule directory: %w", err),
			os.Remove(directory),
		)
	}
	defer func() {
		verifyErr := verifyRealPathRoot(directory, root)
		closeErr := root.Close()
		var removeErr error
		if verifyErr == nil {
			removeErr = os.Remove(directory)
		}
		result = errors.Join(result, verifyErr, closeErr, removeErr)
	}()

	name := "." + worktreeIncludeFile + "-" + uuid.NewString()
	writer, err := root.OpenFile(
		name,
		os.O_RDWR|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		return nil, fmt.Errorf("workspace: create include rule file: %w", err)
	}
	if _, err := writer.Write(manifest); err != nil {
		return nil, errors.Join(
			fmt.Errorf("workspace: write include rule file: %w", err),
			removeRecordPathIfSame(root, name, writer),
			writer.Close(),
		)
	}
	if err := writer.Sync(); err != nil {
		return nil, errors.Join(
			fmt.Errorf("workspace: sync include rule file: %w", err),
			removeRecordPathIfSame(root, name, writer),
			writer.Close(),
		)
	}
	reader, err := root.Open(name)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("workspace: reopen include rule file: %w", err),
			removeRecordPathIfSame(root, name, writer),
			writer.Close(),
		)
	}
	writerInfo, statErr := writer.Stat()
	readerInfo, readerStatErr := reader.Stat()
	closeErr := writer.Close()
	if err := errors.Join(statErr, readerStatErr, closeErr); err != nil {
		return nil, errors.Join(
			fmt.Errorf("workspace: inspect include rule file: %w", err),
			removeRecordPathIfSame(root, name, reader),
			reader.Close(),
		)
	}
	if !os.SameFile(writerInfo, readerInfo) {
		return nil, errors.Join(
			errors.New("workspace: include rule file changed while reopening"),
			removeRecordPathIfSame(root, name, reader),
			reader.Close(),
		)
	}
	defer func() {
		if name != "" {
			result = errors.Join(
				result,
				removeRecordPathIfSame(root, name, reader),
			)
		}
		result = errors.Join(result, reader.Close())
	}()

	if err := verifyRealPathRoot(sourcePath, sourceRoot); err != nil {
		return nil, err
	}
	manifestPath := filepath.Join(directory, name)
	runCommand := func(
		command *exec.Cmd,
		cleanup func() error,
	) (_ []byte, commandResult error) {
		defer func() {
			commandResult = errors.Join(commandResult, cleanup())
		}()
		excludePath, unlinkBeforeRun, err := configureIncludeManifestCommand(
			command,
			reader,
			manifestPath,
		)
		if err != nil {
			return nil, err
		}
		if unlinkBeforeRun && name != "" {
			if err := root.Remove(name); err != nil {
				return nil, fmt.Errorf(
					"workspace: unlink include rule file: %w",
					err,
				)
			}
			name = ""
		}
		command.Args = append(
			command.Args,
			"--exclude-from="+excludePath,
			"--",
			":(top)",
		)
		return runGitCommand(command, "")
	}

	command, cleanupCommand, err := repository.privateGitCommand(
		ctx,
		arguments...,
	)
	if err != nil {
		return nil, err
	}
	output, commandErr := runCommand(command, cleanupCommand)

	var sourceVerificationErr error
	if commandErr == nil &&
		repository.gitRoot != nil &&
		verifyRealPathRoot(repository.gitPath, repository.gitRoot) == nil {
		if _, err := reader.Seek(0, io.SeekStart); err != nil {
			sourceVerificationErr = err
		} else {
			verifier, cleanupVerifier, err := repository.worktreeCommand(
				ctx,
				arguments...,
			)
			if err != nil {
				sourceVerificationErr = err
			} else {
				verifiedOutput, err := runCommand(
					verifier,
					cleanupVerifier,
				)
				sourceVerificationErr = err
				if err == nil && !bytes.Equal(output, verifiedOutput) {
					sourceVerificationErr = errors.New(
						"workspace: included paths changed while " +
							"verifying source root",
					)
				}
			}
		}
	}

	verifyErr := errors.Join(
		verifyIncludeManifest(
			root,
			name,
			reader,
			readerInfo,
			manifest,
		),
		verifyRealPathRoot(sourcePath, sourceRoot),
	)
	if err := errors.Join(commandErr, sourceVerificationErr, verifyErr); err != nil {
		return nil, err
	}
	return output, nil
}

func verifyIncludeManifest(
	root *os.Root,
	name string,
	reader *os.File,
	before os.FileInfo,
	want []byte,
) error {
	after, err := reader.Stat()
	if err != nil {
		return fmt.Errorf("workspace: reinspect include rule file: %w", err)
	}
	if !os.SameFile(before, after) ||
		after.Size() != before.Size() ||
		after.Mode() != before.Mode() ||
		!after.ModTime().Equal(before.ModTime()) {
		return errors.New("workspace: include rule file changed while Git read it")
	}
	if name != "" {
		current, err := root.Lstat(name)
		if err != nil {
			return fmt.Errorf("workspace: recheck include rule file: %w", err)
		}
		if !os.SameFile(after, current) {
			return errors.New(
				"workspace: include rule path changed while Git read it",
			)
		}
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("workspace: rewind include rule file: %w", err)
	}
	got, err := io.ReadAll(io.LimitReader(reader, maxWorktreeIncludeSize+1))
	if err != nil {
		return fmt.Errorf("workspace: reread include rule file: %w", err)
	}
	if !bytes.Equal(got, want) {
		return errors.New("workspace: include rules changed while Git read them")
	}
	return nil
}

func readWorktreeInclude(
	sourceRoot *os.Root,
) (_ []byte, _ bool, result error) {
	info, err := sourceRoot.Lstat(worktreeIncludeFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf(
			"workspace: inspect %s: %w",
			worktreeIncludeFile,
			err,
		)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, false, fmt.Errorf(
			"workspace: %s must be a regular file",
			worktreeIncludeFile,
		)
	}
	if info.Size() > maxWorktreeIncludeSize {
		return nil, false, fmt.Errorf(
			"workspace: %s exceeds %d bytes",
			worktreeIncludeFile,
			maxWorktreeIncludeSize,
		)
	}
	file, err := sourceRoot.Open(worktreeIncludeFile)
	if err != nil {
		return nil, false, fmt.Errorf(
			"workspace: open %s: %w",
			worktreeIncludeFile,
			err,
		)
	}
	defer func() {
		result = errors.Join(result, file.Close())
	}()
	opened, err := file.Stat()
	if err != nil {
		return nil, false, fmt.Errorf(
			"workspace: inspect opened %s: %w",
			worktreeIncludeFile,
			err,
		)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, false, fmt.Errorf(
			"workspace: %s changed while opening",
			worktreeIncludeFile,
		)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxWorktreeIncludeSize+1))
	if err != nil {
		return nil, false, fmt.Errorf(
			"workspace: read %s: %w",
			worktreeIncludeFile,
			err,
		)
	}
	if len(raw) > maxWorktreeIncludeSize {
		return nil, false, fmt.Errorf(
			"workspace: %s exceeds %d bytes",
			worktreeIncludeFile,
			maxWorktreeIncludeSize,
		)
	}
	after, err := file.Stat()
	if err != nil {
		return nil, false, fmt.Errorf(
			"workspace: reinspect %s: %w",
			worktreeIncludeFile,
			err,
		)
	}
	current, err := sourceRoot.Lstat(worktreeIncludeFile)
	if err != nil {
		return nil, false, fmt.Errorf(
			"workspace: recheck %s: %w",
			worktreeIncludeFile,
			err,
		)
	}
	if !os.SameFile(opened, after) ||
		!os.SameFile(opened, current) ||
		after.Size() != opened.Size() ||
		after.Mode() != opened.Mode() ||
		!after.ModTime().Equal(opened.ModTime()) {
		return nil, false, fmt.Errorf(
			"workspace: %s changed while reading",
			worktreeIncludeFile,
		)
	}
	return raw, true, nil
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

	destinationParent, destinationOwned, err := openIncludedParent(
		destinationRoot,
		directory,
		true,
	)
	if err != nil {
		return fmt.Errorf("open destination parent: %w", err)
	}
	if destinationOwned {
		defer func() {
			result = errors.Join(result, destinationParent.Close())
		}()
	}
	if err := verifyRootEntryUnchanged(
		destinationBucket,
		destinationName,
		destinationRoot,
	); err != nil {
		return fmt.Errorf("verify destination workspace: %w", err)
	}
	if err := verifyRootEntryUnchanged(
		destinationRoot,
		directory,
		destinationParent,
	); err != nil {
		return fmt.Errorf("verify destination parent: %w", err)
	}

	temporaryName := "." + destinationName + ".include-" + uuid.NewString()
	output, err := destinationParent.OpenFile(
		temporaryName,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		sourceInfo.Mode().Perm(),
	)
	if err != nil {
		return fmt.Errorf("create staged destination: %w", err)
	}
	outputOpen := true
	defer func() {
		if temporaryName != "" {
			result = errors.Join(
				result,
				removeRecordPathIfSame(
					destinationParent,
					temporaryName,
					output,
				),
			)
		}
		if outputOpen {
			result = errors.Join(result, output.Close())
		}
	}()
	copied, err := io.Copy(output, input)
	if err != nil {
		return fmt.Errorf("copy staged contents: %w", err)
	}
	afterCopy, err := input.Stat()
	if err != nil {
		return fmt.Errorf("reinspect copied source: %w", err)
	}
	if copied != openedInfo.Size() ||
		afterCopy.Size() != openedInfo.Size() ||
		afterCopy.Mode() != openedInfo.Mode() ||
		!afterCopy.ModTime().Equal(openedInfo.ModTime()) {
		return errors.New("source changed while copying")
	}
	if err := output.Sync(); err != nil {
		return fmt.Errorf("sync staged destination: %w", err)
	}

	if err := verifyRootEntryUnchanged(
		destinationBucket,
		destinationName,
		destinationRoot,
	); err != nil {
		return fmt.Errorf("verify destination workspace: %w", err)
	}
	if err := verifyRootEntryUnchanged(
		destinationRoot,
		directory,
		destinationParent,
	); err != nil {
		return fmt.Errorf("verify destination parent: %w", err)
	}

	directoryFile, err := destinationParent.Open(".")
	if err != nil {
		return fmt.Errorf("open destination parent for install: %w", err)
	}
	installed, renameErr := renameRecordFile(
		directoryFile,
		output,
		temporaryName,
		name,
		false,
	)
	if !installed {
		directoryCloseErr := directoryFile.Close()
		if err := errors.Join(renameErr, directoryCloseErr); err != nil {
			return fmt.Errorf("install destination: %w", err)
		}
		return errors.New("destination was not installed")
	}
	closeErr := output.Close()
	outputOpen = false
	syncErr := syncRecordDirectory(directoryFile)
	directoryCloseErr := directoryFile.Close()
	if err := errors.Join(
		renameErr,
		closeErr,
		syncErr,
		directoryCloseErr,
	); err != nil {
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
		destinationRoot,
		directory,
		destinationParent,
	); err != nil {
		return fmt.Errorf("reverify destination parent: %w", err)
	}
	return nil
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
			directory, openErr := current.Open(".")
			if openErr != nil {
				return nil, false, fmt.Errorf(
					"open destination parent for sync: %w",
					openErr,
				)
			}
			syncErr := syncRecordDirectory(directory)
			closeErr := directory.Close()
			if err := errors.Join(syncErr, closeErr); err != nil {
				return nil, false, fmt.Errorf(
					"sync created destination directory %q: %w",
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
