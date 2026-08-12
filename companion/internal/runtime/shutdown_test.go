package runtime

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestRunForceClosesAHandlerAfterGracefulShutdownDeadline(t *testing.T) {
	application, err := New(Config{
		Version: "1.2.3-test",
		Management: ManagementConfig{
			Address:    "127.0.0.1:0",
			AdminToken: "management-test-token-000000000001",
		},
		DeviceHub: DeviceHubConfig{
			Address: "127.0.0.1:0",
		},
		Pairing: testPairingService(t),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	releaseHandler := make(chan struct{})
	handlerStarted := make(chan struct{}, 2)
	blockingHandler := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		handlerStarted <- struct{}{}
		<-releaseHandler
		response.WriteHeader(http.StatusNoContent)
	})
	application.managementHandler = blockingHandler
	application.deviceHubHandler = blockingHandler
	application.shutdownTimeout = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	status := waitForRuntimeState(t, application, StateReady)
	requestDone := make(chan struct{}, 2)
	for _, address := range []string{status.ManagementAddress, status.DeviceHubAddress} {
		go func(address string) {
			response, requestErr := http.Get("http://" + address + "/")
			if requestErr == nil {
				response.Body.Close()
			}
			requestDone <- struct{}{}
		}(address)
	}
	<-handlerStarted
	<-handlerStarted
	cancel()

	select {
	case runErr := <-done:
		if runErr == nil {
			t.Fatal("Run() error = nil, want graceful shutdown deadline error")
		}
	case <-time.After(250 * time.Millisecond):
		close(releaseHandler)
		<-done
		t.Fatal("Run() did not return after the graceful shutdown deadline")
	}
	for range 2 {
		select {
		case <-requestDone:
		case <-time.After(100 * time.Millisecond):
			close(releaseHandler)
			for range 2 {
				select {
				case <-requestDone:
				default:
				}
			}
			t.Fatal("forced shutdown returned without closing both active connections")
		}
	}
	close(releaseHandler)
}

func waitForRuntimeState(t *testing.T, application *Runtime, want State) Status {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status := application.Status()
		if status.State == want {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("runtime did not reach %q; status = %#v", want, application.Status())
	return Status{}
}
