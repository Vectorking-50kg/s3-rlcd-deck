package pairing_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairing"
)

func TestFileStorePersistsOnlyVerifiersAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "pairing.json")
	store, err := pairing.OpenFileStore(path)
	if err != nil {
		t.Fatalf("OpenFileStore() error = %v", err)
	}
	clock := &fakeClock{now: time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC)}
	service := newService(t, clock, store, &fakeRandom{
		numbers: []int{123},
		tokens:  [][]byte{makeTokenBytes(8)},
	})
	issued, err := service.Issue(context.Background())
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	request := validRedeemRequest(issued.Code)
	credential, err := service.Redeem(context.Background(), request)
	if err != nil {
		t.Fatalf("Redeem() error = %v", err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	for name, secret := range map[string]string{
		"pairing code":    issued.Code,
		"device token":    credential.Token,
		"device identity": request.DeviceIdentity,
	} {
		if strings.Contains(string(contents), secret) {
			t.Fatalf("persistent store contains plaintext %s", name)
		}
	}
	if !strings.Contains(string(contents), `"action":"pairing_redeem"`) {
		t.Fatalf("persistent store is missing the redacted pairing audit event: %s", contents)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("store permissions = %o, want 600", info.Mode().Perm())
	}

	reopened, err := pairing.OpenFileStore(path)
	if err != nil {
		t.Fatalf("reopen FileStore error = %v", err)
	}
	restarted := newService(t, clock, reopened, &fakeRandom{})
	valid, err := restarted.Verify(context.Background(), request.DeviceID, credential.Token)
	if err != nil || !valid {
		t.Fatalf("Verify() after restart = %t, %v; want true", valid, err)
	}
}

func TestFileStoreRejectsMalformedStateWithoutReplacingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pairing.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"codes":`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := pairing.OpenFileStore(path); err == nil {
		t.Fatal("OpenFileStore() error = nil, want malformed state failure")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(contents) != `{"schema_version":1,"codes":` {
		t.Fatal("malformed state was overwritten")
	}
}
