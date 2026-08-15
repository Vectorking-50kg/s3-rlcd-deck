//go:build !darwin && !windows

package codexobserver

import (
	"os"
	"path/filepath"
)

func secureOpenRoot(path string) (*os.File, error) {
	return openVerifiedPath(path, true)
}

func secureOpenChild(parent *os.File, name string, directory bool) (*os.File, error) {
	if name == "." || name == ".." || filepath.Base(name) != name {
		return nil, ErrInvalidConfig
	}
	return openVerifiedPath(filepath.Join(parent.Name(), name), directory)
}

func openVerifiedPath(path string, directory bool) (*os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	current, err := os.Lstat(path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(opened, current) || directory != opened.IsDir() ||
		!directory && !opened.Mode().IsRegular() {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrInvalidConfig
	}
	return file, nil
}
