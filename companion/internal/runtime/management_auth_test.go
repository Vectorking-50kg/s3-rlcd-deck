package runtime_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	companionruntime "github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/runtime"
)

func TestManagementClosesSlowLoginBodiesWithinConfiguredDeadline(t *testing.T) {
	config := testConfig()
	config.Management.Limits.ReadTimeout = 30 * time.Millisecond
	application, err := companionruntime.New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	status := waitForState(t, application, companionruntime.StateReady)

	connection, err := net.Dial("tcp", status.ManagementAddress)
	if err != nil {
		t.Fatalf("dial management: %v", err)
	}
	partialLogin := "POST /api/v1/login HTTP/1.1\r\nHost: " + status.ManagementAddress +
		"\r\nOrigin: http://" + status.ManagementAddress +
		"\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{"
	if _, err = io.WriteString(connection, partialLogin); err != nil {
		t.Fatalf("write partial login: %v", err)
	}
	if err = connection.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	buffer := make([]byte, 1)
	_, err = connection.Read(buffer)
	connection.Close()
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		t.Fatal("management listener kept a slow login body beyond ReadTimeout")
	}

	cancel()
	if err = <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestManagementRateLimitsSensitiveRequestsByIP(t *testing.T) {
	config := testConfig()
	config.Management.Limits.SensitiveRequests = 1
	config.Management.Limits.SensitiveRateWindow = time.Minute
	application, err := companionruntime.New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	status := waitForState(t, application, companionruntime.StateReady)

	for attempt, wantStatus := range []int{http.StatusUnauthorized, http.StatusTooManyRequests} {
		request, requestErr := http.NewRequest(
			http.MethodPost,
			"http://"+status.ManagementAddress+"/api/v1/login",
			strings.NewReader(`{"token":"incorrect-management-token-value"}`),
		)
		if requestErr != nil {
			t.Fatalf("NewRequest() error = %v", requestErr)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "http://"+status.ManagementAddress)
		response, requestErr := http.DefaultClient.Do(request)
		if requestErr != nil {
			t.Fatalf("POST login attempt %d: %v", attempt+1, requestErr)
		}
		response.Body.Close()
		if response.StatusCode != wantStatus {
			t.Fatalf("POST login attempt %d = %d, want %d", attempt+1, response.StatusCode, wantStatus)
		}
	}

	cancel()
	if err = <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestManagementAllowsOnlyBootstrapAndLoginBeforeAuthentication(t *testing.T) {
	config := testConfig()
	application, err := companionruntime.New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	status := waitForState(t, application, companionruntime.StateReady)
	baseURL := "http://" + status.ManagementAddress

	for _, path := range []string{"/", "/app.css", "/app.js", "/api/v1/bootstrap"} {
		response, requestErr := http.Get(baseURL + path)
		if requestErr != nil {
			t.Fatalf("GET %s: %v", path, requestErr)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, response.StatusCode)
		}
	}
	for _, path := range []string{"/api/v1/status", "/api/v1/console"} {
		response, err := http.Get(baseURL + path)
		if err != nil {
			t.Fatalf("GET unauthenticated %s: %v", path, err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("GET unauthenticated %s = %d, want 401", path, response.StatusCode)
		}
	}

	cancel()
	if err = <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestManagementResponsesSetBrowserSecurityHeaders(t *testing.T) {
	config := testConfig()
	application, err := companionruntime.New(config)
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
	response.Body.Close()
	for header, want := range map[string]string{
		"Content-Security-Policy": "default-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'",
		"Referrer-Policy":         "no-referrer",
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
	} {
		if got := response.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestManagementLoginRejectsDeviceTokenAndWrongOrigin(t *testing.T) {
	config := testConfig()
	application, err := companionruntime.New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	status := waitForState(t, application, companionruntime.StateReady)
	loginURL := "http://" + status.ManagementAddress + "/api/v1/login"

	for name, testCase := range map[string]struct {
		token      string
		origin     string
		wantStatus int
	}{
		"Device token": {"paired-device-token-does-not-authorize-management", "http://" + status.ManagementAddress, http.StatusUnauthorized},
		"wrong Origin": {config.Management.AdminToken, "http://attacker.invalid", http.StatusForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			request, requestErr := http.NewRequest(
				http.MethodPost,
				loginURL,
				strings.NewReader(`{"token":"`+testCase.token+`"}`),
			)
			if requestErr != nil {
				t.Fatalf("NewRequest() error = %v", requestErr)
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", testCase.origin)
			response, requestErr := http.DefaultClient.Do(request)
			if requestErr != nil {
				t.Fatalf("POST login: %v", requestErr)
			}
			response.Body.Close()
			if response.StatusCode != testCase.wantStatus {
				t.Fatalf("POST login = %d, want %d", response.StatusCode, testCase.wantStatus)
			}
		})
	}

	cancel()
	if err = <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestManagementWriteRequiresSessionOriginAndCSRF(t *testing.T) {
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
	logoutURL := "http://" + status.ManagementAddress + "/api/v1/logout"

	for name, testCase := range map[string]struct {
		origin string
		csrf   string
	}{
		"missing Origin": {"", csrf},
		"wrong Origin":   {"http://attacker.invalid", csrf},
		"missing CSRF":   {"http://" + status.ManagementAddress, ""},
		"wrong CSRF":     {"http://" + status.ManagementAddress, "wrong-csrf-token"},
	} {
		t.Run(name, func(t *testing.T) {
			request, requestErr := http.NewRequest(http.MethodPost, logoutURL, nil)
			if requestErr != nil {
				t.Fatalf("NewRequest() error = %v", requestErr)
			}
			request.AddCookie(session)
			request.Header.Set("Origin", testCase.origin)
			request.Header.Set("X-CSRF-Token", testCase.csrf)
			response, requestErr := client.Do(request)
			if requestErr != nil {
				t.Fatalf("POST logout: %v", requestErr)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusForbidden {
				t.Fatalf("POST logout = %d, want 403", response.StatusCode)
			}
		})
	}

	request, err := http.NewRequest(http.MethodPost, logoutURL, nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.AddCookie(session)
	request.Header.Set("Origin", "http://"+status.ManagementAddress)
	request.Header.Set("X-CSRF-Token", csrf)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST logout: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("POST logout = %d, want 204", response.StatusCode)
	}

	cancel()
	if err = <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestLANManagementMustBeExplicitAndIsReportedAsRisk(t *testing.T) {
	config := testConfig()
	config.Management.Address = "0.0.0.0:7777"
	config.Management.AllowLAN = true
	config.Management.AllowedOrigin = "http://192.0.2.10:7777"
	application, err := companionruntime.New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	status := application.Status()
	if !status.LANManagementEnabled || status.SecurityWarning == "" {
		t.Fatalf("status = %#v, want explicit LAN risk", status)
	}
}

func TestLANManagementRequiresAnExactBrowserOrigin(t *testing.T) {
	for name, origin := range map[string]string{
		"missing":  "",
		"path":     "http://192.0.2.10:7777/admin",
		"non-http": "file://192.0.2.10",
	} {
		t.Run(name, func(t *testing.T) {
			config := testConfig()
			config.Management.Address = "0.0.0.0:7777"
			config.Management.AllowLAN = true
			config.Management.AllowedOrigin = origin
			if _, err := companionruntime.New(config); err == nil {
				t.Fatal("New() error = nil, want invalid LAN management origin")
			}
		})
	}
}
