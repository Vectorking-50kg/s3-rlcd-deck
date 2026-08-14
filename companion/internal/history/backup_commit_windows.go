//go:build windows

package history

import (
	"golang.org/x/sys/windows"
)

func commitBackupFile(from string, to string) error {
	fromPointer, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	toPointer, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(
		fromPointer,
		toPointer,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	)
}
