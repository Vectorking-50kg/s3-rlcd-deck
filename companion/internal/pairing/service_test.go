package pairing_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairing"
)

const testCertificateFingerprint = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type fakeClock struct{ now time.Time }

func (clock *fakeClock) Now() time.Time { return clock.now }

type fakeRandom struct {
	mu      sync.Mutex
	numbers []int
	tokens  [][]byte
}

type failingConsumeStore struct {
	*pairing.MemoryStore
	fail bool
}

func (store *failingConsumeStore) ConsumeCode(
	ctx context.Context,
	codeVerifier string,
	now time.Time,
	trust pairing.StoredTrust,
) error {
	if store.fail {
		return errors.New("injected storage failure")
	}
	return store.MemoryStore.ConsumeCode(ctx, codeVerifier, now, trust)
}

type captureAuditor struct {
	mu     sync.Mutex
	events []pairing.AuditEvent
}

func (auditor *captureAuditor) Record(event pairing.AuditEvent) {
	auditor.mu.Lock()
	defer auditor.mu.Unlock()
	auditor.events = append(auditor.events, event)
}

func (random *fakeRandom) Number(_ int) (int, error) {
	random.mu.Lock()
	defer random.mu.Unlock()
	if len(random.numbers) == 0 {
		return 0, errors.New("no fake number")
	}
	value := random.numbers[0]
	random.numbers = random.numbers[1:]
	return value, nil
}

func (random *fakeRandom) Bytes(_ int) ([]byte, error) {
	random.mu.Lock()
	defer random.mu.Unlock()
	if len(random.tokens) == 0 {
		return nil, errors.New("no fake bytes")
	}
	value := random.tokens[0]
	random.tokens = random.tokens[1:]
	return append([]byte(nil), value...), nil
}

func TestPairingCodeIsZeroPaddedShortLivedAndSingleUse(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC)}
	store := pairing.NewMemoryStore()
	service := newService(t, clock, store, &fakeRandom{
		numbers: []int{7},
		tokens:  [][]byte{makeTokenBytes(1), makeTokenBytes(2)},
	})

	issued, err := service.Issue(context.Background())
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if issued.Code != "000007" {
		t.Fatalf("code = %q, want zero-padded 000007", issued.Code)
	}
	if !issued.ExpiresAt.Equal(clock.now.Add(5 * time.Minute)) {
		t.Fatalf("expires_at = %s, want five-minute TTL", issued.ExpiresAt)
	}

	request := validRedeemRequest(issued.Code)
	credential, err := service.Redeem(context.Background(), request)
	if err != nil {
		t.Fatalf("Redeem() error = %v", err)
	}
	if credential.Token == "" || credential.DeviceID != request.DeviceID ||
		credential.CertificateFingerprint != testCertificateFingerprint {
		t.Fatalf("credential = %#v", credential)
	}
	if _, err = service.Redeem(context.Background(), request); !errors.Is(err, pairing.ErrCodeUnavailable) {
		t.Fatalf("replay error = %v, want ErrCodeUnavailable", err)
	}
	valid, err := service.Verify(context.Background(), authentication(request, credential.Token))
	if err != nil || !valid {
		t.Fatalf("Verify() = %t, %v; want true", valid, err)
	}
	valid, err = service.Verify(context.Background(), authentication(request, "wrong-device-token"))
	if err != nil || valid {
		t.Fatalf("Verify(wrong token) = %t, %v; want false", valid, err)
	}
	wrongIdentity := authentication(request, credential.Token)
	wrongIdentity.DeviceIdentity = "ZGlmZmVyZW50LWRldmljZS1wdWJsaWMta2V5"
	valid, err = service.Verify(context.Background(), wrongIdentity)
	if err != nil || valid {
		t.Fatalf("Verify(wrong identity) = %t, %v; want false", valid, err)
	}
	wrongProtocol := authentication(request, credential.Token)
	wrongProtocol.ProtocolVersion++
	valid, err = service.Verify(context.Background(), wrongProtocol)
	if err != nil || valid {
		t.Fatalf("Verify(wrong protocol) = %t, %v; want false", valid, err)
	}
	stored, err := store.LookupTrust(context.Background(), request.DeviceID)
	if err != nil {
		t.Fatalf("LookupTrust() error = %v", err)
	}
	if stored.TokenVerifier == "" || stored.TokenVerifier == credential.Token {
		t.Fatalf("stored verifier = %q, must be irreversible and not plaintext", stored.TokenVerifier)
	}
	if stored.DeviceIdentityVerifier == "" || stored.ProtocolVersion != pairing.ProtocolVersion {
		t.Fatalf("stored trust = %#v, want bound identity and protocol", stored)
	}
}

