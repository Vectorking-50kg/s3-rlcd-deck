//go:build windows

package protectedfile

import (
	"os"
	"testing"
)

func TestPrivateTemporaryFileHasCurrentUserDACLAtCreation(t *testing.T) {
	file, err := createPrivateTemp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := file.Name()
	defer os.Remove(path)
	defer file.Close()
	if err = verifyPrivate(path); err != nil {
		t.Fatalf("new temporary file is not current-user only: %v", err)
	}
}
