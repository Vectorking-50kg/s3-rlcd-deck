package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	companionruntime "github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/runtime"
)

func TestRuntimeRejectsNonLoopbackManagementAddress(t *testing.T) {
	_, err := companionruntime.New(companionruntime.Config{
		ManagementAddress: "0.0.0.0:7777",
		Version:           "1.2.3-test",
	})
	if !errors.Is(err, companionruntime.ErrManagementAddressNotLoopback) {
		t.Fatalf("New() error = %v, want ErrManagementAddressNotLoopback", err)
	}
}

func TestRuntimeServesReadOnlyStatus(t *testing.T) {
	application, err := companionruntime.New(companionruntime.Config{
		ManagementAddress: "127.0.0.1:0",
		Version:           "1.2.3-test",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()

	status := waitForState(t, application, companionruntime.StateReady)
	response, err := http.Get("http://" + status.ManagementAddress + "/api/v1/status")
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

func TestRuntimeServesEmbeddedWebApplication(t *testing.T) {
	application, err := companionruntime.New(companionruntime.Config{
		ManagementAddress: "127.0.0.1:0",
		Version:           "1.2.3-test",
	})
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
