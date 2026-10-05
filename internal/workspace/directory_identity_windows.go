//go:build windows

package workspace

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func openedDirectoryIdentity(root *os.Root) (result string, resultErr error) {
	directory, err := root.Open(".")
	if err != nil {
		return "", err
	}
	defer func() {
		resultErr = errors.Join(resultErr, directory.Close())
	}()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(
		windows.Handle(directory.Fd()),
		&info,
	); err != nil {
		return "", err
	}
	index := uint64(info.FileIndexHigh)<<32 |
		uint64(info.FileIndexLow)
	return fmt.Sprintf(
		"windows:%x:%x",
		info.VolumeSerialNumber,
		index,
	), nil
}
