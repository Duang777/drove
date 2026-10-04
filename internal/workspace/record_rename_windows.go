//go:build windows

package workspace

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type recordRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

func renameRecordFile(
	directory *os.File,
	temporary *os.File,
	temporaryName string,
	recordName string,
) (result error) {
	renaming, err := openRecordForRename(directory, temporaryName)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, renaming.Close())
	}()
	writtenInfo, err := temporary.Stat()
	if err != nil {
		return fmt.Errorf("inspect written temporary record: %w", err)
	}
	renamingInfo, err := renaming.Stat()
	if err != nil {
		return fmt.Errorf("inspect rename temporary record: %w", err)
	}
	if !os.SameFile(writtenInfo, renamingInfo) {
		return errors.New("temporary record changed before rename")
	}

	name, err := windows.UTF16FromString(recordName)
	if err != nil {
		return err
	}
	nameLength := (len(name) - 1) * 2
	var header recordRenameInformation
	size := int(unsafe.Offsetof(header.FileName)) + nameLength
	buffer := make([]byte, size)
	information := (*recordRenameInformation)(unsafe.Pointer(&buffer[0]))
	information.ReplaceIfExists = windows.FILE_RENAME_REPLACE_IF_EXISTS |
		windows.FILE_RENAME_POSIX_SEMANTICS
	information.RootDirectory = windows.Handle(directory.Fd())
	information.FileNameLength = uint32(nameLength)
	target := unsafe.Slice(&information.FileName[0], nameLength/2)
	copy(target, name)
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(
		windows.Handle(renaming.Fd()),
		&status,
		&buffer[0],
		uint32(size),
		windows.FileRenameInformation,
	)
}

func openRecordForRename(directory *os.File, name string) (*os.File, error) {
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
		windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE,
		attributes,
		&status,
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		windows.FILE_SYNCHRONOUS_IO_NONALERT|
			windows.FILE_NON_DIRECTORY_FILE|
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
		return nil, errors.New("create rename file from Windows handle")
	}
	return file, nil
}

func syncRecordDirectory(*os.File) error {
	return nil
}
