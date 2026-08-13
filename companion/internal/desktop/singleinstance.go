package desktop

import (
	"errors"
	"fmt"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
)

var ErrAlreadyRunning = errors.New("S3 RLCD Deck Companion is already running")

type SingleInstance struct {
	lock *protectedfile.Lock
}

func AcquireSingleInstance(dataDirectory string) (*SingleInstance, error) {
	lock, err := protectedfile.AcquireDirectoryLock(dataDirectory, ".companion.lock")
	if err != nil {
		if errors.Is(err, protectedfile.ErrLockHeld) {
			return nil, fmt.Errorf("%w: %w", ErrAlreadyRunning, err)
		}
		return nil, err
	}
	return &SingleInstance{lock: lock}, nil
}

func (instance *SingleInstance) Close() error {
	if instance == nil || instance.lock == nil {
		return nil
	}
	err := instance.lock.Close()
	instance.lock = nil
	return err
}
