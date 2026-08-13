package pairing_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairing"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
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
	if err = protectedfile.VerifyPrivate(path); err != nil {
		t.Fatalf("store protection = %v", err)
	}
	if err = store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := pairing.OpenFileStore(path)
	if err != nil {
		t.Fatalf("reopen FileStore error = %v", err)
	}
	restarted := newService(t, clock, reopened, &fakeRandom{})
	valid, err := restarted.Verify(context.Background(), authentication(request, credential.Token))
	if err != nil || !valid {
		t.Fatalf("Verify() after restart = %t, %v; want true", valid, err)
	}
	if err = reopened.Close(); err != nil {
		t.Fatalf("Close(reopened) error = %v", err)
	}
}

func TestFileStoreExclusivelyOwnsItsDataDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pairing.json")
	first, err := pairing.OpenFileStore(path)
	if err != nil {
		t.Fatalf("OpenFileStore(first) error = %v", err)
	}
	defer first.Close()
	if _, err = pairing.OpenFileStore(path); err == nil {
		t.Fatal("OpenFileStore(second) error = nil, stale instances could overwrite revocation")
	}
	if err = first.Close(); err != nil {
		t.Fatalf("Close(first) error = %v", err)
	}
	second, err := pairing.OpenFileStore(path)
	if err != nil {
		t.Fatalf("OpenFileStore(after close) error = %v", err)
	}
	second.Close()
}

func TestFileStoreRepairsExistingUnixPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows protection is verified through the DACL adapter")
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "pairing.json")
	contents := []byte(`{"schema_version":1,"codes":{},"trusts":{},"audit":[]}`)
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	store, err := pairing.OpenFileStore(path)
	if err != nil {
		t.Fatalf("OpenFileStore() error = %v", err)
	}
	defer store.Close()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("repaired mode = %o, %v; want 600", info.Mode().Perm(), err)
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

func TestPairingCodeVerifierIsPepperedAndRestartInvalidatesOutstandingCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pairing.json")
	store, err := pairing.OpenFileStore(path)
	if err != nil {
		t.Fatalf("OpenFileStore() error = %v", err)
	}
	clock := &fakeClock{now: time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC)}
	firstRandom := &fakeRandom{numbers: []int{456789}}
	first, err := pairing.New(pairing.Config{
		Clock:                  clock,
		Store:                  store,
		Random:                 firstRandom,
		CodePepper:             makeTokenBytes(0xa1),
		CertificateFingerprint: testCertificateFingerprint,
		CertificateDER:         []byte(testCertificateDER),
	})
	if err != nil {
		t.Fatalf("pairing.New(first) error = %v", err)
	}
	issued, err := first.Issue(context.Background())
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	plainDigest := sha256.Sum256([]byte(issued.Code))
	if strings.Contains(string(contents), hex.EncodeToString(plainDigest[:])) {
		t.Fatal("stored Pairing verifier matches an offline SHA-256 code table")
	}
	if err = store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := pairing.OpenFileStore(path)
	if err != nil {
		t.Fatalf("reopen FileStore error = %v", err)
	}
	defer reopened.Close()
	restarted, err := pairing.New(pairing.Config{
		Clock:                  clock,
		Store:                  reopened,
		Random:                 &fakeRandom{tokens: [][]byte{makeTokenBytes(0xb1)}},
		CodePepper:             makeTokenBytes(0xa2),
		CertificateFingerprint: testCertificateFingerprint,
		CertificateDER:         []byte(testCertificateDER),
	})
	if err != nil {
		t.Fatalf("pairing.New(restarted) error = %v", err)
	}
	if _, err = restarted.Redeem(context.Background(), validRedeemRequest(issued.Code)); !errors.Is(err, pairing.ErrCodeUnavailable) {
		t.Fatalf("Redeem(outstanding code after restart) error = %v, want unavailable", err)
	}
}
