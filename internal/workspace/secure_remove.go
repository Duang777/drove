package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/google/uuid"
)

func (m *Manager) ensureManagedRoot() (result error) {
	if m.rootErr != nil {
		return m.rootErr
	}
	dataDir := filepath.Dir(m.root)
	if err := ensureDirectory(dataDir); err != nil {
		return err
	}
	root, err := m.openDataDirectoryRoot()
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, root.Close())
	}()

	name := filepath.Base(m.root)
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		if err := root.Mkdir(name, 0o700); err != nil &&
			!errors.Is(err, os.ErrExist) {
			return fmt.Errorf("workspace: create worktree root: %w", err)
		}
		info, err = root.Lstat(name)
	}
	if err != nil {
		return fmt.Errorf("workspace: inspect worktree root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf(
			"workspace: worktree root %q is not a real directory",
			m.root,
		)
	}
	opened, err := openRealRootFromRoot(root, name)
	if err != nil {
		return fmt.Errorf("workspace: open worktree root: %w", err)
	}
	if err := m.verifyWorktreeRoot(opened); err != nil {
		_ = opened.Close()
		return err
	}
	return opened.Close()
}

func (m *Manager) pinDataDirectory() error {
	root, err := m.openDataDirectoryRoot()
	if err != nil {
		return err
	}
	if err := root.Close(); err != nil {
		return fmt.Errorf("workspace: close data directory: %w", err)
	}
	return nil
}

func (m *Manager) openDataDirectoryRoot() (*os.Root, error) {
	path := filepath.Dir(m.root)
	root, err := openRealPathRoot(path)
	if err != nil {
		return nil, fmt.Errorf("workspace: open data directory: %w", err)
	}
	opened, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("workspace: inspect opened data directory: %w", err)
	}
	if m.dataDirInfo != nil && !os.SameFile(m.dataDirInfo, opened) {
		_ = root.Close()
		return nil, errors.New(
			"workspace: data directory changed after manager initialization",
		)
	}
	if m.dataDirInfo == nil {
		m.dataDirInfo = opened
	}
	return root, nil
}

func (m *Manager) openWorktreeRoot() (*os.Root, error) {
	if m.rootErr != nil {
		return nil, m.rootErr
	}
	dataDir, err := m.openDataDirectoryRoot()
	if err != nil {
		return nil, err
	}
	root, err := openRealRootFromRoot(dataDir, filepath.Base(m.root))
	if err != nil {
		_ = dataDir.Close()
		return nil, fmt.Errorf("open managed root: %w", err)
	}
	if err := m.verifyWorktreeRoot(root); err != nil {
		_ = dataDir.Close()
		_ = root.Close()
		return nil, err
	}
	if err := dataDir.Close(); err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("close data directory: %w", err)
	}
	return root, nil
}

func (m *Manager) openManagedBucketRoot(target Workspace) (*os.Root, error) {
	if err := m.validateManagedPath(target); err != nil {
		return nil, err
	}
	root, err := m.openWorktreeRoot()
	if err != nil {
		return nil, err
	}
	bucket, err := openRealRootFromRoot(
		root,
		repositoryHash(target.Repository),
	)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("open repository bucket: %w", err)
	}
	if err := m.verifyRepositoryBucket(
		repositoryHash(target.Repository),
		bucket,
	); err != nil {
		_ = root.Close()
		_ = bucket.Close()
		return nil, err
	}
	if err := root.Close(); err != nil {
		_ = bucket.Close()
		return nil, fmt.Errorf("close managed root: %w", err)
	}
	return bucket, nil
}

func (m *Manager) ensureManagedBucket(repository string) (result error) {
	root, err := m.openWorktreeRoot()
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, root.Close())
	}()
	name := repositoryHash(repository)
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		if err := root.Mkdir(name, 0o700); err != nil &&
			!errors.Is(err, os.ErrExist) {
			return fmt.Errorf("workspace: create repository bucket: %w", err)
		}
		info, err = root.Lstat(name)
	}
	if err != nil {
		return fmt.Errorf("workspace: inspect repository bucket: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf(
			"workspace: repository bucket %q is not a real directory",
			name,
		)
	}
	opened, err := openRealRootFromRoot(root, name)
	if err != nil {
		return fmt.Errorf("workspace: open repository bucket: %w", err)
	}
	if err := m.verifyRepositoryBucket(name, opened); err != nil {
		_ = opened.Close()
		return err
	}
	return opened.Close()
}

func (m *Manager) verifyWorktreeRoot(root *os.Root) error {
	opened, err := root.Stat(".")
	if err != nil {
		return fmt.Errorf("workspace: inspect opened worktree root: %w", err)
	}
	if m.worktreeInfo != nil && !os.SameFile(m.worktreeInfo, opened) {
		return errors.New(
			"workspace: worktree root changed after manager initialization",
		)
	}
	if m.worktreeInfo == nil {
		m.worktreeInfo = opened
	}
	return nil
}

