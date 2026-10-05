//go:build darwin || linux

package workspace

import (
	"errors"
	"fmt"
	"os"

	"github.com/google/uuid"
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
	targetName string,
	rename directoryRenameNoReplace,
) (bool, error) {
	fd := int(directory.Fd())
	isolatedName := ".drove-directory-" + uuid.NewString()
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
		return nil
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
