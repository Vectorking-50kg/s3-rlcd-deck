package secretstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"runtime"
	"testing"
)

// TestNativeSecretStore is enabled by the desktop workflow on real macOS and
// Windows runners. It proves create, replace, enumerate, read, and cleanup
// against the current user's actual platform vault without requesting UI.
func TestNativeSecretStore(t *testing.T) {
	if os.Getenv("S3DECK_NATIVE_SECRET_STORE_TEST") != "1" {
		t.Skip("native platform-vault test is disabled")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Fatal("native platform-vault test enabled on unsupported platform")
	}
	store, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first := []byte("s3deck-native-secret-canary-first")
	second := []byte("s3deck-native-secret-canary-second")
	defer overwrite(first)
	defer overwrite(second)
	reference, err := store.Put(ctx, "", first)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cleanupErr := store.Delete(context.Background(), reference); cleanupErr != nil {
			t.Errorf("native secret cleanup: %v", cleanupErr)
		}
	})
	read, err := store.Get(ctx, reference)
	if err != nil || !bytes.Equal(read, first) {
		overwrite(read)
		t.Fatalf("native Get(create) mismatch: %v", err)
	}
	overwrite(read)
	if _, err = store.Put(ctx, reference, second); err != nil {
		t.Fatal(err)
	}
	read, err = store.Get(ctx, reference)
	if err != nil || !bytes.Equal(read, second) {
		overwrite(read)
		t.Fatalf("native Get(update) mismatch: %v", err)
	}
	overwrite(read)
	metadata, err := store.ListMetadata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range metadata {
		if item.Reference == reference {
			found = true
		}
	}
	if !found {
		t.Fatal("native ListMetadata omitted the created reference")
	}
	if err = store.Delete(ctx, reference); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(ctx, reference); !errors.Is(err, ErrNotFound) {
		t.Fatalf("native Get(deleted) error = %v", err)
	}
	metadata, err = store.ListMetadata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range metadata {
		if item.Reference == reference {
			t.Fatal("native cleanup left the deleted reference behind")
		}
	}
}
