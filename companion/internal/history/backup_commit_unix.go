//go:build !windows

package history

import (
	"errors"
	"os"
	"path/filepath"
)

func commitBackupFile(from string, to string) error {
	if err := os.Rename(from, to); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(to))
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
