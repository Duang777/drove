package workspace

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

func (m *Manager) ensureManagedRoot() (result error) {
	if m.rootErr != nil {
		return m.rootErr
	}
	root, err := m.openOrCreateDataDirectoryRoot()
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

func (m *Manager) openOrCreateDataDirectoryRoot() (*os.Root, error) {
	if m.dataDirInfo != nil {
		return m.openDataDirectoryRoot()
	}
	path := filepath.Dir(m.root)
	root, err := openOrCreateRealPathRoot(path)
	if err != nil {
		return nil, fmt.Errorf("workspace: create data directory: %w", err)
	}
	opened, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("workspace: inspect created data directory: %w", err)
	}
	if err := verifyRealPathRoot(path, root); err != nil {
		_ = root.Close()
		return nil, err
	}
	m.dataDirInfo = opened
	return root, nil
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

func removalMarkerName(record workspaceRecord) string {
	return ".drove-removal-" + record.Removal.OperationID
}

func removalMarkerTemporaryPrefix(record workspaceRecord) string {
	return removalMarkerName(record) + ".install-" +
		record.Removal.DirectoryToken + "-"
}

func removalMarkerTemporaryName(record workspaceRecord) string {
	return removalMarkerTemporaryPrefix(record) + uuid.NewString()
}

func removalMarkerDiscardPrefix(record workspaceRecord) string {
	return removalMarkerName(record) + ".discard-" +
		record.Removal.DirectoryToken + "-"
}

func removalMarkerDiscardName(record workspaceRecord) string {
	return removalMarkerDiscardPrefix(record) + uuid.NewString()
}

func isRemovalMarkerTemporaryName(
	record workspaceRecord,
	name string,
) bool {
	return validRemovalMarkerArtifactName(
		name,
		removalMarkerTemporaryPrefix(record),
	)
}

func isRemovalMarkerDiscardName(
	record workspaceRecord,
	name string,
) bool {
	return validRemovalMarkerArtifactName(
		name,
		removalMarkerDiscardPrefix(record),
	)
}

func validRemovalMarkerArtifactName(name string, prefix string) bool {
	suffix, found := strings.CutPrefix(name, prefix)
	return found && canonicalUUID(suffix)
}

func removalQuarantineIsolationName(name string) string {
	const suffix = ".rename"
	if strings.HasSuffix(name, suffix) {
		return strings.TrimSuffix(name, suffix)
	}
	return name + suffix
}

func validateRemovalMarkerPhase(
	root *os.Root,
	record workspaceRecord,
) error {
	if err := cleanupRemovalMarkerTemps(root, record); err != nil {
		return err
	}
	if record.Removal != nil && record.Removal.ContentsCleared {
		return validateClearedRemovalDirectory(root, record, false)
	}
	if record.Removal != nil && record.Removal.Started {
		return verifyRemovalMarker(root, record)
	}
	return ensureRemovalMarker(root, record)
}

func ensureRemovalMarker(
	root *os.Root,
	record workspaceRecord,
) (result error) {
	if record.Removal == nil ||
		record.Removal.DirectoryToken == "" {
		return errors.New("workspace: removal record has no directory token")
	}
	if err := cleanupRemovalMarkerTemps(root, record); err != nil {
		return err
	}
	name := removalMarkerName(record)
	_, err := root.Lstat(name)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := installRemovalMarker(root, name, record.Removal.DirectoryToken); err != nil {
			return err
		}
	case err != nil:
		return fmt.Errorf("workspace: inspect removal marker: %w", err)
	}
	if err := verifyRemovalMarker(root, record); err == nil {
		return nil
	}
	if err := removeIncompleteRemovalMarker(root, name, record); err != nil {
		return err
	}
	if err := installRemovalMarker(
		root,
		name,
		record.Removal.DirectoryToken,
	); err != nil {
		return err
	}
	return verifyRemovalMarker(root, record)
}

