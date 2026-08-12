package protectedfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
)

func TestLockIsExclusiveAndReplaceKeepsOwnerOnlyProtection(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private")
	lock, err := protectedfile.AcquireDirectoryLock(directory, ".owner.lock")
	if err != nil {
		t.Fatalf("AcquireDirectoryLock() error = %v", err)
	}
	if _, err = protectedfile.AcquireDirectoryLock(directory, ".owner.lock"); err == nil {
		t.Fatal("second AcquireDirectoryLock() error = nil")
	}
	path := filepath.Join(directory, "state.json")
	committed, err := protectedfile.Replace(path, []byte(`{"generation":1}`))
	if err != nil || !committed {
		t.Fatalf("Replace(first) = %t, %v", committed, err)
	}
	committed, err = protectedfile.Replace(path, []byte(`{"generation":2}`))
	if err != nil || !committed {
		t.Fatalf("Replace(second) = %t, %v", committed, err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != `{"generation":2}` {
		t.Fatalf("state = %q, %v", contents, err)
	}
	if err = lock.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := protectedfile.AcquireDirectoryLock(directory, ".owner.lock")
	if err != nil {
		t.Fatalf("AcquireDirectoryLock(after close) error = %v", err)
	}
	reopened.Close()
}
