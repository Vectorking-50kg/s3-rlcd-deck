package runtime_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	companionruntime "github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/runtime"
)

func TestConsoleViewIsAuthenticatedStableAndPrivacySafe(t *testing.T) {
	config := testConfig()
	config.DeviceHub.AdvertisedAddress = "192.168.50.8:7780"
	application, err := companionruntime.New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	status := waitForState(t, application, companionruntime.StateReady)
	client, session, _ := loginManagement(t, status.ManagementAddress, config.Management.AdminToken)

	request, err := http.NewRequest(
		http.MethodGet,
		"http://"+status.ManagementAddress+"/api/v1/console",
		nil,
	)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.AddCookie(session)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("GET console: %v", err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil {
		t.Fatalf("read console response: %v", readErr)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET console = %d %q, want 200", response.StatusCode, body)
	}
	for _, privateName := range []string{"prompt", "raw_response", "credential", "absolute_path", "serial_body"} {
		if strings.Contains(strings.ToLower(string(body)), privateName) {
			t.Fatalf("console ViewModel exposed forbidden field %q in %s", privateName, body)
		}
	}
	var document struct {
		Runtime struct {
			State                      string `json:"state"`
			Version                    string `json:"version"`
			DeviceHubAddress           string `json:"device_hub_address"`
			DeviceHubAdvertisedAddress string `json:"device_hub_advertised_address"`
		} `json:"runtime"`
		Providers    []json.RawMessage `json:"providers"`
		Sessions     []json.RawMessage `json:"sessions"`
		Capabilities struct {
			Pairing bool `json:"pairing"`
			Serial  bool `json:"serial"`
		} `json:"capabilities"`
	}
	if err = json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode console response: %v", err)
	}
	if document.Runtime.State != "ready" || document.Runtime.Version != config.Version ||
		document.Runtime.DeviceHubAddress == document.Runtime.DeviceHubAdvertisedAddress ||
		document.Runtime.DeviceHubAdvertisedAddress != "192.168.50.8:7780" ||
		document.Providers == nil || document.Sessions == nil || !document.Capabilities.Pairing ||
		!document.Capabilities.Serial {
		t.Fatalf("console ViewModel = %#v", document)
	}

	cancel()
	if err = <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestExistingDesktopSessionCanRefreshCSRF(t *testing.T) {
	config := testConfig()
	application, err := companionruntime.New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	status := waitForState(t, application, companionruntime.StateReady)
	client, session, oldCSRF := loginManagement(t, status.ManagementAddress, config.Management.AdminToken)
	baseURL := "http://" + status.ManagementAddress

	missingOrigin, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/session/refresh", nil)
	if err != nil {
		t.Fatalf("NewRequest(refresh without Origin) error = %v", err)
	}
	missingOrigin.AddCookie(session)
	response, err := client.Do(missingOrigin)
	if err != nil {
		t.Fatalf("POST session refresh without Origin: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("POST session refresh without Origin = %d, want 403", response.StatusCode)
	}

	refresh, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/session/refresh", nil)
	if err != nil {
		t.Fatalf("NewRequest(refresh) error = %v", err)
	}
	refresh.AddCookie(session)
	refresh.Header.Set("Origin", baseURL)
	response, err = client.Do(refresh)
	if err != nil {
		t.Fatalf("POST session refresh: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("POST session refresh = %d %q, want 200", response.StatusCode, body)
	}
	var refreshed struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err = json.NewDecoder(response.Body).Decode(&refreshed); err != nil {
		t.Fatalf("decode refreshed session: %v", err)
	}
	if refreshed.CSRFToken == "" || refreshed.CSRFToken == oldCSRF {
		t.Fatal("session refresh did not return a distinct CSRF token")
	}

	for _, testCase := range []struct {
		name string
		csrf string
		want int
	}{
		{name: "old", csrf: oldCSRF, want: http.StatusForbidden},
		{name: "refreshed", csrf: refreshed.CSRFToken, want: http.StatusNoContent},
	} {
		logout, requestErr := http.NewRequest(http.MethodPost, baseURL+"/api/v1/logout", strings.NewReader("{}"))
		if requestErr != nil {
			t.Fatalf("NewRequest(logout) error = %v", requestErr)
		}
		logout.AddCookie(session)
		logout.Header.Set("Origin", baseURL)
		logout.Header.Set("X-CSRF-Token", testCase.csrf)
		response, requestErr = client.Do(logout)
		if requestErr != nil {
			t.Fatalf("POST logout with %s CSRF: %v", testCase.name, requestErr)
		}
		response.Body.Close()
		if response.StatusCode != testCase.want {
			t.Fatalf("POST logout with %s CSRF = %d, want %d", testCase.name, response.StatusCode, testCase.want)
		}
	}

	cancel()
	if err = <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}
