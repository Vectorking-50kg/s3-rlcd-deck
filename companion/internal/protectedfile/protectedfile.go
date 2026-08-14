package protectedfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var ErrLockHeld = errors.New("protected lock is already held")

type Lock struct {
	file *os.File
}

func AcquireDirectoryLock(directory string, name string) (*Lock, error) {
	if directory == "" || name == "" || filepath.Base(name) != name {
		return nil, errors.New("protected lock requires a directory and simple name")
	}
	if err := EnsurePrivateDirectory(directory); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open protected lock: %w", err)
	}
	if err = EnsurePrivateFile(path); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err = lockFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: %w", ErrLockHeld, err)
	}
	return &Lock{file: file}, nil
}

func (lock *Lock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	unlockErr := unlockFile(lock.file)
	closeErr := lock.file.Close()
	lock.file = nil
	return errors.Join(unlockErr, closeErr)
}

// VerifyPrivate exposes the platform protection contract for tests and callers
// that must audit persisted state. Unix checks owner-only mode; Windows checks
// the protected single-user DACL.
func VerifyPrivate(path string) error {
	return verifyPrivate(path)
}

// Read opens a private regular file without following a symbolic link or
// Windows reparse point, verifies a stable path-to-handle identity, and reads
// at most maximumBytes. The returned bytes are caller-owned.
func Read(path string, maximumBytes int) ([]byte, error) {
	if path == "" || maximumBytes <= 0 {
		return nil, errors.New("protected read requires a path and size limit")
	}
	path = filepath.Clean(path)
	file, err := openPrivateRead(path)
	if err != nil {
		return nil, fmt.Errorf("open protected file: %w", err)
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, int64(maximumBytes)+1))
	if err != nil || len(contents) > maximumBytes {
		for index := range contents {
			contents[index] = 0
		}
		if err == nil {
			err = errors.New("protected file exceeds size limit")
		}
		return nil, fmt.Errorf("read protected file: %w", err)
	}
	return contents, nil
}

func Replace(path string, contents []byte) (bool, error) {
	return replace(path, contents, true)
}

// ReplaceFile atomically writes one owner-only file inside an existing user
// selected directory. Unlike Replace, it never changes the parent directory's
// mode or ACL.
func ReplaceFile(path string, contents []byte) (bool, error) {
	return replace(path, contents, false)
}

func replace(path string, contents []byte, protectParent bool) (bool, error) {
	if path == "" {
		return false, errors.New("protected file path is required")
	}
	path = filepath.Clean(path)
	parent := filepath.Dir(path)
	if protectParent {
		if err := EnsurePrivateDirectory(parent); err != nil {
			return false, err
		}
	} else {
		parentInfo, err := os.Lstat(parent)
		if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
			return false, errors.New("protected file parent must be an existing directory")
		}
		if targetInfo, targetErr := os.Lstat(path); targetErr == nil {
			if !targetInfo.Mode().IsRegular() || targetInfo.Mode()&os.ModeSymlink != 0 {
				return false, errors.New("protected file target must be a regular non-symlink file")
			}
		} else if !errors.Is(targetErr, os.ErrNotExist) {
			return false, errors.New("protected file target is unavailable")
		}
	}
	temporary, err := createPrivateTemp(parent)
	if err != nil {
		return false, fmt.Errorf("create protected transaction: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err = EnsurePrivateFile(temporaryPath); err == nil {
		_, err = temporary.Write(contents)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return false, fmt.Errorf("write protected transaction: %w", err)
	}
	committed, err := replaceFile(temporaryPath, path)
	if err != nil {
		return committed, fmt.Errorf("commit protected transaction: %w", err)
	}
	removeTemporary = false
	if err = EnsurePrivateFile(path); err != nil {
		return true, fmt.Errorf("verify committed file protection: %w", err)
	}
	return true, nil
}