func TestExpiredCodeAndWrongProtocolFailClosed(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC)}
	store := pairing.NewMemoryStore()
	service := newService(t, clock, store, &fakeRandom{
		numbers: []int{42, 43},
		tokens: [][]byte{
			makeTokenBytes(2),
			makeTokenBytes(3),
		},
	})
	issued, err := service.Issue(context.Background())
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	wrongProtocol := validRedeemRequest(issued.Code)
	wrongProtocol.ProtocolVersion = pairing.ProtocolVersion + 1
	if _, err = service.Redeem(context.Background(), wrongProtocol); !errors.Is(err, pairing.ErrUnsupportedProtocol) {
		t.Fatalf("wrong protocol error = %v, want ErrUnsupportedProtocol", err)
	}
	if _, err = service.Redeem(context.Background(), validRedeemRequest(issued.Code)); err != nil {
		t.Fatalf("valid retry after wrong protocol error = %v", err)
	}

	issued, err = service.Issue(context.Background())
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	clock.now = issued.ExpiresAt.Add(time.Nanosecond)
	if _, err = service.Redeem(context.Background(), validRedeemRequest(issued.Code)); !errors.Is(err, pairing.ErrCodeUnavailable) {
		t.Fatalf("expired code error = %v, want ErrCodeUnavailable", err)
	}
}

func TestConcurrentRedeemAllowsExactlyOneConsumer(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC)}
	store := pairing.NewMemoryStore()
	service := newService(t, clock, store, &fakeRandom{
		numbers: []int{314159},
		tokens: [][]byte{
			makeTokenBytes(3),
			makeTokenBytes(4),
		},
	})
	issued, err := service.Issue(context.Background())
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	request := validRedeemRequest(issued.Code)
	results := make(chan error, 2)
	var start sync.WaitGroup
	start.Add(1)
	for range 2 {
		go func() {
			start.Wait()
			_, redeemErr := service.Redeem(context.Background(), request)
			results <- redeemErr
		}()
	}
	start.Done()
	successes := 0
	unavailable := 0
	for range 2 {
		switch redeemErr := <-results; {
		case redeemErr == nil:
			successes++
		case errors.Is(redeemErr, pairing.ErrCodeUnavailable):
			unavailable++
		default:
			t.Fatalf("unexpected Redeem() error = %v", redeemErr)
		}
	}
	if successes != 1 || unavailable != 1 {
		t.Fatalf("successes=%d unavailable=%d, want 1/1", successes, unavailable)
	}
}

func TestRotationCodeDeliversTokenOnlyThroughRedeemAndRevokeTrust(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC)}
	store := pairing.NewMemoryStore()
	service := newService(t, clock, store, &fakeRandom{
		numbers: []int{1, 2},
		tokens: [][]byte{
			makeTokenBytes(5),
			makeTokenBytes(6),
		},
	})
	issued, err := service.Issue(context.Background())
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	credential, err := service.Redeem(context.Background(), validRedeemRequest(issued.Code))
	if err != nil {
		t.Fatalf("Redeem() error = %v", err)
	}
	rotation, err := service.IssueRotation(context.Background(), credential.DeviceID)
	if err != nil {
		t.Fatalf("IssueRotation() error = %v", err)
	}
	rotatedRequest := validRedeemRequest(rotation.Code)
	rotated, err := service.Redeem(context.Background(), rotatedRequest)
	if err != nil {
		t.Fatalf("Redeem(rotation) error = %v", err)
	}
	if rotated.Token == credential.Token || rotated.Token == "" {
		t.Fatal("Rotate() did not issue a distinct one-time token")
	}
	valid, _ := service.Verify(context.Background(), authentication(rotatedRequest, credential.Token))
	if valid {
		t.Fatal("old token remained valid after rotation")
	}
	valid, _ = service.Verify(context.Background(), authentication(rotatedRequest, rotated.Token))
	if !valid {
		t.Fatal("rotated token is not valid")
	}
	if err = service.Revoke(context.Background(), credential.DeviceID); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	valid, _ = service.Verify(context.Background(), authentication(rotatedRequest, rotated.Token))
	if valid {
		t.Fatal("token remained valid after device revocation")
	}
}

