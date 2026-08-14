//go:build !windows

package protectedfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func createPrivateTemp(parent string) (*os.File, error) {
	// os.CreateTemp creates mode 0600 atomically on Unix, independent of the
	// parent directory's permissions.
	return os.CreateTemp(parent, ".protected-*.tmp")
}

func openPrivateRead(path string) (*os.File, error) {
	descriptor, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		_ = unix.Close(descriptor)
		return nil, errors.New("create protected file handle")
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0o600 {
		_ = file.Close()
		return nil, errors.New("protected file handle is not a private regular file")
	}
	current, err := os.Lstat(path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() ||
		!os.SameFile(opened, current) {
		_ = file.Close()
		return nil, errors.New("protected file path changed while opening")
	}
	return file, nil
}

func EnsurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create private directory: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("protect private directory: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("verify private directory permissions: %w", err)
	}
	return nil
}

func EnsurePrivateFile(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("protect private file: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("verify private file permissions: %w", err)
	}
	return nil
}

func verifyPrivate(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("verify private file permissions: %w", err)
	}
	return nil
}

func lockFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func unlockFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}

func replaceFile(from string, to string) (bool, error) {
	if err := os.Rename(from, to); err != nil {
		return false, err
	}
	directory, err := os.Open(filepath.Dir(to))
	if err != nil {
		return true, err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return true, syncErr
	}
	return true, closeErr
}
