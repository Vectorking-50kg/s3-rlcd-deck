package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairing"
	companionruntime "github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/runtime"
)

const (
	testCertificateDER         = "test-certificate-der"
	testCertificateFingerprint = "sha256:69be57455b3b4f84c7c23140e875002791c5a5509ca9d0c644a63d5eaf836cce"
)

type failHTTPConsumeStore struct {
	*pairing.MemoryStore
	mu   sync.Mutex
	fail bool
}

func (store *failHTTPConsumeStore) ConsumeCode(
	ctx context.Context,
	codeVerifier string,
	now time.Time,
	trust pairing.StoredTrust,
) error {
	store.mu.Lock()
	fail := store.fail
	store.mu.Unlock()
	if fail {
		return errors.New("injected HTTP store failure")
	}
	return store.MemoryStore.ConsumeCode(ctx, codeVerifier, now, trust)
}

func (store *failHTTPConsumeStore) SetFail(fail bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.fail = fail
}

func TestPairingHTTPFlowIssuesRedeemsRotatesAndRevokesDeviceTrust(t *testing.T) {
	config := testConfig()
	application, err := companionruntime.New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	status := waitForState(t, application, companionruntime.StateReady)
	client, session, csrf := loginManagement(t, status.ManagementAddress, config.Management.AdminToken)

	issued := issuePairingCode(t, client, status.ManagementAddress, session, csrf)
	credential := redeemPairingCode(t, status.DeviceHubAddress, issued.Code, pairing.ProtocolVersion, http.StatusOK)
	if credential.Token == "" || credential.CertificateFingerprint != testCertificateFingerprint {
		t.Fatalf("pairing credential = %#v", credential)
	}
	devices := listPairedDevices(t, client, status.ManagementAddress, session)
	if len(devices) != 1 || devices[0].DeviceID != credential.DeviceID ||
		devices[0].ProtocolVersion != pairing.ProtocolVersion || devices[0].CreatedAt.IsZero() {
		t.Fatalf("paired devices = %#v, want the redacted paired Deck", devices)
	}
	redeemPairingCode(t, status.DeviceHubAddress, issued.Code, pairing.ProtocolVersion, http.StatusUnauthorized)
	if got := deviceHealthStatus(t, status.DeviceHubAddress, credential.DeviceID, "ZGV2aWNlLXB1YmxpYy1rZXktbWF0ZXJpYWw", pairing.ProtocolVersion, credential.Token); got != http.StatusOK {
		t.Fatalf("paired Device health = %d, want 200", got)
	}
	if got := deviceHealthStatus(t, status.DeviceHubAddress, credential.DeviceID, "ZGlmZmVyZW50LWRldmljZS1wdWJsaWMta2V5", pairing.ProtocolVersion, credential.Token); got != http.StatusUnauthorized {
		t.Fatalf("wrong device identity = %d, want 401", got)
	}
	if got := deviceHealthStatus(t, status.DeviceHubAddress, credential.DeviceID, "ZGV2aWNlLXB1YmxpYy1rZXktbWF0ZXJpYWw", pairing.ProtocolVersion+1, credential.Token); got != http.StatusUnauthorized {
		t.Fatalf("wrong device protocol = %d, want 401", got)
	}
	if got := deviceHealthStatus(t, status.DeviceHubAddress, credential.DeviceID, "ZGV2aWNlLXB1YmxpYy1rZXktbWF0ZXJpYWw", pairing.ProtocolVersion, config.Management.AdminToken); got != http.StatusUnauthorized {
		t.Fatalf("management token on Device Hub = %d, want 401", got)
	}

	rotationCode := rotateDeviceToken(t, client, status.ManagementAddress, credential.DeviceID, session, csrf)
	rotated := redeemPairingCode(t, status.DeviceHubAddress, rotationCode.Code, pairing.ProtocolVersion, http.StatusOK)
	if rotated.Token == "" || rotated.Token == credential.Token {
		t.Fatal("management rotation did not return one distinct device token")
	}
	if got := deviceHealthStatus(t, status.DeviceHubAddress, credential.DeviceID, "ZGV2aWNlLXB1YmxpYy1rZXktbWF0ZXJpYWw", pairing.ProtocolVersion, credential.Token); got != http.StatusUnauthorized {
		t.Fatalf("old token after rotation = %d, want 401", got)
	}
	if got := deviceHealthStatus(t, status.DeviceHubAddress, credential.DeviceID, "ZGV2aWNlLXB1YmxpYy1rZXktbWF0ZXJpYWw", pairing.ProtocolVersion, rotated.Token); got != http.StatusOK {
		t.Fatalf("rotated token health = %d, want 200", got)
	}

	revokeDevice(t, client, status.ManagementAddress, credential.DeviceID, session, csrf)
	if got := deviceHealthStatus(t, status.DeviceHubAddress, credential.DeviceID, "ZGV2aWNlLXB1YmxpYy1rZXktbWF0ZXJpYWw", pairing.ProtocolVersion, rotated.Token); got != http.StatusUnauthorized {
		t.Fatalf("revoked token health = %d, want 401", got)
	}
	if devices = listPairedDevices(t, client, status.ManagementAddress, session); len(devices) != 0 {
		t.Fatalf("paired devices after revoke = %#v, want empty", devices)
	}

	cancel()
	if err = <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func listPairedDevices(
	t *testing.T,
	client *http.Client,
	managementAddress string,
	session *http.Cookie,
) []pairing.TrustView {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "http://"+managementAddress+"/api/v1/devices", nil)
	if err != nil {
		t.Fatalf("NewRequest(list devices) error = %v", err)
	}
	request.AddCookie(session)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("GET devices: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET devices = %d, want 200", response.StatusCode)
	}
	var document struct {
		Devices []pairing.TrustView `json:"devices"`
	}
	if err = json.NewDecoder(response.Body).Decode(&document); err != nil {
		t.Fatalf("decode devices: %v", err)
	}
	return document.Devices
}

