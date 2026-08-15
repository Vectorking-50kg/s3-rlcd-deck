//go:build !windows

package installation

import (
	"errors"

	"golang.org/x/sys/unix"
)

func diskAvailableBytes(path string) (uint64, error) {
	var status unix.Statfs_t
	if err := unix.Statfs(path, &status); err != nil {
		return 0, err
	}
	blockSize := uint64(status.Bsize)
	availableBlocks := uint64(status.Bavail)
	if blockSize != 0 && availableBlocks > ^uint64(0)/blockSize {
		return 0, errors.New("filesystem capacity is out of range")
	}
	return blockSize * availableBlocks, nil
}
