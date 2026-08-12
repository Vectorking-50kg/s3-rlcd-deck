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

type runtimeTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *runtimeTestClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *runtimeTestClock) Set(now time.Time) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = now
}

func TestRuntimeRejectsNonLoopbackManagementAddress(t *testing.T) {
	config := testConfig()
	config.Management.Address = "0.0.0.0:7777"
	_, err := companionruntime.New(config)
	if !errors.Is(err, companionruntime.ErrManagementAddressNotLoopback) {
		t.Fatalf("New() error = %v, want ErrManagementAddressNotLoopback", err)
	}
}

func TestRuntimeRejectsNonLoopbackDeviceHubUntilPinnedTLSExists(t *testing.T) {
	config := testConfig()
	config.DeviceHub.Address = "0.0.0.0:7780"
	_, err := companionruntime.New(config)
	if !errors.Is(err, companionruntime.ErrDeviceHubTLSRequired) {
		t.Fatalf("New() error = %v, want ErrDeviceHubTLSRequired", err)
	}
}

func TestRuntimeServesReadOnlyStatus(t *testing.T) {
	config := testConfig()
	application, err := companionruntime.New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()

	status := waitForState(t, application, companionruntime.StateReady)
	client, session, _ := loginManagement(t, status.ManagementAddress, config.Management.AdminToken)
	request, err := http.NewRequest(http.MethodGet, "http://"+status.ManagementAddress+"/api/v1/status", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.AddCookie(session)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("GET status: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want 200", response.StatusCode)
	}
	var document struct {
		State   string `json:"state"`
		Version string `json:"version"`
	}
	if err = json.NewDecoder(response.Body).Decode(&document); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if document.State != "ready" || document.Version != "1.2.3-test" {
		t.Fatalf("status = %#v, want ready version 1.2.3-test", document)
	}

	cancel()
	if err = <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func loginManagement(
	t *testing.T,
	address string,
	adminToken string,
) (*http.Client, *http.Cookie, string) {
	t.Helper()
	request, err := http.NewRequest(
		http.MethodPost,
		"http://"+address+"/api/v1/login",
		strings.NewReader(`{"token":"`+adminToken+`"}`),
	)
	if err != nil {
		t.Fatalf("NewRequest(login) error = %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://"+address)
	client := &http.Client{}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST login: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("POST login = %d %q, want 200", response.StatusCode, body)
	}
	var document struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err = json.NewDecoder(response.Body).Decode(&document); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name == "s3deck_session" {
			return client, cookie, document.CSRFToken
		}
	}
	t.Fatal("login did not return a session cookie")
	return nil, nil, ""
}

