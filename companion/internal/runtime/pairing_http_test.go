package runtime_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairing"
	companionruntime "github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/runtime"
)

const testCertificateFingerprint = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

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
	redeemPairingCode(t, status.DeviceHubAddress, issued.Code, pairing.ProtocolVersion, http.StatusUnauthorized)
	if got := deviceHealthStatus(t, status.DeviceHubAddress, credential.DeviceID, credential.Token); got != http.StatusOK {
		t.Fatalf("paired Device health = %d, want 200", got)
	}
	if got := deviceHealthStatus(t, status.DeviceHubAddress, credential.DeviceID, config.Management.AdminToken); got != http.StatusUnauthorized {
		t.Fatalf("management token on Device Hub = %d, want 401", got)
	}

	rotated := rotateDeviceToken(t, client, status.ManagementAddress, credential.DeviceID, session, csrf)
	if rotated.Token == "" || rotated.Token == credential.Token {
		t.Fatal("management rotation did not return one distinct device token")
	}
	if got := deviceHealthStatus(t, status.DeviceHubAddress, credential.DeviceID, credential.Token); got != http.StatusUnauthorized {
		t.Fatalf("old token after rotation = %d, want 401", got)
	}
	if got := deviceHealthStatus(t, status.DeviceHubAddress, credential.DeviceID, rotated.Token); got != http.StatusOK {
		t.Fatalf("rotated token health = %d, want 200", got)
	}

	revokeDevice(t, client, status.ManagementAddress, credential.DeviceID, session, csrf)
	if got := deviceHealthStatus(t, status.DeviceHubAddress, credential.DeviceID, rotated.Token); got != http.StatusUnauthorized {
		t.Fatalf("revoked token health = %d, want 401", got)
	}

	cancel()
	if err = <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
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
	if len(issued.Code) != 6 || time.Until(issued.ExpiresAt) <= 0 {
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
) pairing.Credential {
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
	var credential pairing.Credential
	if err = json.NewDecoder(response.Body).Decode(&credential); err != nil {
		t.Fatalf("decode rotated credential: %v", err)
	}
	return credential
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

func deviceHealthStatus(t *testing.T, address string, deviceID string, token string) int {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "http://"+address+"/api/v1/device/health", nil)
	if err != nil {
		t.Fatalf("NewRequest(health) error = %v", err)
	}
	request.Header.Set("X-Device-ID", deviceID)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET device health: %v", err)
	}
	response.Body.Close()
	return response.StatusCode
}