func (m *Manager) verifyRepositoryBucket(
	name string,
	root *os.Root,
) error {
	opened, err := root.Stat(".")
	if err != nil {
		return fmt.Errorf("workspace: inspect opened repository bucket: %w", err)
	}
	if expected := m.bucketInfo[name]; expected != nil &&
		!os.SameFile(expected, opened) {
		return fmt.Errorf(
			"workspace: repository bucket %q changed after manager initialization",
			name,
		)
	}
	if m.bucketInfo[name] == nil {
		m.bucketInfo[name] = opened
	}
	return nil
}

func (m *Manager) openManagedWorkspaceRoot(
	target Workspace,
) (*os.Root, error) {
	bucket, err := m.openManagedBucketRoot(target)
	if err != nil {
		return nil, err
	}
	entry, err := openRealRootFromRoot(bucket, target.AgentID)
	if err != nil {
		_ = bucket.Close()
		return nil, fmt.Errorf("open managed entry: %w", err)
	}
	if err := bucket.Close(); err != nil {
		_ = entry.Close()
		return nil, fmt.Errorf("close repository bucket: %w", err)
	}
	return entry, nil
}

func (m *Manager) removeManagedPath(target Workspace) (result error) {
	bucket, err := m.openManagedBucketRoot(target)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, bucket.Close())
	}()
	if err := removeAllFromRoot(bucket, target.AgentID); err != nil {
		return fmt.Errorf("remove managed entry: %w", err)
	}
	return nil
}

func removalQuarantinePrefix(record workspaceRecord) string {
	return "." + record.AgentID + ".removal-" +
		record.Removal.OperationID + "-"
}

func findRemovalQuarantine(
	bucket *os.Root,
	record workspaceRecord,
) (string, bool, error) {
	entries, err := readRootDirectory(bucket)
	if err != nil {
		return "", false, err
	}
	prefix := removalQuarantinePrefix(record)
	var matched string
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		if matched != "" {
			return "", false, fmt.Errorf(
				"workspace: removal %q has multiple quarantined paths",
				record.Removal.OperationID,
			)
		}
		info, err := bucket.Lstat(entry.Name())
		if err != nil {
			return "", false, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", false, fmt.Errorf(
				"workspace: removal quarantine %q is not a real directory",
				entry.Name(),
			)
		}
		matched = entry.Name()
	}
	return matched, matched != "", nil
}

func quarantineManagedPath(
	bucket *os.Root,
	record workspaceRecord,
	opened *os.Root,
) (name string, moved bool, result error) {
	if record.Removal == nil {
		return "", false, errors.New(
			"workspace: removal intent is missing",
		)
	}
	existing, exists, err := findRemovalQuarantine(bucket, record)
	if err != nil {
		return "", false, err
	}
	if exists {
		if opened != nil {
			if err := verifyRootEntryUnchanged(
				bucket,
				existing,
				opened,
			); err != nil {
				return "", false, fmt.Errorf(
					"workspace: verify removal quarantine: %w",
					err,
				)
			}
		}
		return existing, false, nil
	}
	if record.Removal.Quarantined {
		return "", false, nil
	}

	var owned bool
	if opened == nil {
		opened, err = openRealRootFromRoot(bucket, record.AgentID)
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		owned = true
		defer func() {
			if owned {
				result = errors.Join(result, opened.Close())
			}
		}()
	}
	if err := verifyRootEntryUnchanged(
		bucket,
		record.AgentID,
		opened,
	); err != nil {
		return "", false, err
	}
	openedInfo, err := opened.Stat(".")
	if err != nil {
		return "", false, err
	}
	name = removalQuarantinePrefix(record) + uuid.NewString()
	directory, err := bucket.Open(".")
	if err != nil {
		return "", false, err
	}
	moved, renameErr := renameDirectoryNoReplace(
		directory,
		openedInfo,
		record.AgentID,
		name,
	)
	syncErr := syncRecordDirectory(directory)
	closeErr := directory.Close()
	if err := errors.Join(renameErr, syncErr, closeErr); err != nil {
		return name, moved, err
	}
	current, err := bucket.Lstat(name)
	if err != nil {
		return name, true, err
	}
	if !current.IsDir() ||
		current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(openedInfo, current) {
		restoreErr := restoreManagedQuarantine(bucket, record, name)
		return name, true, errors.Join(
			errors.New(
				"workspace: managed path changed while entering quarantine",
			),
			restoreErr,
		)
	}
	return name, true, nil
}

