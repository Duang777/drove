//go:build darwin || linux

package workspace

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

type directoryRenameNoReplace func(
	directoryFD int,
	sourceName string,
	targetName string,
) error

func renameBoundDirectoryNoReplace(
	directory *os.File,
	expected os.FileInfo,
	sourceName string,
	isolatedName string,
	targetName string,
	rename directoryRenameNoReplace,
) (bool, error) {
	if sourceName == isolatedName ||
		sourceName == targetName ||
		isolatedName == targetName {
		return false, errors.New(
			"directory rename source, isolation, and target names must differ",
		)
	}
	fd := int(directory.Fd())
	if err := rename(fd, sourceName, isolatedName); err != nil {
		return false, err
	}
	restore := func() error {
		if err := rename(fd, isolatedName, sourceName); err != nil {
			return fmt.Errorf(
				"restore isolated directory %q: %w",
				sourceName,
				err,
			)
		}
		return syncRecordDirectory(directory)
	}
	if err := syncRecordDirectory(directory); err != nil {
		return false, errors.Join(
			fmt.Errorf(
				"sync isolated directory %q: %w",
				sourceName,
				err,
			),
			restore(),
		)
	}
	isolated, err := statDirectoryAt(directory, isolatedName)
	if err != nil {
		return false, errors.Join(
			fmt.Errorf(
				"inspect isolated directory %q: %w",
				sourceName,
				err,
			),
			restore(),
		)
	}
	if !isolated.IsDir() || !os.SameFile(expected, isolated) {
		return false, errors.Join(
			fmt.Errorf("directory %q changed before rename", sourceName),
			restore(),
		)
	}
	if err := rename(fd, isolatedName, targetName); err != nil {
		return false, errors.Join(err, restore())
	}
	if err := syncRecordDirectory(directory); err != nil {
		return true, fmt.Errorf(
			"sync renamed directory %q: %w",
			targetName,
			err,
		)
	}
	target, err := statDirectoryAt(directory, targetName)
	if err != nil {
		return true, fmt.Errorf(
			"inspect renamed directory %q: %w",
			targetName,
			err,
		)
	}
	if !target.IsDir() || !os.SameFile(expected, target) {
		return true, fmt.Errorf(
			"renamed directory %q changed identity",
			targetName,
		)
	}
	return true, nil
}

func statDirectoryAt(
	directory *os.File,
	name string,
) (_ os.FileInfo, result error) {
	fd, err := unix.Openat(
		int(directory.Fd()),
		name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY,
		0,
	)
	if err != nil {
		return nil, err
	}
	opened := os.NewFile(uintptr(fd), name)
	if opened == nil {
		_ = unix.Close(fd)
		return nil, errors.New("wrap opened directory")
	}
	defer func() {
		result = errors.Join(result, opened.Close())
	}()
	return opened.Stat()
}
