//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package workspace

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func cleanupRecordDeletionNamespace(root *os.Root) (result error) {
	directory, err := openRecordDirectory(root)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, directory.Close())
	}()
	namespace, err := openRecordDeletionDirectoryAt(
		directory,
		recordDeletionNamespace,
	)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf(
			"open private record deletion namespace for cleanup: %w",
			err,
		)
	}
	defer func() {
		result = errors.Join(result, namespace.Close())
	}()
	if err := validateRecordDeletionDirectory(namespace); err != nil {
		return err
	}
	if err := unix.Flock(int(namespace.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf(
			"lock private record deletion namespace for cleanup: %w",
			err,
		)
	}
	if err := recoverRecordDeletionTransactions(directory, namespace); err != nil {
		return err
	}
	entries, err := readRecordDeletionDirectory(namespace)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New(
			"workspace: private record deletion namespace is not empty",
		)
	}
	current, err := openRecordDeletionDirectoryAt(
		directory,
		recordDeletionNamespace,
	)
	if err != nil {
		return err
	}
	expectedInfo, expectedErr := namespace.Stat()
	currentInfo, currentErr := current.Stat()
	closeErr := current.Close()
	if err := errors.Join(expectedErr, currentErr, closeErr); err != nil {
		return err
	}
	if !os.SameFile(expectedInfo, currentInfo) {
		return errors.New(
			"workspace: private record deletion namespace changed identity",
		)
	}
	if err := unix.Unlinkat(
		int(directory.Fd()),
		recordDeletionNamespace,
		unix.AT_REMOVEDIR,
	); err != nil {
		return fmt.Errorf(
			"remove private record deletion namespace: %w",
			err,
		)
	}
	if err := directory.Sync(); err != nil {
		return fmt.Errorf(
			"sync removed private record deletion namespace: %w",
			err,
		)
	}
	return nil
}
