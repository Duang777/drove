//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package workspace

import (
	"errors"
	"fmt"
	"os"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

func renameRecordFile(
	directory *os.File,
	temporary *os.File,
	temporaryName string,
	recordName string,
	replace bool,
) (bool, error) {
	return renameRecordPath(
		directory,
		temporary,
		temporaryName,
		recordName,
		replace,
	)
}

func moveRecordFile(
	directory *os.File,
	source *os.File,
	sourceName string,
	targetName string,
) (bool, error) {
	return renameRecordPath(
		directory,
		source,
		sourceName,
		targetName,
		false,
	)
}

func renameRecordPath(
	directory *os.File,
	expected *os.File,
	sourceName string,
	targetName string,
	replace bool,
) (bool, error) {
	return renameRecordPathAfterValidation(
		directory,
		expected,
		sourceName,
		targetName,
		replace,
		nil,
	)
}

func renameRecordPathAfterValidation(
	directory *os.File,
	expected *os.File,
	sourceName string,
	targetName string,
	replace bool,
	afterValidation func(),
) (bool, error) {
	if err := verifyRecordPathIdentity(
		directory,
		expected,
		sourceName,
	); err != nil {
		return false, err
	}
	if afterValidation != nil {
		afterValidation()
	}
	fd := int(directory.Fd())
	alias := ".drove-install-" + uuid.NewString()
	if err := unix.Linkat(fd, sourceName, fd, alias, 0); err != nil {
		return false, err
	}
	aliasPresent := true
	defer func() {
		if aliasPresent {
			if verifyRecordPathIdentity(
				directory,
				expected,
				alias,
			) == nil {
				_ = unix.Unlinkat(fd, alias, 0)
			}
		}
	}()
	if err := verifyRecordPathIdentity(
		directory,
		expected,
		alias,
	); err != nil {
		return false, fmt.Errorf(
			"verify staged record identity: %w",
			err,
		)
	}
	if replace {
		if err := unix.Renameat(fd, alias, fd, targetName); err != nil {
			return false, err
		}
		aliasPresent = false
		if err := verifyRecordPathIdentity(
			directory,
			expected,
			targetName,
		); err != nil {
			return true, fmt.Errorf(
				"verify renamed record identity: %w",
				err,
			)
		}
		if err := unlinkRecordPath(
			directory,
			expected,
			sourceName,
		); err != nil {
			return true, fmt.Errorf(
				"remove installed record source: %w",
				err,
			)
		}
		return true, nil
	}
	if err := unix.Linkat(fd, alias, fd, targetName, 0); err != nil {
		return false, err
	}
	if err := verifyRecordPathIdentity(
		directory,
		expected,
		targetName,
	); err != nil {
		return true, fmt.Errorf(
			"verify linked record identity: %w",
			err,
		)
	}
	if err := unix.Unlinkat(fd, alias, 0); err != nil {
		return true, err
	}
	aliasPresent = false
	if err := verifyRecordPathIdentity(
		directory,
		expected,
		targetName,
	); err != nil {
		return true, fmt.Errorf(
			"reverify installed record identity: %w",
			err,
		)
	}
	if err := unlinkRecordPath(
		directory,
		expected,
		sourceName,
	); err != nil {
		return true, fmt.Errorf(
			"remove installed record source: %w",
			err,
		)
	}
	return true, nil
}

func unlinkRecordPath(
	directory *os.File,
	expected *os.File,
	name string,
) error {
	if err := verifyRecordPathIdentity(
		directory,
		expected,
		name,
	); err != nil {
		return err
	}
	return unix.Unlinkat(int(directory.Fd()), name, 0)
}

func verifyRecordPathIdentity(
	directory *os.File,
	expected *os.File,
	name string,
) (result error) {
	expectedInfo, err := expected.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened record: %w", err)
	}
	fd, err := unix.Openat(
		int(directory.Fd()),
		name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return fmt.Errorf("open record path %q: %w", name, err)
	}
	current := os.NewFile(uintptr(fd), name)
	if current == nil {
		_ = unix.Close(fd)
		return errors.New("create record file from descriptor")
	}
	defer func() {
		result = errors.Join(result, current.Close())
	}()
	currentInfo, err := current.Stat()
	if err != nil {
		return fmt.Errorf("inspect record path %q: %w", name, err)
	}
	if !currentInfo.Mode().IsRegular() ||
		currentInfo.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(expectedInfo, currentInfo) {
		return fmt.Errorf("record path %q changed identity", name)
	}
	return nil
}

func openRecordDirectory(root *os.Root) (*os.File, error) {
	return root.Open(".")
}

func syncRecordDirectory(directory *os.File) error {
	return directory.Sync()
}
