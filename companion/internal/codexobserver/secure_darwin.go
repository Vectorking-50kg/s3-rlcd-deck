//go:build darwin

package codexobserver

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func secureOpenRoot(path string) (*os.File, error) {
	fd, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func secureOpenChild(parent *os.File, name string, directory bool) (*os.File, error) {
	if name == "." || name == ".." || filepath.Base(name) != name {
		return nil, ErrInvalidConfig
	}
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	if directory {
		flags |= unix.O_DIRECTORY
	} else {
		flags |= unix.O_NONBLOCK
	}
	fd, err := unix.Openat(int(parent.Fd()), name, flags, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Join(parent.Name(), name))
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
