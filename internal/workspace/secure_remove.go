package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (m *Manager) validateManagedPathLocation(target Workspace) (result error) {
	if err := m.validateManagedPath(target); err != nil {
		return err
	}
	root, err := openRealRoot(filepath.Dir(m.root), filepath.Base(m.root))
	if err != nil {
		return fmt.Errorf("open managed root: %w", err)
	}
	defer func() {
		result = errors.Join(result, root.Close())
	}()

	bucketName := repositoryHash(target.Repository)
	bucket, err := openRealRootFromRoot(root, bucketName)
	if err != nil {
		return fmt.Errorf("open repository bucket: %w", err)
	}
	defer func() {
		result = errors.Join(result, bucket.Close())
	}()

	info, err := bucket.Lstat(target.AgentID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect managed entry: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%q is not a real directory", target.AgentID)
	}
	entry, err := openRealRootFromRoot(bucket, target.AgentID)
	if err != nil {
		return fmt.Errorf("open managed entry: %w", err)
	}
	if err := entry.Close(); err != nil {
		return fmt.Errorf("close managed entry: %w", err)
	}
	return nil
}

func (m *Manager) removeManagedPath(target Workspace) (result error) {
	if err := m.validateManagedPath(target); err != nil {
		return err
	}
	root, err := openRealRoot(filepath.Dir(m.root), filepath.Base(m.root))
	if err != nil {
		return fmt.Errorf("open managed root: %w", err)
	}
	defer func() {
		result = errors.Join(result, root.Close())
	}()

	relative, err := filepath.Rel(m.root, target.Path)
	if err != nil {
		return fmt.Errorf("resolve managed relative path: %w", err)
	}
	components := strings.Split(relative, string(filepath.Separator))
	if len(components) != 2 ||
		components[0] != repositoryHash(target.Repository) ||
		components[1] != target.AgentID {
		return fmt.Errorf("managed relative path %q is invalid", relative)
	}

	bucket, err := openRealRootFromRoot(root, components[0])
	if err != nil {
		return fmt.Errorf("open repository bucket: %w", err)
	}
	defer func() {
		result = errors.Join(result, bucket.Close())
	}()
	if err := removeAllFromRoot(bucket, components[1]); err != nil {
		return fmt.Errorf("remove managed entry: %w", err)
	}
	return nil
}

func openRealRoot(parentPath string, name string) (*os.Root, error) {
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return nil, err
	}
	root, err := openRealRootFromRoot(parent, name)
	if err != nil {
		_ = parent.Close()
		return nil, err
	}
	if err := parent.Close(); err != nil {
		_ = root.Close()
		return nil, err
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
	for _, entry := range entries {
		if err := removeAllFromRoot(child, entry.Name()); err != nil {
			return err
		}
	}
	closeErr = child.Close()
	child = nil
	if closeErr != nil {
		return closeErr
	}
	return root.Remove(name)
}
