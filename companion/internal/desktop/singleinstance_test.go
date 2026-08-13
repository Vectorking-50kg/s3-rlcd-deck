package desktop

import (
	"errors"
	"testing"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
)

func TestSingleInstanceRejectsSecondOwner(t *testing.T) {
	directory := t.TempDir()
	first, err := AcquireSingleInstance(directory)
	if err != nil {
		t.Fatalf("first AcquireSingleInstance() error = %v", err)
	}
	defer first.Close()
	second, err := AcquireSingleInstance(directory)
	if second != nil || !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second instance = %#v, error = %v, want ErrAlreadyRunning", second, err)
	}
	if !errors.Is(err, protectedfile.ErrLockHeld) {
		t.Fatalf("second error = %v, want protected lock cause", err)
	}
}
