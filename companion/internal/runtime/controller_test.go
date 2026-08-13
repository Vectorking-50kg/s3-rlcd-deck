package runtime_test

import (
	"context"
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

func TestControllerStopWhileFactoryIsStartingPreventsListenerStart(t *testing.T) {
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
		t.Fatalf("Start() error = %v", err)
	}
	if err = <-stopDone; err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if status := controller.Status(); status.State != companionruntime.StateStopped || status.ConnectedDecks != 0 {
		t.Fatalf("status = %#v, want stopped without connected Decks", status)
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