func installRemovalMarker(
	root *os.Root,
	name string,
	token string,
) (result error) {
	temporaryName := name + ".install-" + token + "-" + uuid.NewString()
	file, err := root.OpenFile(
		temporaryName,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		return fmt.Errorf("workspace: create removal marker: %w", err)
	}
	cleanupOnFailure := true
	defer func() {
		if cleanupOnFailure {
			result = errors.Join(
				result,
				removeOwnedRecordPathIfSame(
					root,
					temporaryName,
					file,
					name+".install-"+token+"-"+uuid.NewString(),
					nil,
				),
				syncRecordBucket(root, temporaryName),
			)
		}
		result = errors.Join(result, file.Close())
	}()
	payload := token + "\n"
	written, err := io.WriteString(file, payload)
	if err != nil {
		return fmt.Errorf("workspace: write removal marker: %w", err)
	}
	if written != len(payload) {
		return fmt.Errorf("workspace: write removal marker: %w", io.ErrShortWrite)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("workspace: sync removal marker: %w", err)
	}
	directory, err := openRecordDirectory(root)
	if err != nil {
		return fmt.Errorf("workspace: open removal marker directory: %w", err)
	}
	installed, renameErr := renameRecordFile(
		directory,
		file,
		temporaryName,
		name,
		false,
	)
	if !installed {
		closeDirectoryErr := directory.Close()
		return fmt.Errorf(
			"workspace: install removal marker: %w",
			errors.Join(renameErr, closeDirectoryErr),
		)
	}
	cleanupOnFailure = false
	syncErr := syncRecordDirectory(directory)
	closeDirectoryErr := directory.Close()
	if err := errors.Join(renameErr, syncErr, closeDirectoryErr); err != nil {
		return fmt.Errorf("workspace: sync removal marker directory: %w", err)
	}
	return nil
}

func cleanupRemovalMarkerTemps(
	root *os.Root,
	record workspaceRecord,
) (result error) {
	if record.Removal == nil ||
		record.Removal.OperationID == "" ||
		record.Removal.DirectoryToken == "" {
		return errors.New(
			"workspace: removal record has no marker identity",
		)
	}
	entries, err := readRootDirectory(root)
	if err != nil {
		return fmt.Errorf("workspace: inspect removal marker staging files: %w", err)
	}
	removed := false
	for _, entry := range entries {
		temporary := isRemovalMarkerTemporaryName(record, entry.Name())
		discard := isRemovalMarkerDiscardName(record, entry.Name())
		if !temporary && !discard {
			if strings.HasPrefix(
				entry.Name(),
				removalMarkerTemporaryPrefix(record),
			) || strings.HasPrefix(
				entry.Name(),
				removalMarkerDiscardPrefix(record),
			) {
				return fmt.Errorf(
					"workspace: removal marker artifact %q has an invalid name",
					entry.Name(),
				)
			}
			continue
		}
		info, err := root.Lstat(entry.Name())
		if err != nil {
			return fmt.Errorf(
				"workspace: inspect removal marker staging file: %w",
				err,
			)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New(
				"workspace: removal marker staging path is not a regular file",
			)
		}
		file, err := root.Open(entry.Name())
		if err != nil {
			return fmt.Errorf(
				"workspace: open removal marker staging file: %w",
				err,
			)
		}
		opened, statErr := file.Stat()
		if statErr == nil && !os.SameFile(info, opened) {
			statErr = errors.New(
				"workspace: removal marker staging file changed while opening",
			)
		}
		removeErr := error(nil)
		if statErr == nil {
			isolatedName := removalMarkerDiscardName(record)
			if temporary {
				isolatedName = removalMarkerTemporaryName(record)
			}
			removeErr = removeOwnedRecordPath(
				root,
				entry.Name(),
				file,
				isolatedName,
				nil,
				nil,
			)
		}
		closeErr := file.Close()
		if err := errors.Join(statErr, removeErr, closeErr); err != nil {
			return err
		}
		removed = true
	}
	if removed {
		return syncRecordBucket(root, removalMarkerName(record))
	}
	return nil
}

