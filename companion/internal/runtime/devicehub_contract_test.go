package runtime_test

import (
	"bufio"
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

func TestDeviceHubRejectsOversizedHeaders(t *testing.T) {
	config := testConfig()
	config.DeviceHub.Limits.MaxHeaderBytes = 128
	application, err := companionruntime.New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	status := waitForState(t, application, companionruntime.StateReady)

	connection, err := net.Dial("tcp", status.DeviceHubAddress)
	if err != nil {
		t.Fatalf("dial Device Hub: %v", err)
	}
	request := "GET /api/v1/device/health HTTP/1.1\r\nHost: deck\r\nX-Oversized: " +
		strings.Repeat("x", 8<<10) + "\r\n\r\n"
	if _, err = io.WriteString(connection, request); err != nil {
		t.Fatalf("write oversized header: %v", err)
	}
	if err = connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	connection.Close()
	if err != nil {
		t.Fatalf("read oversized-header response: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("oversized header = %d, want 431", response.StatusCode)
	}

	cancel()
	if err = <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestDeviceHubRejectsOversizedBodiesAndRateLimitsByIP(t *testing.T) {
	config := testConfig()
	config.DeviceHub.Limits.MaxBodyBytes = 8
	config.DeviceHub.Limits.RateLimitRequests = 1
	config.DeviceHub.Limits.RateLimitWindow = time.Minute
	application, err := companionruntime.New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	status := waitForState(t, application, companionruntime.StateReady)
	url := "http://" + status.DeviceHubAddress + "/api/v1/device/health"

	oversizedRequest, err := http.NewRequest(http.MethodGet, url, strings.NewReader("123456789"))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	oversizedRequest.Header.Set("Authorization", "Bearer "+config.DeviceHub.BootstrapToken)
	response, err := http.DefaultClient.Do(oversizedRequest)
	if err != nil {
		t.Fatalf("oversized request: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request = %d, want 413", response.StatusCode)
	}

	rateLimitedRequest, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	rateLimitedRequest.Header.Set("Authorization", "Bearer "+config.DeviceHub.BootstrapToken)
	response, err = http.DefaultClient.Do(rateLimitedRequest)
	if err != nil {
		t.Fatalf("rate-limited request: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second request from IP = %d, want 429", response.StatusCode)
	}

	cancel()
	if err = <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestDeviceHubClosesSlowHeadersWithinConfiguredDeadline(t *testing.T) {
	config := testConfig()
	config.DeviceHub.Limits.ReadHeaderTimeout = 30 * time.Millisecond
	application, err := companionruntime.New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	status := waitForState(t, application, companionruntime.StateReady)

	connection, err := net.Dial("tcp", status.DeviceHubAddress)
	if err != nil {
		t.Fatalf("dial Device Hub: %v", err)
	}
	if _, err = io.WriteString(connection, "GET /api/v1/device/health HTTP/1.1\r\nHost: deck\r\n"); err != nil {
		t.Fatalf("write partial request: %v", err)
	}
	if err = connection.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	buffer := make([]byte, 1)
	_, err = connection.Read(buffer)
	connection.Close()
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		t.Fatal("Device Hub kept a slowloris connection beyond ReadHeaderTimeout")
	}

	cancel()
	if err = <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestDeviceHubLimitsConnectionsBeforeHeadersAreComplete(t *testing.T) {
	config := testConfig()
	config.DeviceHub.Limits.MaxConcurrent = 1
	config.DeviceHub.Limits.MaxConcurrentPerIP = 1
	config.DeviceHub.Limits.ReadHeaderTimeout = time.Second
	application, err := companionruntime.New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	status := waitForState(t, application, companionruntime.StateReady)

	partial, err := net.Dial("tcp", status.DeviceHubAddress)
	if err != nil {
		t.Fatalf("dial first Device Hub connection: %v", err)
	}
	if _, err = io.WriteString(partial, "GET /api/v1/device/health HTTP/1.1\r\nHost: deck\r\n"); err != nil {
		t.Fatalf("write partial header: %v", err)
	}

	overLimit, err := net.Dial("tcp", status.DeviceHubAddress)
	if err != nil {
		t.Fatalf("dial over-limit connection: %v", err)
	}
	if _, err = io.WriteString(overLimit, "GET / HTTP/1.1\r\nHost: deck\r\n\r\n"); err != nil {
		t.Fatalf("write over-limit request: %v", err)
	}
	if err = overLimit.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	buffer := make([]byte, 1)
	_, err = overLimit.Read(buffer)
	overLimit.Close()
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		t.Fatal("over-limit half-open connection was not rejected at the listener")
	}
	partial.Close()

	cancel()
	if err = <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestDeviceHubBindFailureClosesManagementListener(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy address: %v", err)
	}
	defer occupied.Close()
	config := testConfig()
	config.DeviceHub.Address = occupied.Addr().String()
	application, err := companionruntime.New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err = application.Run(context.Background()); err == nil {
		t.Fatal("Run() error = nil, want Device Hub bind failure")
	}
	status := application.Status()
	connection, dialErr := net.DialTimeout("tcp", status.ManagementAddress, 50*time.Millisecond)
	if dialErr == nil {
		connection.Close()
		t.Fatal("management listener remained open after Device Hub bind failure")
	}
}
