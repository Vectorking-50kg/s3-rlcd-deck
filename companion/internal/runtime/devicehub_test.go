package runtime

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeviceHubRejectsConcurrentWorkBeyondItsLimit(t *testing.T) {
	config, err := normalizeConfig(Config{
		Version: "1.2.3-test",
		Management: ManagementConfig{
			Address:    "127.0.0.1:0",
			AdminToken: "management-test-token-000000000001",
		},
		DeviceHub: DeviceHubConfig{
			Address:        "127.0.0.1:0",
			BootstrapToken: "device-hub-test-token-000000000001",
			Limits: DeviceHubLimits{
				MaxConcurrent:     1,
				RateLimitRequests: 10,
			},
		},
	})
	if err != nil {
		t.Fatalf("normalizeConfig() error = %v", err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	next := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		response.WriteHeader(http.StatusNoContent)
	})
	server := httptest.NewServer(newDeviceHubGateway(config.DeviceHub, next))
	defer server.Close()

	firstDone := make(chan error, 1)
	go func() {
		request, requestErr := http.NewRequest(http.MethodGet, server.URL, nil)
		if requestErr == nil {
			request.Header.Set("Authorization", "Bearer "+config.DeviceHub.BootstrapToken)
			var response *http.Response
			response, requestErr = http.DefaultClient.Do(request)
			if requestErr == nil {
				response.Body.Close()
			}
		}
		firstDone <- requestErr
	}()
	<-entered

	request, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+config.DeviceHub.BootstrapToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("concurrent request = %d, want 503", response.StatusCode)
	}

	close(release)
	if err = <-firstDone; err != nil {
		t.Fatalf("first request: %v", err)
	}
}
