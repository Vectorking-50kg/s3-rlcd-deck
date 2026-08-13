package runtime_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	companionruntime "github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/runtime"
)

func TestControllerStartsStopsAndRestartsRuntime(t *testing.T) {
	controller, err := companionruntime.NewController(func() (*companionruntime.Runtime, error) {
		return companionruntime.New(testConfig())
	})
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}
	if controller.Status().State != companionruntime.StateStopped {
		t.Fatalf("initial state = %q, want stopped", controller.Status().State)
	}
	if err = controller.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForControllerState(t, controller, companionruntime.StateReady)
	if controller.Status().ManagementAddress == "" {
		t.Fatal("ready controller omitted management address")
	}
	if err = controller.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if controller.Status().State != companionruntime.StateStopped {
		t.Fatalf("state after Stop = %q, want stopped", controller.Status().State)
	}
	if err = controller.Start(); err != nil {
		t.Fatalf("restart error = %v", err)
	}
	waitForControllerState(t, controller, companionruntime.StateReady)
	if err = controller.Stop(context.Background()); err != nil {
		t.Fatalf("final Stop() error = %v", err)
	}
}

func TestControllerStartReturnsListenerFailureAndPreservesItInStatus(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer occupied.Close()
	config := testConfig()
	config.Management.Address = occupied.Addr().String()
	controller, err := companionruntime.NewController(func() (*companionruntime.Runtime, error) {
		return companionruntime.New(config)
	})
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}
	if err = controller.Start(); err == nil {
		t.Fatal("Start() accepted occupied management address")
	}
	status := controller.Status()
	if status.State != companionruntime.StateStopped || status.LastError == "" {
		t.Fatalf("status after failed Start = %#v", status)
	}
}

func TestControllerStopWhileFactoryIsStartingPreventsListenerStart(t *testing.T) {
	for iteration := 0; iteration < 50; iteration++ {
		release := make(chan struct{})
		factoryEntered := make(chan struct{})
		var once sync.Once
		controller, err := companionruntime.NewController(func() (*companionruntime.Runtime, error) {
			once.Do(func() { close(factoryEntered) })
			<-release
			return companionruntime.New(testConfig())
		})
		if err != nil {
			t.Fatalf("NewController() error = %v", err)
		}
		startDone := make(chan error, 1)
		go func() { startDone <- controller.Start() }()
		<-factoryEntered
		stopDone := make(chan error, 1)
		go func() { stopDone <- controller.Stop(context.Background()) }()
		close(release)
		if err = <-startDone; err != nil {
			t.Fatalf("iteration %d Start() error = %v", iteration, err)
		}
		if err = <-stopDone; err != nil {
			t.Fatalf("iteration %d Stop() error = %v", iteration, err)
		}
		if status := controller.Status(); status.State != companionruntime.StateStopped || status.ConnectedDecks != 0 {
			t.Fatalf("iteration %d status = %#v, want stopped without connected Decks", iteration, status)
		}
	}
}

func waitForControllerState(
	t *testing.T,
	controller *companionruntime.Controller,
	want companionruntime.State,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if controller.Status().State == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("controller did not reach %q; last status = %#v", want, controller.Status())
}
