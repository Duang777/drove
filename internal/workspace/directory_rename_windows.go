//go:build windows

package workspace

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func renameDirectoryNoReplace(
	directory *os.File,
	expected os.FileInfo,
	sourceName string,
	_ string,
	targetName string,
) (moved bool, result error) {
	renaming, err := openDirectoryForRename(directory, sourceName)
	if err != nil {
		return false, err
	}
	defer func() {
		result = errors.Join(result, renaming.Close())
	}()
	opened, err := renaming.Stat()
	if err != nil {
		return false, err
	}
	if !opened.IsDir() || !os.SameFile(expected, opened) {
		return false, errors.New("directory changed before rename")
	}
	return renameWindowsHandle(
		directory,
		renaming,
		targetName,
		false,
	)
}

func openDirectoryForRename(
	directory *os.File,
	name string,
) (*os.File, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: windows.Handle(directory.Fd()),
		ObjectName:    objectName,
	}
	var (
		handle windows.Handle
		status windows.IO_STATUS_BLOCK
	)
	err = windows.NtCreateFile(
		&handle,
		windows.DELETE|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		attributes,
		&status,
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		windows.FILE_OPEN,
		windows.FILE_SYNCHRONOUS_IO_NONALERT|
			windows.FILE_DIRECTORY_FILE|
			windows.FILE_OPEN_REPARSE_POINT,
		0,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), name)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("create rename directory from Windows handle")
	}
	return file, nil
}
