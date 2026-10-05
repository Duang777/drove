//go:build windows

package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func rootedGitCommand(
	ctx context.Context,
	git string,
	path string,
	root *os.Root,
	arguments []string,
) (*exec.Cmd, func() error, error) {
	opened, err := root.Stat(".")
	if err != nil {
		return nil, nil, fmt.Errorf(
			"workspace: inspect rooted Git directory: %w",
			err,
		)
	}
	name, err := windows.UTF16PtrFromString(filepath.Clean(path))
	if err != nil {
		return nil, nil, fmt.Errorf(
			"workspace: encode rooted Git directory: %w",
			err,
		)
	}
	handle, err := windows.CreateFile(
		name,
		windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return nil, nil, fmt.Errorf(
			"workspace: lock rooted Git directory: %w",
			err,
		)
	}
	lock := os.NewFile(uintptr(handle), path)
	if lock == nil {
		_ = windows.CloseHandle(handle)
		return nil, nil, errors.New(
			"workspace: wrap rooted Git directory handle",
		)
	}
	locked, err := lock.Stat()
	if err != nil {
		_ = lock.Close()
		return nil, nil, fmt.Errorf(
			"workspace: inspect locked Git directory: %w",
			err,
		)
	}
	if !os.SameFile(opened, locked) {
		_ = lock.Close()
		return nil, nil, errors.New(
			"workspace: rooted Git directory changed while locking",
		)
	}
	command := exec.CommandContext(
		ctx,
		git,
		append(
			[]string{"-c", "core.hooksPath=NUL", "-C", path},
			arguments...,
		)...,
	)
	return command, lock.Close, nil
}
