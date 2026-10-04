//go:build windows

package workspace

import (
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
	_ string,
	recordName string,
) error {
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
		windows.Handle(temporary.Fd()),
		&status,
		&buffer[0],
		uint32(size),
		windows.FileRenameInformation,
	)
}

func syncRecordDirectory(*os.File) error {
	return nil
}