func TestPairingRedeemRejectsWrongProtocolWithoutConsumingCode(t *testing.T) {
	config := testConfig()
	application, err := companionruntime.New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	status := waitForState(t, application, companionruntime.StateReady)
	client, session, csrf := loginManagement(t, status.ManagementAddress, config.Management.AdminToken)
	issued := issuePairingCode(t, client, status.ManagementAddress, session, csrf)

	redeemPairingCode(t, status.DeviceHubAddress, issued.Code, pairing.ProtocolVersion+1, http.StatusUpgradeRequired)
	credential := redeemPairingCode(t, status.DeviceHubAddress, issued.Code, pairing.ProtocolVersion, http.StatusOK)
	if credential.Token == "" {
		t.Fatal("valid retry after protocol rejection did not return a token")
	}

	cancel()
	if err = <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestPairingRedeemRateLimitsCodeGuessingByIP(t *testing.T) {
	config := testConfig()
	config.DeviceHub.Limits.PairingAttempts = 1
	config.DeviceHub.Limits.PairingRateWindow = time.Minute
	application, err := companionruntime.New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	status := waitForState(t, application, companionruntime.StateReady)

	redeemPairingCode(t, status.DeviceHubAddress, "999999", pairing.ProtocolVersion, http.StatusUnauthorized)
	redeemPairingCode(t, status.DeviceHubAddress, "888888", pairing.ProtocolVersion, http.StatusTooManyRequests)

	cancel()
	if err = <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestPairingRedeemRejectsChunkedBodyBeyondDeviceLimit(t *testing.T) {
	config := testConfig()
	config.DeviceHub.Limits.MaxBodyBytes = 64
	application, err := companionruntime.New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	status := waitForState(t, application, companionruntime.StateReady)

	reader, writer := io.Pipe()
	request, err := http.NewRequest(
		http.MethodPost,
		"http://"+status.DeviceHubAddress+"/api/v1/pairing/redeem",
		reader,
	)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.ContentLength = -1
	request.Header.Set("Content-Type", "application/json")
	go func() {
		_, _ = io.WriteString(writer, `{"code":"`+strings.Repeat("9", 512)+`"}`)
		_ = writer.Close()
	}()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST oversized chunked pairing: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized chunked pairing = %d, want 413", response.StatusCode)
	}

	cancel()
	if err = <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestPairingHTTPUsesInjectedClockAndFailsClosedOnStoreError(t *testing.T) {
	clock := &runtimeTestClock{now: time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC)}
	store := &failHTTPConsumeStore{MemoryStore: pairing.NewMemoryStore()}
	config := testConfigWithPairing(clock, store)
	application, err := companionruntime.New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	status := waitForState(t, application, companionruntime.StateReady)
	client, session, csrf := loginManagement(t, status.ManagementAddress, config.Management.AdminToken)

	expired := issuePairingCode(t, client, status.ManagementAddress, session, csrf)
	clock.Set(expired.ExpiresAt.Add(time.Nanosecond))
	redeemPairingCode(t, status.DeviceHubAddress, expired.Code, pairing.ProtocolVersion, http.StatusUnauthorized)

	current := issuePairingCode(t, client, status.ManagementAddress, session, csrf)
	store.SetFail(true)
	redeemPairingCode(t, status.DeviceHubAddress, current.Code, pairing.ProtocolVersion, http.StatusServiceUnavailable)
	store.SetFail(false)
	credential := redeemPairingCode(t, status.DeviceHubAddress, current.Code, pairing.ProtocolVersion, http.StatusOK)
	if credential.Token == "" {
		t.Fatal("store failure returned/consumed the only plaintext credential")
	}

	cancel()
	if err = <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func issuePairingCode(
	t *testing.T,
	client *http.Client,
	managementAddress string,
	session *http.Cookie,
	csrf string,
) pairing.IssuedCode {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "http://"+managementAddress+"/api/v1/pairing/codes", nil)
	if err != nil {
		t.Fatalf("NewRequest(issue) error = %v", err)
	}
	request.AddCookie(session)
	request.Header.Set("Origin", "http://"+managementAddress)
	request.Header.Set("X-CSRF-Token", csrf)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST pairing code: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("POST pairing code = %d, want 200", response.StatusCode)
	}
	var issued pairing.IssuedCode
	if err = json.NewDecoder(response.Body).Decode(&issued); err != nil {
		t.Fatalf("decode pairing code: %v", err)
	}
	if len(issued.Code) != 6 || issued.ExpiresAt.IsZero() {
		t.Fatalf("issued code = %#v", issued)
	}
	return issued
}

