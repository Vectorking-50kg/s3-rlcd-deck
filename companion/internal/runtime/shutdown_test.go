package runtime

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestRunForceClosesAHandlerAfterGracefulShutdownDeadline(t *testing.T) {
	application, err := New(Config{
		ManagementAddress: "127.0.0.1:0",
		Version:           "1.2.3-test",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	releaseHandler := make(chan struct{})
	handlerStarted := make(chan struct{})
	application.handler = http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		close(handlerStarted)
		<-releaseHandler
		response.WriteHeader(http.StatusNoContent)
	})
	application.shutdownTimeout = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	status := waitForRuntimeState(t, application, StateReady)
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		response, requestErr := http.Get("http://" + status.ManagementAddress + "/")
		if requestErr == nil {
			response.Body.Close()
		}
	}()
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
	close(releaseHandler)
	<-requestDone
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
