//go:build !windows

package installation

import (
	"context"
	"os"
	"testing"
)

func TestReadOnlyInstallationRootFailsClosed(t *testing.T) {
	manager, _, source := openTestManager(t)
	if err := os.Chmod(manager.root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(manager.root, 0o700) })
	if _, err := manager.Apply(context.Background(), Request{
		SourceExecutable: source, Version: "1.0.0", Commit: "0123456789ab",
		DeviceHubAddress: "127.0.0.1:7780",
	}); err == nil {
		t.Fatal("read-only installation root was accepted")
	}
}