func redeemPairingCode(
	t *testing.T,
	deviceHubAddress string,
	code string,
	protocolVersion int,
	wantStatus int,
) pairing.Credential {
	t.Helper()
	body := `{"code":"` + code + `","device_id":"deck-http1234",` +
		`"device_identity":"ZGV2aWNlLXB1YmxpYy1rZXktbWF0ZXJpYWw",` +
		`"protocol_version":` + strconv.Itoa(protocolVersion) + `}`
	request, err := http.NewRequest(
		http.MethodPost,
		"http://"+deviceHubAddress+"/api/v1/pairing/redeem",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatalf("NewRequest(redeem) error = %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST pairing redeem: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		t.Fatalf("POST pairing redeem = %d, want %d", response.StatusCode, wantStatus)
	}
	var credential pairing.Credential
	if wantStatus == http.StatusOK {
		if err = json.NewDecoder(response.Body).Decode(&credential); err != nil {
			t.Fatalf("decode pairing credential: %v", err)
		}
	}
	return credential
}

func rotateDeviceToken(
	t *testing.T,
	client *http.Client,
	managementAddress string,
	deviceID string,
	session *http.Cookie,
	csrf string,
) pairing.IssuedCode {
	t.Helper()
	request, err := http.NewRequest(
		http.MethodPost,
		"http://"+managementAddress+"/api/v1/devices/"+deviceID+"/rotate",
		nil,
	)
	if err != nil {
		t.Fatalf("NewRequest(rotate) error = %v", err)
	}
	request.AddCookie(session)
	request.Header.Set("Origin", "http://"+managementAddress)
	request.Header.Set("X-CSRF-Token", csrf)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST rotate: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("POST rotate = %d, want 200", response.StatusCode)
	}
	var issued pairing.IssuedCode
	if err = json.NewDecoder(response.Body).Decode(&issued); err != nil {
		t.Fatalf("decode rotation code: %v", err)
	}
	return issued
}

func revokeDevice(
	t *testing.T,
	client *http.Client,
	managementAddress string,
	deviceID string,
	session *http.Cookie,
	csrf string,
) {
	t.Helper()
	request, err := http.NewRequest(
		http.MethodDelete,
		"http://"+managementAddress+"/api/v1/devices/"+deviceID,
		nil,
	)
	if err != nil {
		t.Fatalf("NewRequest(revoke) error = %v", err)
	}
	request.AddCookie(session)
	request.Header.Set("Origin", "http://"+managementAddress)
	request.Header.Set("X-CSRF-Token", csrf)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("DELETE device: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE device = %d, want 204", response.StatusCode)
	}
}

func deviceHealthStatus(
	t *testing.T,
	address string,
	deviceID string,
	deviceIdentity string,
	protocolVersion int,
	token string,
) int {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "http://"+address+"/api/v1/device/health", nil)
	if err != nil {
		t.Fatalf("NewRequest(health) error = %v", err)
	}
	request.Header.Set("X-Device-ID", deviceID)
	request.Header.Set("X-Device-Identity", deviceIdentity)
	request.Header.Set("X-Protocol-Version", strconv.Itoa(protocolVersion))
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET device health: %v", err)
	}
	response.Body.Close()
	return response.StatusCode
}
