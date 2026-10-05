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
	replace bool,
) (installed bool, result error) {
	renaming, err := openRecordForRename(directory, temporaryName)
	if err != nil {
		return false, err
	}
	defer func() {
		result = errors.Join(result, renaming.Close())
	}()
	writtenInfo, err := temporary.Stat()
	if err != nil {
		return false, fmt.Errorf("inspect written temporary record: %w", err)
	}
	renamingInfo, err := renaming.Stat()
	if err != nil {
		return false, fmt.Errorf("inspect rename temporary record: %w", err)
	}
	if !os.SameFile(writtenInfo, renamingInfo) {
		return false, errors.New("temporary record changed before rename")
	}

	installed, err = renameWindowsHandle(
		directory,
		renaming,
		recordName,
		replace,
	)
	if err != nil {
		return installed, err
	}
	return true, nil
}

func moveRecordFile(
	directory *os.File,
	source *os.File,
	sourceName string,
	targetName string,
) (bool, error) {
	return renameRecordFile(
		directory,
		source,
		sourceName,
		targetName,
		false,
	)
}

func renameWindowsHandle(
	directory *os.File,
	renaming *os.File,
	recordName string,
	replace bool,
) (bool, error) {
	name, err := windows.UTF16FromString(recordName)
	if err != nil {
		return false, err
	}
	nameLength := (len(name) - 1) * 2
	var header recordRenameInformation
	size := int(unsafe.Offsetof(header.FileName)) + nameLength
	buffer := make([]byte, size)
	information := (*recordRenameInformation)(unsafe.Pointer(&buffer[0]))
	if replace {
		information.ReplaceIfExists = 1
	}
	information.RootDirectory = windows.Handle(directory.Fd())
	information.FileNameLength = uint32(nameLength)
	target := unsafe.Slice(&information.FileName[0], nameLength/2)
	copy(target, name)
	var status windows.IO_STATUS_BLOCK
	if err := windows.NtSetInformationFile(
		windows.Handle(renaming.Fd()),
		&status,
		&buffer[0],
		uint32(size),
		windows.FileRenameInformation,
	); err != nil {
		return false, err
	}
	return true, nil
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
		windows.FILE_READ_ATTRIBUTES|
			windows.DELETE|
			windows.SYNCHRONIZE,
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

func openRecordDirectory(root *os.Root) (_ *os.File, result error) {
	anchor, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() {
		if anchor != nil {
			result = errors.Join(result, anchor.Close())
		}
	}()
	anchorInfo, err := anchor.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect directory anchor: %w", err)
	}
	objectName, err := windows.NewNTUnicodeString(".")
	if err != nil {
		return nil, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: windows.Handle(anchor.Fd()),
		ObjectName:    objectName,
	}
	var (
		handle windows.Handle
		status windows.IO_STATUS_BLOCK
	)
	err = windows.NtCreateFile(
		&handle,
		windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE,
		attributes,
		&status,
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
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
	directory := os.NewFile(uintptr(handle), ".")
	if directory == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("create directory file from Windows handle")
	}
	openedInfo, err := directory.Stat()
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("inspect writable directory handle: %w", err),
			directory.Close(),
		)
	}
	if !openedInfo.IsDir() || !os.SameFile(anchorInfo, openedInfo) {
		return nil, errors.Join(
			errors.New("writable directory handle changed identity"),
			directory.Close(),
		)
	}
	closeErr := anchor.Close()
	anchor = nil
	if closeErr != nil {
		return nil, errors.Join(closeErr, directory.Close())
	}
	return directory, nil
}

func syncRecordDirectory(directory *os.File) error {
	if err := windows.FlushFileBuffers(
		windows.Handle(directory.Fd()),
	); err != nil {
		return fmt.Errorf("flush directory metadata: %w", err)
	}
	return nil
}
