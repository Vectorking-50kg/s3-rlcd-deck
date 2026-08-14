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
	protectedContents, err := protectedfile.Read(path, 1024)
	if err != nil || string(protectedContents) != `{"generation":2}` {
		t.Fatalf("protected read = %q, %v", protectedContents, err)
	}
	if _, err = protectedfile.Read(path, 2); err == nil {
		t.Fatal("oversized protected read was accepted")
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

func TestReplaceFileProtectsOnlyTheExportAndPreservesParentMode(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "shared-export-directory")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "backup.age")
	committed, err := protectedfile.ReplaceFile(path, []byte("encrypted-backup"))
	if err != nil || !committed {
		t.Fatalf("ReplaceFile() = %t, %v", committed, err)
	}
	after, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode().Perm() != after.Mode().Perm() {
		t.Fatalf("parent mode changed from %o to %o", before.Mode().Perm(), after.Mode().Perm())
	}
	if err = protectedfile.VerifyPrivate(path); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "linked.age")
	if err = os.Symlink(path, link); err == nil {
		if _, err = protectedfile.ReplaceFile(link, []byte("replacement")); err == nil {
			t.Fatal("ReplaceFile accepted a symbolic-link target")
		}
	}
}