func restoreManagedQuarantine(
	bucket *os.Root,
	record workspaceRecord,
	name string,
) (result error) {
	opened, err := openRealRootFromRoot(bucket, name)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, opened.Close())
	}()
	openedInfo, err := opened.Stat(".")
	if err != nil {
		return err
	}
	directory, err := bucket.Open(".")
	if err != nil {
		return err
	}
	restored, renameErr := renameDirectoryNoReplace(
		directory,
		openedInfo,
		name,
		record.AgentID,
	)
	syncErr := syncRecordDirectory(directory)
	closeErr := directory.Close()
	if err := errors.Join(renameErr, syncErr, closeErr); err != nil {
		return err
	}
	if !restored {
		return errors.New("workspace: removal quarantine was not restored")
	}
	return verifyRootEntryUnchanged(bucket, record.AgentID, opened)
}

func (m *Manager) removeManagedBucketIfEmpty(
	target Workspace,
) (result error) {
	if err := m.validateManagedPath(target); err != nil {
		return err
	}
	root, err := m.openWorktreeRoot()
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, root.Close())
	}()
	name := repositoryHash(target.Repository)
	err = root.Remove(name)
	switch {
	case err == nil:
		delete(m.bucketInfo, name)
		return nil
	case errors.Is(err, os.ErrNotExist):
		return nil
	case errors.Is(err, syscall.ENOTEMPTY), errors.Is(err, syscall.EEXIST):
		return nil
	default:
		return fmt.Errorf("remove repository bucket: %w", err)
	}
}

func (m *Manager) openRecordBucket(
	worktreePath string,
) (*os.Root, string, error) {
	if !filepath.IsAbs(worktreePath) ||
		filepath.Clean(worktreePath) != worktreePath {
		return nil, "", fmt.Errorf(
			"workspace: record path %q is not a clean absolute path",
			worktreePath,
		)
	}
	relative, err := filepath.Rel(m.root, worktreePath)
	if err != nil {
		return nil, "", fmt.Errorf("workspace: resolve record path: %w", err)
	}
	components := strings.Split(relative, string(filepath.Separator))
	if len(components) != 2 ||
		!validRepositoryHash(components[0]) ||
		validateAgentID(components[1]) != nil {
		return nil, "", fmt.Errorf(
			"workspace: record path %q is outside the managed root",
			worktreePath,
		)
	}
	root, err := m.openWorktreeRoot()
	if err != nil {
		return nil, "", err
	}
	bucket, err := openRealRootFromRoot(root, components[0])
	if err != nil {
		_ = root.Close()
		return nil, "", fmt.Errorf("workspace: open record bucket: %w", err)
	}
	if err := m.verifyRepositoryBucket(components[0], bucket); err != nil {
		_ = root.Close()
		_ = bucket.Close()
		return nil, "", err
	}
	if err := root.Close(); err != nil {
		_ = bucket.Close()
		return nil, "", fmt.Errorf("workspace: close managed root: %w", err)
	}
	return bucket, components[1], nil
}

func openRealPathRoot(path string) (*os.Root, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%q is not a real directory", path)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	if !os.SameFile(info, opened) {
		_ = root.Close()
		return nil, fmt.Errorf("%q changed while opening", path)
	}
	return root, nil
}

func openRealRootFromRoot(parent *os.Root, name string) (*os.Root, error) {
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%q is not a real directory", name)
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	if !os.SameFile(info, opened) {
		_ = root.Close()
		return nil, fmt.Errorf("%q changed while opening", name)
	}
	return root, nil
}

func verifyRootEntryUnchanged(
	parent *os.Root,
	name string,
	opened *os.Root,
) error {
	openedInfo, err := opened.Stat(".")
	if err != nil {
		return fmt.Errorf("inspect opened directory %q: %w", name, err)
	}
	current, err := parent.Lstat(name)
	if err != nil {
		return fmt.Errorf("reinspect directory %q: %w", name, err)
	}
	if !current.IsDir() ||
		current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(openedInfo, current) {
		return fmt.Errorf("directory %q changed while in use", name)
	}
	return nil
}

func readRootDirectory(root *os.Root) ([]os.DirEntry, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	return entries, errors.Join(readErr, closeErr)
}

func removeAllFromRoot(root *os.Root, name string) (result error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return root.Remove(name)
	}

	child, err := openRealRootFromRoot(root, name)
	if err != nil {
		return err
	}
	defer func() {
		if child != nil {
			result = errors.Join(result, child.Close())
		}
	}()
	directory, err := child.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	if err := verifyRootEntryUnchanged(root, name, child); err != nil {
		return err
	}
	if err := removeRootEntries(root, name, entries); err != nil {
		return err
	}
	closeErr = child.Close()
	child = nil
	if closeErr != nil {
		return closeErr
	}
	return root.Remove(name)
}

func removeRootEntries(
	root *os.Root,
	name string,
	entries []os.DirEntry,
) error {
	for _, entry := range entries {
		childName := filepath.Join(name, entry.Name())
		if err := removeAllFromRoot(root, childName); err != nil {
			return err
		}
	}
	return nil
}
