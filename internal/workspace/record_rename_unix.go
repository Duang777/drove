//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package workspace

import (
	"errors"
	"fmt"
	"os"

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
	if err := verifyRecordPathIdentity(
		directory,
		expected,
		sourceName,
	); err != nil {
		return false, err
	}
	fd := int(directory.Fd())
	if replace {
		if err := unix.Renameat(fd, sourceName, fd, targetName); err != nil {
			return false, err
		}
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
		return true, nil
	}
	if err := unix.Linkat(fd, sourceName, fd, targetName, 0); err != nil {
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
	if err := verifyRecordPathIdentity(
		directory,
		expected,
		sourceName,
	); err != nil {
		return true, fmt.Errorf(
			"reverify source record identity: %w",
			err,
		)
	}
	if err := unix.Unlinkat(fd, sourceName, 0); err != nil {
		return true, err
	}
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
	return true, nil
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

func syncRecordDirectory(directory *os.File) error {
	return directory.Sync()
}
