package protectedfile

import (
	"errors"
	"fmt"
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

func Replace(path string, contents []byte) (bool, error) {
	if path == "" {
		return false, errors.New("protected file path is required")
	}
	parent := filepath.Dir(filepath.Clean(path))
	if err := EnsurePrivateDirectory(parent); err != nil {
		return false, err
	}
	temporary, err := os.CreateTemp(parent, ".protected-*.tmp")
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
	committed, err := replaceFile(temporaryPath, filepath.Clean(path))
	if err != nil {
		return committed, fmt.Errorf("commit protected transaction: %w", err)
	}
	removeTemporary = false
	if err = EnsurePrivateFile(filepath.Clean(path)); err != nil {
		return true, fmt.Errorf("verify committed file protection: %w", err)
	}
	return true, nil
}
