//go:build windows

package workspace

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func configureIncludeManifestCommand(
	_ *exec.Cmd,
	manifest *os.File,
	path string,
) (string, bool, func() error, error) {
	expected, err := manifest.Stat()
	if err != nil {
		return "", false, nil, fmt.Errorf(
			"workspace: inspect opened include manifest: %w",
			err,
		)
	}
	name, err := windows.UTF16PtrFromString(filepath.Clean(path))
	if err != nil {
		return "", false, nil, fmt.Errorf(
			"workspace: encode include manifest path: %w",
			err,
		)
	}
	handle, err := windows.CreateFile(
		name,
		windows.FILE_GENERIC_READ,
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|
			windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return "", false, nil, fmt.Errorf(
			"workspace: lock include manifest: %w",
			err,
		)
	}
	var handleInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(
		handle,
		&handleInfo,
	); err != nil {
		_ = windows.CloseHandle(handle)
		return "", false, nil, fmt.Errorf(
			"workspace: inspect include manifest handle: %w",
			err,
		)
	}
	if handleInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		return "", false, nil, errors.New(
			"workspace: include manifest is a reparse point",
		)
	}
	guard := os.NewFile(uintptr(handle), path)
	if guard == nil {
		_ = windows.CloseHandle(handle)
		return "", false, nil, errors.New(
			"workspace: wrap include manifest guard",
		)
	}
	locked, err := guard.Stat()
	if err != nil {
		_ = guard.Close()
		return "", false, nil, fmt.Errorf(
			"workspace: inspect locked include manifest: %w",
			err,
		)
	}
	if !locked.Mode().IsRegular() || !os.SameFile(expected, locked) {
		_ = guard.Close()
		return "", false, nil, errors.New(
			"workspace: include manifest changed while locking",
		)
	}
	return path, false, guard.Close, nil
}
