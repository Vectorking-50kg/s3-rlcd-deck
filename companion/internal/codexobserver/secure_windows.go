//go:build windows

package codexobserver

import (
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func secureOpenRoot(path string) (*os.File, error) {
	if strings.HasPrefix(filepath.VolumeName(path), `\\`) {
		return nil, ErrInvalidConfig
	}
	name, err := windows.NewNTUnicodeString(`\??\` + filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	return openWindowsObject(0, name, path, true)
}

func secureOpenChild(parent *os.File, name string, directory bool) (*os.File, error) {
	if name == "." || name == ".." || filepath.Base(name) != name {
		return nil, ErrInvalidConfig
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	return openWindowsObject(
		windows.Handle(parent.Fd()),
		objectName,
		filepath.Join(parent.Name(), name),
		directory,
	)
}

func openWindowsObject(
	root windows.Handle,
	name *windows.NTUnicodeString,
	displayPath string,
	directory bool,
) (*os.File, error) {
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: root,
		ObjectName:    name,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	options := uint32(windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if directory {
		options |= windows.FILE_DIRECTORY_FILE
	} else {
		options |= windows.FILE_NON_DIRECTORY_FILE
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err := windows.NtCreateFile(
		&handle,
		windows.FILE_GENERIC_READ,
		attributes,
		&status,
		nil,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		options,
		0,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), displayPath)
	info, err := file.Stat()
	if err != nil || directory != info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		!directory && !info.Mode().IsRegular() {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrInvalidConfig
	}
	return file, nil
}
