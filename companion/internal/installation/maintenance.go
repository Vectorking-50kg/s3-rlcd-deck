package installation

import (
	"errors"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
)

const maintenanceLockName = ".installation-maintenance.lock"

// Maintenance is held by a mutating installer while it owns the normal
// runtime instance lock. A freshly bootstrapped login process can distinguish
// that short transaction from a genuinely running second instance and wait
// for the transaction to publish its final state.
type Maintenance struct {
	lock *protectedfile.Lock
}

func AcquireMaintenance(dataDirectory string) (*Maintenance, error) {
	lock, err := protectedfile.AcquireDirectoryLock(dataDirectory, maintenanceLockName)
	if err != nil {
		return nil, err
	}
	return &Maintenance{lock: lock}, nil
}

// MaintenanceActive probes without changing persistent state. A false result
// means callers must treat an occupied runtime lock as another real instance.
func MaintenanceActive(dataDirectory string) (bool, error) {
	lock, err := protectedfile.AcquireDirectoryLock(dataDirectory, maintenanceLockName)
	if err != nil {
		if errors.Is(err, protectedfile.ErrLockHeld) {
			return true, nil
		}
		return false, err
	}
	return false, lock.Close()
}

func (maintenance *Maintenance) Close() error {
	if maintenance == nil || maintenance.lock == nil {
		return nil
	}
	err := maintenance.lock.Close()
	maintenance.lock = nil
	return err
}
