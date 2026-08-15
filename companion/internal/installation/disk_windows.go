//go:build windows

package installation

import "golang.org/x/sys/windows"

func diskAvailableBytes(path string) (uint64, error) {
	encoded, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var available uint64
	if err = windows.GetDiskFreeSpaceEx(encoded, &available, nil, nil); err != nil {
		return 0, err
	}
	return available, nil
}
