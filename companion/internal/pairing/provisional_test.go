package pairing_test

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairing"
)

const (
	provisionalSessionID     = "00112233445566778899aabbccddeeff"
	provisionalTransactionID = "ffeeddccbbaa99887766554433221100"
)

type failingCommitStore struct {
	*pairing.MemoryStore
	fail bool
}

func (store *failingCommitStore) CommitTrust(ctx context.Context, trust pairing.StoredTrust) error {
	if store.fail {
		return errors.New("injected provisional commit failure")
	}
	return store.MemoryStore.CommitTrust(ctx, trust)
}

func TestProvisionalTrustGrantsOnlyTheRestrictedProbeUntilExactCommit(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)}
	store := pairing.NewMemoryStore()
	service := newService(t, clock, store, &fakeRandom{})
	request, authentication := validProvisional(clock.now)

	if err := service.StageProvisional(context.Background(), request); err != nil {
		t.Fatalf("StageProvisional() error = %v", err)
	}
	if err := service.StageProvisional(context.Background(), request); err != nil {
		t.Fatalf("idempotent StageProvisional() error = %v", err)
	}
	if valid, err := service.Verify(context.Background(), authentication); err != nil || valid {
		t.Fatalf("normal Verify(staged) = %t, %v; want false", valid, err)
	}
	transactionID, valid, err := service.VerifyProvisional(context.Background(), authentication)
	if err != nil || !valid || transactionID != request.TransactionID {
		t.Fatalf("VerifyProvisional() = %q, %t, %v", transactionID, valid, err)
	}

	wrong := authentication
	wrong.Token = base64.RawURLEncoding.EncodeToString(makeTokenBytes(0x55))
	if _, valid, err = service.VerifyProvisional(context.Background(), wrong); err != nil || valid {
		t.Fatalf("VerifyProvisional(wrong token) = %t, %v; want false", valid, err)
	}
	if err = service.CommitProvisional(
		context.Background(),
		request.TransactionID,
		request.DeviceID,
	); err != nil {
		t.Fatalf("CommitProvisional() error = %v", err)
	}
	if valid, err = service.Verify(context.Background(), authentication); err != nil || !valid {
		t.Fatalf("normal Verify(committed) = %t, %v; want true", valid, err)
	}
	if _, valid, err = service.VerifyProvisional(context.Background(), authentication); err != nil || valid {
		t.Fatalf("VerifyProvisional(committed) = %t, %v; want false", valid, err)
	}
	if err = service.CommitProvisional(
		context.Background(),
		request.TransactionID,
		request.DeviceID,
	); !errors.Is(err, pairing.ErrProvisionalNotFound) {
		t.Fatalf("replayed CommitProvisional() error = %v", err)
	}
}

func TestProvisionalTrustExpiresCancelsAndRejectsConflictingSessions(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)}
	service := newService(t, clock, pairing.NewMemoryStore(), &fakeRandom{})
	request, authentication := validProvisional(clock.now)
	if err := service.StageProvisional(context.Background(), request); err != nil {
		t.Fatalf("StageProvisional() error = %v", err)
	}

	conflict := request
	conflict.TransactionID = "11111111111111111111111111111111"
	if err := service.StageProvisional(context.Background(), conflict); !errors.Is(err, pairing.ErrProvisionalConflict) {
		t.Fatalf("conflicting StageProvisional() error = %v", err)
	}
	if !service.CancelProvisional(request.TransactionID) || service.CancelProvisional(request.TransactionID) {
		t.Fatal("CancelProvisional() did not consume exactly one transaction")
	}
	if _, valid, err := service.VerifyProvisional(context.Background(), authentication); err != nil || valid {
		t.Fatalf("VerifyProvisional(cancelled) = %t, %v; want false", valid, err)
	}

	if err := service.StageProvisional(context.Background(), request); err != nil {
		t.Fatalf("restage error = %v", err)
	}
	clock.now = request.ExpiresAt
	if _, valid, err := service.VerifyProvisional(context.Background(), authentication); err != nil || valid {
		t.Fatalf("VerifyProvisional(expired) = %t, %v; want false", valid, err)
	}
	if err := service.CommitProvisional(
		context.Background(), request.TransactionID, request.DeviceID,
	); !errors.Is(err, pairing.ErrProvisionalNotFound) {
		t.Fatalf("CommitProvisional(expired) error = %v", err)
	}
}

func TestFailedDurableCommitKeepsBoundedProvisionalTrustForRetry(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)}
	store := &failingCommitStore{MemoryStore: pairing.NewMemoryStore(), fail: true}
	service := newService(t, clock, store, &fakeRandom{})
	request, authentication := validProvisional(clock.now)
	if err := service.StageProvisional(context.Background(), request); err != nil {
		t.Fatalf("StageProvisional() error = %v", err)
	}
	if err := service.CommitProvisional(
		context.Background(), request.TransactionID, request.DeviceID,
	); err == nil {
		t.Fatal("CommitProvisional(storage failure) error = nil")
	}
	if transactionID, valid, err := service.VerifyProvisional(
		context.Background(), authentication,
	); err != nil || !valid || transactionID != request.TransactionID {
		t.Fatalf("provisional trust was lost after storage failure: %q, %t, %v", transactionID, valid, err)
	}
	store.fail = false
	if err := service.CommitProvisional(
		context.Background(), request.TransactionID, request.DeviceID,
	); err != nil {
		t.Fatalf("CommitProvisional(retry) error = %v", err)
	}
}

func TestFileStoreDoesNotPersistProvisionalSecretsBeforeCommit(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "pairing.json")
	store, err := pairing.OpenFileStore(path)
	if err != nil {
		t.Fatalf("OpenFileStore() error = %v", err)
	}
	defer store.Close()
	clock := &fakeClock{now: time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)}
	service := newService(t, clock, store, &fakeRandom{})
	request, authentication := validProvisional(clock.now)
	if err = service.StageProvisional(context.Background(), request); err != nil {
		t.Fatalf("StageProvisional() error = %v", err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("provisional stage created durable trust state: %v", err)
	}
	if err = service.CommitProvisional(
		context.Background(), request.TransactionID, request.DeviceID,
	); err != nil {
		t.Fatalf("CommitProvisional() error = %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	for name, secret := range map[string]string{
		"token":           request.Token,
		"device identity": request.DeviceIdentity,
		"session ID":      request.SessionID,
		"transaction ID":  request.TransactionID,
	} {
		if strings.Contains(string(contents), secret) {
			t.Fatalf("persistent trust store contains provisional %s", name)
		}
	}
	if valid, verifyErr := service.Verify(context.Background(), authentication); verifyErr != nil || !valid {
		t.Fatalf("Verify(committed file trust) = %t, %v", valid, verifyErr)
	}
}

func validProvisional(now time.Time) (pairing.ProvisionalTrustRequest, pairing.Authentication) {
	deviceIdentity := "ZGV2aWNlLXB1YmxpYy1rZXktbWF0ZXJpYWw"
	token := base64.RawURLEncoding.EncodeToString(makeTokenBytes(0x44))
	request := pairing.ProvisionalTrustRequest{
		SessionID: provisionalSessionID, TransactionID: provisionalTransactionID,
		DeviceID: "deck-a1b2c3d4", DeviceIdentity: deviceIdentity, Token: token,
		ProtocolVersion: pairing.ProtocolVersion, ExpiresAt: now.Add(2 * time.Minute),
	}
	return request, pairing.Authentication{
		DeviceID: request.DeviceID, DeviceIdentity: deviceIdentity, Token: token,
		ProtocolVersion: pairing.ProtocolVersion,
	}
}
