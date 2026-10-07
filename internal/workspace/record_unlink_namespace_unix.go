//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package workspace

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

func cleanupRecordDeletionNamespace(root *os.Root) (result error) {
	return cleanupRecordDeletionNamespaceAfterValidation(root, nil)
}

func cleanupRecordDeletionNamespaceAfterValidation(
	root *os.Root,
	afterValidation func(),
) (result error) {
	directory, err := openRecordDirectory(root)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, directory.Close())
	}()
	if err := lockRecordDeletionDirectory(directory); err != nil {
		return err
	}
	defer func() {
		result = errors.Join(
			result,
			unlockRecordDeletionDirectory(directory),
		)
	}()
	if err := rejectRecordDeletionNamespaceIsolationDebris(directory); err != nil {
		return err
	}
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
	namespaceOpen := true
	defer func() {
		if namespaceOpen {
			result = errors.Join(result, namespace.Close())
		}
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
	if afterValidation != nil {
		afterValidation()
	}
	isolatedName := recordDeletionNamespaceIsolationPrefix + uuid.NewString()
	moved, err := renameDirectoryNoReplace(
		directory,
		expectedInfo,
		recordDeletionNamespace,
		isolatedName+".rename",
		isolatedName,
	)
	if err != nil {
		return fmt.Errorf(
			"isolate private record deletion namespace: %w",
			err,
		)
	}
	if !moved {
		return errors.New(
			"workspace: private record deletion namespace was not isolated",
		)
	}
	namespaceOpen = false
	if err := namespace.Close(); err != nil {
		return fmt.Errorf(
			"close isolated private record deletion namespace: %w",
			err,
		)
	}
	if err := unix.Unlinkat(
		int(directory.Fd()),
		isolatedName,
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

func rejectRecordDeletionNamespaceIsolationDebris(
	directory *os.File,
) error {
	entries, err := readRecordDeletionDirectory(directory)
	if err != nil {
		return fmt.Errorf(
			"inspect private record deletion namespace debris: %w",
			err,
		)
	}
	for _, entry := range entries {
		if strings.HasPrefix(
			entry.Name(),
			recordDeletionNamespaceIsolationPrefix,
		) {
			return fmt.Errorf(
				"workspace: private record deletion namespace debris %q has no recoverable owner",
				entry.Name(),
			)
		}
	}
	return nil
}