func TestRuntimeServesEmbeddedWebApplication(t *testing.T) {
	application, err := companionruntime.New(testConfig())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	status := waitForState(t, application, companionruntime.StateReady)

	response, err := http.Get("http://" + status.ManagementAddress + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil {
		t.Fatalf("read embedded page: %v", readErr)
	}
	if response.StatusCode != http.StatusOK ||
		!strings.Contains(string(body), "<title>S3 RLCD Deck</title>") {
		t.Fatalf("GET / = %d %q, want embedded S3 RLCD Deck page", response.StatusCode, body)
	}

	cancel()
	if err = <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRuntimeServesDeviceHubOnAnIndependentAuthenticatedListener(t *testing.T) {
	config := testConfig()
	application, err := companionruntime.New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	status := waitForState(t, application, companionruntime.StateReady)
	if status.DeviceHubAddress == "" || status.DeviceHubAddress == status.ManagementAddress {
		t.Fatalf("status = %#v, want independent management and Device Hub addresses", status)
	}
	issued, err := config.Pairing.Issue(context.Background())
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	credential, err := config.Pairing.Redeem(context.Background(), pairing.RedeemRequest{
		Code:            issued.Code,
		DeviceID:        "deck-runtime1",
		DeviceIdentity:  "ZGV2aWNlLXB1YmxpYy1rZXktbWF0ZXJpYWw",
		ProtocolVersion: pairing.ProtocolVersion,
	})
	if err != nil {
		t.Fatalf("Redeem() error = %v", err)
	}

	request, err := http.NewRequest(
		http.MethodGet,
		"http://"+status.DeviceHubAddress+"/api/v1/device/health",
		nil,
	)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("X-Device-ID", credential.DeviceID)
	request.Header.Set("X-Device-Identity", "ZGV2aWNlLXB1YmxpYy1rZXktbWF0ZXJpYWw")
	request.Header.Set("X-Protocol-Version", strconv.Itoa(pairing.ProtocolVersion))
	request.Header.Set("Authorization", "Bearer "+credential.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET Device Hub health: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Device Hub health status = %d, want 200", response.StatusCode)
	}
	deviceRouteOnManagement, err := http.NewRequest(
		http.MethodGet,
		"http://"+status.ManagementAddress+"/api/v1/device/health",
		nil,
	)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	client, session, _ := loginManagement(t, status.ManagementAddress, config.Management.AdminToken)
	deviceRouteOnManagement.AddCookie(session)
	wrongListenerResponse, err := client.Do(deviceRouteOnManagement)
	if err != nil {
		t.Fatalf("GET Device route on management listener: %v", err)
	}
	wrongListenerResponse.Body.Close()
	if wrongListenerResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("Device route on management listener = %d, want 404", wrongListenerResponse.StatusCode)
	}
	managementRouteOnDevice, err := http.NewRequest(
		http.MethodGet,
		"http://"+status.DeviceHubAddress+"/api/v1/bootstrap",
		nil,
	)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	managementRouteOnDevice.Header.Set("X-Device-ID", credential.DeviceID)
	managementRouteOnDevice.Header.Set("X-Device-Identity", "ZGV2aWNlLXB1YmxpYy1rZXktbWF0ZXJpYWw")
	managementRouteOnDevice.Header.Set("X-Protocol-Version", strconv.Itoa(pairing.ProtocolVersion))
	managementRouteOnDevice.Header.Set("Authorization", "Bearer "+credential.Token)
	wrongListenerResponse, err = http.DefaultClient.Do(managementRouteOnDevice)
	if err != nil {
		t.Fatalf("GET management route on Device Hub: %v", err)
	}
	wrongListenerResponse.Body.Close()
	if wrongListenerResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("management route on Device Hub = %d, want 404", wrongListenerResponse.StatusCode)
	}
	for name, token := range map[string]string{
		"missing token":    "",
		"management token": config.Management.AdminToken,
	} {
		t.Run(name, func(t *testing.T) {
			unauthorizedRequest, requestErr := http.NewRequest(
				http.MethodGet,
				"http://"+status.DeviceHubAddress+"/api/v1/device/health",
				nil,
			)
			if requestErr != nil {
				t.Fatalf("NewRequest() error = %v", requestErr)
			}
			if token != "" {
				unauthorizedRequest.Header.Set("X-Device-ID", credential.DeviceID)
				unauthorizedRequest.Header.Set("X-Device-Identity", "ZGV2aWNlLXB1YmxpYy1rZXktbWF0ZXJpYWw")
				unauthorizedRequest.Header.Set("X-Protocol-Version", strconv.Itoa(pairing.ProtocolVersion))
				unauthorizedRequest.Header.Set("Authorization", "Bearer "+token)
			}
			unauthorizedResponse, requestErr := http.DefaultClient.Do(unauthorizedRequest)
			if requestErr != nil {
				t.Fatalf("GET Device Hub health: %v", requestErr)
			}
			unauthorizedResponse.Body.Close()
			if unauthorizedResponse.StatusCode != http.StatusUnauthorized {
				t.Fatalf("Device Hub cross-token status = %d, want 401", unauthorizedResponse.StatusCode)
			}
		})
	}

	cancel()
	if err = <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func testConfig() companionruntime.Config {
	clock := &runtimeTestClock{now: time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC)}
	return testConfigWithPairing(clock, pairing.NewMemoryStore())
}

func testConfigWithPairing(clock pairing.Clock, store pairing.Store) companionruntime.Config {
	pairingService, err := pairing.New(pairing.Config{
		Clock:                  clock,
		Store:                  store,
		CertificateFingerprint: testCertificateFingerprint,
	})
	if err != nil {
		panic(err)
	}
	return companionruntime.Config{
		Version: "1.2.3-test",
		Management: companionruntime.ManagementConfig{
			Address:    "127.0.0.1:0",
			AdminToken: "management-test-token-000000000001",
		},
		DeviceHub: companionruntime.DeviceHubConfig{
			Address: "127.0.0.1:0",
		},
		Pairing: pairingService,
	}
}

func waitForState(
	t *testing.T,
	application *companionruntime.Runtime,
	want companionruntime.State,
) companionruntime.Status {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := application.Status()
		if status.State == want {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("runtime did not reach state %q; last status = %#v", want, application.Status())
	return companionruntime.Status{}
}