func TestStorageFailureReturnsNoCredentialAndDoesNotConsumeCode(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC)}
	store := &failingConsumeStore{MemoryStore: pairing.NewMemoryStore(), fail: true}
	service := newService(t, clock, store, &fakeRandom{
		numbers: []int{88},
		tokens: [][]byte{
			makeTokenBytes(9),
			makeTokenBytes(10),
		},
	})
	issued, err := service.Issue(context.Background())
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	credential, err := service.Redeem(context.Background(), validRedeemRequest(issued.Code))
	if err == nil || credential.Token != "" {
		t.Fatalf("Redeem(storage failure) = %#v, %v; want no credential", credential, err)
	}
	store.fail = false
	credential, err = service.Redeem(context.Background(), validRedeemRequest(issued.Code))
	if err != nil || credential.Token == "" {
		t.Fatalf("Redeem(retry) = %#v, %v; code was consumed on failed storage", credential, err)
	}
}

func TestAuditEventsContainOnlyHashedDeviceReferences(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC)}
	store := pairing.NewMemoryStore()
	auditor := &captureAuditor{}
	service, err := pairing.New(pairing.Config{
		Clock:                  clock,
		Store:                  store,
		Random:                 &fakeRandom{numbers: []int{321}, tokens: [][]byte{makeTokenBytes(11)}},
		Auditor:                auditor,
		CodeTTL:                5 * time.Minute,
		CertificateFingerprint: testCertificateFingerprint,
		CodePepper:             makeTokenBytes(0xf0),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	issued, err := service.Issue(context.Background())
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	request := validRedeemRequest(issued.Code)
	credential, err := service.Redeem(context.Background(), request)
	if err != nil {
		t.Fatalf("Redeem() error = %v", err)
	}
	if len(auditor.events) != 2 {
		t.Fatalf("audit events = %#v, want issue and redeem", auditor.events)
	}
	for _, event := range auditor.events {
		serialized := event.Action + event.Outcome + event.DeviceRef
		for name, forbidden := range map[string]string{
			"pairing code": issued.Code,
			"device token": credential.Token,
			"device ID":    request.DeviceID,
			"identity":     request.DeviceIdentity,
		} {
			if strings.Contains(serialized, forbidden) {
				t.Fatalf("audit event contains %s: %#v", name, event)
			}
		}
	}
}

func newService(
	t *testing.T,
	clock pairing.Clock,
	store pairing.Store,
	random pairing.Random,
) *pairing.Service {
	t.Helper()
	service, err := pairing.New(pairing.Config{
		Clock:                  clock,
		Store:                  store,
		Random:                 random,
		CodeTTL:                5 * time.Minute,
		CertificateFingerprint: testCertificateFingerprint,
		CodePepper:             makeTokenBytes(0xf1),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return service
}

func validRedeemRequest(code string) pairing.RedeemRequest {
	return pairing.RedeemRequest{
		Code:            code,
		DeviceID:        "deck-a1b2c3d4",
		DeviceIdentity:  "ZGV2aWNlLXB1YmxpYy1rZXktbWF0ZXJpYWw",
		ProtocolVersion: pairing.ProtocolVersion,
	}
}

func makeTokenBytes(fill byte) []byte {
	value := make([]byte, 32)
	for index := range value {
		value[index] = fill
	}
	return value
}

func authentication(request pairing.RedeemRequest, token string) pairing.Authentication {
	return pairing.Authentication{
		DeviceID:        request.DeviceID,
		Token:           token,
		DeviceIdentity:  request.DeviceIdentity,
		ProtocolVersion: request.ProtocolVersion,
	}
}