func removeIncompleteRemovalMarker(
	root *os.Root,
	name string,
	record workspaceRecord,
) (result error) {
	info, err := root.Lstat(name)
	if err != nil {
		return fmt.Errorf("workspace: inspect incomplete removal marker: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("workspace: incomplete removal marker is not a regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return fmt.Errorf("workspace: open incomplete removal marker: %w", err)
	}
	defer func() {
		result = errors.Join(result, file.Close())
	}()
	opened, err := file.Stat()
	if err != nil {
		return fmt.Errorf("workspace: inspect incomplete removal marker: %w", err)
	}
	if !os.SameFile(info, opened) {
		return errors.New(
			"workspace: incomplete removal marker changed while opening",
		)
	}
	if err := removeOwnedRecordPath(
		root,
		name,
		file,
		removalMarkerDiscardName(record),
		nil,
		nil,
	); err != nil {
		return fmt.Errorf("workspace: remove incomplete removal marker: %w", err)
	}
	return nil
}

func verifyRemovalMarker(
	root *os.Root,
	record workspaceRecord,
) (result error) {
	if record.Removal == nil ||
		record.Removal.DirectoryToken == "" {
		return errors.New("workspace: removal record has no directory token")
	}
	name := removalMarkerName(record)
	info, err := root.Lstat(name)
	if err != nil {
		return fmt.Errorf("workspace: inspect removal marker: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("workspace: removal marker is not a regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return fmt.Errorf("workspace: open removal marker: %w", err)
	}
	defer func() {
		result = errors.Join(result, file.Close())
	}()
	opened, err := file.Stat()
	if err != nil {
		return fmt.Errorf("workspace: inspect opened removal marker: %w", err)
	}
	if !os.SameFile(info, opened) {
		return errors.New("workspace: removal marker changed while opening")
	}
	if err := validateRemovalMarkerFile(file, record); err != nil {
		return err
	}
	current, err := root.Lstat(name)
	if err != nil {
		return fmt.Errorf("workspace: recheck removal marker: %w", err)
	}
	if !os.SameFile(opened, current) {
		return errors.New("workspace: removal marker changed while reading")
	}
	return nil
}

func validateRemovalMarkerFile(
	file *os.File,
	record workspaceRecord,
) error {
	if record.Removal == nil || record.Removal.DirectoryToken == "" {
		return errors.New("workspace: removal record has no directory token")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("workspace: rewind removal marker: %w", err)
	}
	raw, err := io.ReadAll(io.LimitReader(file, 128))
	if err != nil {
		return fmt.Errorf("workspace: read removal marker: %w", err)
	}
	if string(raw) != record.Removal.DirectoryToken+"\n" {
		return errors.New("workspace: removal marker token mismatch")
	}
	return nil
}

func removalDirectoryContainsOnlyMarker(
	root *os.Root,
	record workspaceRecord,
) (bool, error) {
	if err := cleanupRemovalMarkerTemps(root, record); err != nil {
		return false, err
	}
	entries, err := readRootDirectory(root)
	if err != nil {
		return false, err
	}
	if len(entries) != 1 || entries[0].Name() != removalMarkerName(record) {
		return false, nil
	}
	if err := verifyRemovalMarker(root, record); err != nil {
		return false, err
	}
	return true, nil
}

func validateClearedRemovalDirectory(
	root *os.Root,
	record workspaceRecord,
	requireMarker bool,
) error {
	entries, err := readRootDirectory(root)
	if err != nil {
		return err
	}
	markerPresent := false
	for _, entry := range entries {
		if entry.Name() != removalMarkerName(record) {
			return fmt.Errorf(
				"workspace: cleared removal quarantine contains unexpected entry %q",
				entry.Name(),
			)
		}
		if markerPresent {
			return errors.New(
				"workspace: cleared removal quarantine contains duplicate markers",
			)
		}
		markerPresent = true
	}
	if !markerPresent {
		if requireMarker {
			return errors.New(
				"workspace: cleared removal quarantine marker is missing",
			)
		}
		return nil
	}
	return verifyRemovalMarker(root, record)
}

func removeRootEntriesExcept(
	root *os.Root,
	entries []os.DirEntry,
	preserved string,
) error {
	for _, entry := range entries {
		if entry.Name() == preserved {
			continue
		}
		if err := removeAllFromRoot(root, entry.Name()); err != nil {
			return err
		}
	}
	directory, err := openRecordDirectory(root)
	if err != nil {
		return err
	}
	syncErr := syncRecordDirectory(directory)
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

func removeRemovalMarkerIfPresent(
	root *os.Root,
	record workspaceRecord,
) (result error) {
	if err := cleanupRemovalMarkerTemps(root, record); err != nil {
		return err
	}
	name := removalMarkerName(record)
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("workspace: inspect removal marker: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("workspace: removal marker is not a regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return fmt.Errorf("workspace: open removal marker: %w", err)
	}
	defer func() {
		result = errors.Join(result, file.Close())
	}()
	opened, err := file.Stat()
	if err != nil {
		return fmt.Errorf("workspace: inspect opened removal marker: %w", err)
	}
	if !os.SameFile(info, opened) {
		return errors.New("workspace: removal marker changed while opening")
	}
	if err := removeOwnedRecordPath(
		root,
		name,
		file,
		removalMarkerDiscardName(record),
		func(candidate *os.File) error {
			return validateRemovalMarkerFile(candidate, record)
		},
		nil,
	); err != nil {
		return fmt.Errorf("workspace: remove removal marker: %w", err)
	}
	return nil
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
	directory, err := openRecordDirectory(bucket)
	if err != nil {
		return "", false, err
	}
	moved, renameErr := renameDirectoryNoReplace(
		directory,
		openedInfo,
		record.AgentID,
		removalQuarantineIsolationName(name),
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
		return name, true, errors.New(
			"workspace: managed path changed while entering quarantine",
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
	directory, err := openRecordDirectory(bucket)
	if err != nil {
		return err
	}
	restored, renameErr := renameDirectoryNoReplace(
		directory,
		openedInfo,
		name,
		removalQuarantineIsolationName(name),
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

func (m *Manager) verifyRecordBucket(
	worktreePath string,
	bucket *os.Root,
) (result error) {
	relative, err := filepath.Rel(m.root, worktreePath)
	if err != nil {
		return fmt.Errorf("workspace: resolve record path: %w", err)
	}
	components := strings.Split(relative, string(filepath.Separator))
	if len(components) != 2 || !validRepositoryHash(components[0]) {
		return fmt.Errorf(
			"workspace: record path %q is outside the managed root",
			worktreePath,
		)
	}
	root, err := m.openWorktreeRoot()
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, root.Close())
	}()
	if err := verifyRootEntryUnchanged(
		root,
		components[0],
		bucket,
	); err != nil {
		return fmt.Errorf("workspace: verify record bucket: %w", err)
	}
	return nil
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

func openOrCreateRealPathRoot(path string) (*os.Root, error) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%q is not an absolute path", path)
	}
	volumeRoot := filepath.VolumeName(path) + string(filepath.Separator)
	relative, err := filepath.Rel(volumeRoot, path)
	if err != nil {
		return nil, fmt.Errorf("resolve path below volume root: %w", err)
	}
	root, err := openRealPathRoot(volumeRoot)
	if err != nil {
		return nil, fmt.Errorf("open volume root %q: %w", volumeRoot, err)
	}
	if relative == "." {
		return root, nil
	}
	current := volumeRoot
	for _, name := range strings.Split(relative, string(filepath.Separator)) {
		if name == "" || name == "." || name == ".." {
			_ = root.Close()
			return nil, fmt.Errorf("invalid path component %q in %q", name, path)
		}
		info, inspectErr := root.Lstat(name)
		if errors.Is(inspectErr, os.ErrNotExist) {
			if err := root.Mkdir(name, 0o700); err != nil &&
				!errors.Is(err, os.ErrExist) {
				_ = root.Close()
				return nil, fmt.Errorf(
					"create directory %q: %w",
					filepath.Join(current, name),
					err,
				)
			}
			info, inspectErr = root.Lstat(name)
		}
		if inspectErr != nil {
			_ = root.Close()
			return nil, fmt.Errorf(
				"inspect directory %q: %w",
				filepath.Join(current, name),
				inspectErr,
			)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			_ = root.Close()
			return nil, fmt.Errorf(
				"%q is not a real directory",
				filepath.Join(current, name),
			)
		}
		child, err := openRealRootFromRoot(root, name)
		if err != nil {
			_ = root.Close()
			return nil, fmt.Errorf(
				"open directory %q: %w",
				filepath.Join(current, name),
				err,
			)
		}
		if err := verifyRootEntryUnchanged(root, name, child); err != nil {
			_ = child.Close()
			_ = root.Close()
			return nil, err
		}
		if err := root.Close(); err != nil {
			_ = child.Close()
			return nil, fmt.Errorf(
				"close parent directory %q: %w",
				current,
				err,
			)
		}
		current = filepath.Join(current, name)
		root = child
	}
	if err := verifyRealPathRoot(path, root); err != nil {
		_ = root.Close()
		return nil, err
	}
	return root, nil
}

func verifyRealPathRoot(path string, opened *os.Root) error {
	openedInfo, err := opened.Stat(".")
	if err != nil {
		return fmt.Errorf("inspect opened directory %q: %w", path, err)
	}
	current, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("reinspect directory %q: %w", path, err)
	}
	if !current.IsDir() ||
		current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(openedInfo, current) {
		return fmt.Errorf("directory %q changed while in use", path)
	}
	return nil
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
		return removeRootDeletionDebris(root, name)
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
	return removeOpenedDirectoryFromRoot(root, name, child)
}

func removeOpenedDirectoryFromRoot(
	parent *os.Root,
	name string,
	root *os.Root,
) (result error) {
	return removeOpenedDirectoryFromRootAfterIsolation(
		parent,
		name,
		root,
		name,
		nil,
	)
}

func removeOpenedDirectoryFromRootAfterIsolation(
	parent *os.Root,
	name string,
	root *os.Root,
	recoveryName string,
	afterIsolation func(string),
) (result error) {
	defer func() {
		if root != nil {
			result = errors.Join(result, root.Close())
		}
	}()
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	if err := verifyRootEntryUnchanged(parent, name, root); err != nil {
		return err
	}
	if debris, err := findRootDeletionDebris(
		parent,
		recoveryName,
		name,
	); err != nil {
		return err
	} else if debris != "" {
		return fmt.Errorf(
			"directory %q has existing deletion debris %q",
			recoveryName,
			debris,
		)
	}
	if err := removeRootDirectoryContents(
		parent,
		name,
		root,
		entries,
	); err != nil {
		return err
	}
	openedInfo, err := root.Stat(".")
	if err != nil {
		return err
	}
	deleteName := rootDeletionPrefix(recoveryName) + uuid.NewString()
	directory, err = openRecordDirectory(parent)
	if err != nil {
		return err
	}
	moved, renameErr := renameDirectoryNoReplace(
		directory,
		openedInfo,
		name,
		deleteName+".rename",
		deleteName,
	)
	closeDirectoryErr := directory.Close()
	if moved {
		name = deleteName
	}
	if err := errors.Join(renameErr, closeDirectoryErr); err != nil {
		return fmt.Errorf("isolate removed directory: %w", err)
	}
	if !moved {
		return errors.New("removed directory was not isolated")
	}
	if err := verifyRootEntryUnchanged(parent, name, root); err != nil {
		return err
	}
	if afterIsolation != nil {
		afterIsolation(name)
	}
	closeErr = root.Close()
	root = nil
	if closeErr != nil {
		return closeErr
	}
	if err := parent.Remove(name); err != nil {
		return err
	}
	return syncRecordBucket(parent, name)
}

func rootDeletionPrefix(name string) string {
	digest := sha256.Sum256([]byte(name))
	return fmt.Sprintf(".drove-delete-%x-", digest[:8])
}

func isRootDeletionName(name string, recoveryName string) bool {
	suffix, found := strings.CutPrefix(name, rootDeletionPrefix(recoveryName))
	if !found {
		return false
	}
	suffix = strings.TrimSuffix(suffix, ".rename")
	_, err := uuid.Parse(suffix)
	return err == nil
}

func findRootDeletionDebris(
	parent *os.Root,
	recoveryName string,
	ignoredName string,
) (string, error) {
	entries, err := readRootDirectory(parent)
	if err != nil {
		return "", err
	}
	found := ""
	for _, entry := range entries {
		if entry.Name() == ignoredName ||
			!isRootDeletionName(entry.Name(), recoveryName) {
			continue
		}
		info, err := parent.Lstat(entry.Name())
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf(
				"deletion debris %q is not a real directory",
				entry.Name(),
			)
		}
		if found != "" {
			return "", fmt.Errorf(
				"directory %q has multiple deletion debris paths",
				recoveryName,
			)
		}
		found = entry.Name()
	}
	return found, nil
}

func removeRootDeletionDebris(
	parent *os.Root,
	recoveryName string,
) error {
	name, err := findRootDeletionDebris(parent, recoveryName, "")
	if err != nil || name == "" {
		return err
	}
	root, err := openRealRootFromRoot(parent, name)
	if err != nil {
		return err
	}
	return removeOpenedDirectoryFromRootAfterIsolation(
		parent,
		name,
		root,
		recoveryName,
		nil,
	)
}

func removeRootDirectoryContents(
	parent *os.Root,
	name string,
	root *os.Root,
	entries []os.DirEntry,
) error {
	if err := removeRootEntries(root, entries); err != nil {
		return err
	}
	return verifyRootEntryUnchanged(parent, name, root)
}

func removeRootEntries(
	root *os.Root,
	entries []os.DirEntry,
) error {
	for _, entry := range entries {
		if err := removeAllFromRoot(root, entry.Name()); err != nil {
			return err
		}
	}
	return nil
}
