//go:build windows

package protectedfile

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var replaceFileW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")

const fileFullControl = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff

func EnsurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create private directory: %w", err)
	}
	return applyAndVerifyCurrentUserACL(path)
}

func EnsurePrivateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("protected file must be a regular non-symlink file: %w", err)
	}
	return applyAndVerifyCurrentUserACL(path)
}

func applyAndVerifyCurrentUserACL(path string) error {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return fmt.Errorf("open current user token: %w", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("read current user SID: %w", err)
	}
	sid := user.User.Sid
	descriptor, err := windows.SecurityDescriptorFromString(
		"D:P(A;OICI;FA;;;" + sid.String() + ")",
	)
	if err != nil {
		return fmt.Errorf("build current-user DACL: %w", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read current-user DACL: %w", err)
	}
	information := windows.SECURITY_INFORMATION(
		windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION,
	)
	if err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, information, nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("set current-user DACL: %w", err)
	}
	verified, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read committed current-user DACL: %w", err)
	}
	if verified == nil {
		return errors.New("verify current-user DACL: security descriptor is missing")
	}
	verifiedDACL, _, err := verified.DACL()
	if err != nil {
		return fmt.Errorf("read committed current-user ACL entries: %w", err)
	}
	if err = verifySingleFullControlACE(verifiedDACL, sid); err != nil {
		return fmt.Errorf("verify current-user DACL: %w", err)
	}
	return nil
}

func verifySingleFullControlACE(dacl *windows.ACL, expectedSID *windows.SID) error {
	if dacl == nil || dacl.AceCount != 1 {
		return fmt.Errorf("expected one access entry, found %d", aclEntryCount(dacl))
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		return fmt.Errorf("read access entry: %w", err)
	}
	if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
		return errors.New("current-user access entry is not allow")
	}
	aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !aceSID.Equals(expectedSID) {
		return errors.New("access entry belongs to a different SID")
	}
	if ace.Mask&fileFullControl != fileFullControl {
		return fmt.Errorf("access entry lacks full control: %#x", ace.Mask)
	}
	return nil
}

func aclEntryCount(dacl *windows.ACL) uint16 {
	if dacl == nil {
		return 0
	}
	return dacl.AceCount
}

func lockFile(file *os.File) error {
	return windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&windows.Overlapped{},
	)
}

func unlockFile(file *os.File) error {
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &windows.Overlapped{})
}

func replaceFile(from string, to string) (bool, error) {
	fromPointer, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return false, err
	}
	toPointer, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return false, err
	}
	if _, statErr := os.Stat(to); errors.Is(statErr, os.ErrNotExist) {
		err = windows.MoveFileEx(fromPointer, toPointer, windows.MOVEFILE_WRITE_THROUGH)
		return err == nil, err
	} else if statErr != nil {
		return false, statErr
	}
	r1, _, callErr := replaceFileW.Call(
		uintptr(unsafe.Pointer(toPointer)),
		uintptr(unsafe.Pointer(fromPointer)),
		0,
		0,
		0,
		0,
	)
	if r1 == 0 {
		return false, callErr
	}
	return true, nil
}
