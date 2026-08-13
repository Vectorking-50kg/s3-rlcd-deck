package desktop

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	companionruntime "github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/runtime"
)

type fakeController struct {
	mu        sync.Mutex
	status    companionruntime.Status
	starts    int
	stops     int
	accesses  int
	startErr  error
	stopErr   error
	accessErr error
}

func (controller *fakeController) Start() error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	controller.starts++
	if controller.startErr != nil {
		return controller.startErr
	}
	controller.status.State = companionruntime.StateReady
	return nil
}

func (controller *fakeController) Stop(context.Context) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	controller.stops++
	if controller.stopErr != nil {
		return controller.stopErr
	}
	controller.status.State = companionruntime.StateStopped
	return nil
}

func (controller *fakeController) Status() companionruntime.Status {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.status
}

func (controller *fakeController) ConsoleAccessURL(time.Duration) (string, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	controller.accesses++
	if controller.accessErr != nil {
		return "", controller.accessErr
	}
	return "http://127.0.0.1:7777/api/v1/desktop/access?token=once", nil
}

func TestShellMakesControllerActionFailuresVisible(t *testing.T) {
	controller := &fakeController{
		status:   companionruntime.Status{State: companionruntime.StateStopped},
		startErr: errors.New("address unavailable"),
	}
	tray := newFakeTray()
	shell, err := NewShell(controller, tray, []byte("png"), func(string) error {
		return errors.New("browser unavailable")
	})
	if err != nil {
		t.Fatalf("NewShell() error = %v", err)
	}
	shell.toggleRuntime()
	tray.mu.Lock()
	status := tray.status
	tray.mu.Unlock()
	if status != "Error · address unavailable" {
		t.Fatalf("tray status = %q", status)
	}

	controller.startErr = nil
	controller.status.State = companionruntime.StateReady
	shell.openConsole()
	tray.mu.Lock()
	status = tray.status
	tray.mu.Unlock()
	if status != "Error · browser unavailable" {
		t.Fatalf("browser failure status = %q", status)
	}
}

type fakeTray struct {
	mu        sync.Mutex
	open      func()
	toggle    func()
	quit      func()
	status    string
	running   bool
	shown     chan struct{}
	closed    chan struct{}
	showOnce  sync.Once
	closeOnce sync.Once
}

func newFakeTray() *fakeTray {
	return &fakeTray{shown: make(chan struct{}), closed: make(chan struct{})}
}

func (*fakeTray) SetIcon([]byte)         {}
func (*fakeTray) SetTemplateIcon([]byte) {}
func (*fakeTray) SetTooltip(string)      {}
func (tray *fakeTray) SetStatus(status string) {
	tray.mu.Lock()
	tray.status = status
	tray.mu.Unlock()
}
func (tray *fakeTray) SetRunning(running bool) {
	tray.mu.Lock()
	tray.running = running
	tray.mu.Unlock()
}
func (tray *fakeTray) OnOpenConsole(callback func())   { tray.open = callback }
func (tray *fakeTray) OnToggleRunning(callback func()) { tray.toggle = callback }
func (tray *fakeTray) OnQuit(callback func())          { tray.quit = callback }
func (tray *fakeTray) Show()                           { tray.showOnce.Do(func() { close(tray.shown) }) }
func (tray *fakeTray) Run() error                      { <-tray.closed; return nil }
func (tray *fakeTray) Close()                          { tray.closeOnce.Do(func() { close(tray.closed) }) }

func TestShellUsesControllerForConsoleLifecycleStatusAndQuit(t *testing.T) {
	controller := &fakeController{status: companionruntime.Status{
		State: companionruntime.StateReady, Version: "1.2.3-test", ConnectedDecks: 2,
	}}
	tray := newFakeTray()
	opened := make(chan string, 1)
	shell, err := NewShell(controller, tray, []byte("png"), func(address string) error {
		opened <- address
		return nil
	})
	if err != nil {
		t.Fatalf("NewShell() error = %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- shell.Run() }()
	select {
	case <-tray.shown:
	case <-time.After(time.Second):
		t.Fatal("shell did not show tray")
	}
	tray.mu.Lock()
	status := tray.status
	running := tray.running
	tray.mu.Unlock()
	if status != "1.2.3-test · Running · 2 Decks connected" || !running {
		t.Fatalf("tray status = %q, running = %t", status, running)
	}
	tray.open()
	select {
	case address := <-opened:
		if address == "" || controller.accessCount() != 1 {
			t.Fatalf("opened = %q, accesses = %d", address, controller.accessCount())
		}
	case <-time.After(time.Second):
		t.Fatal("open console action did not complete")
	}
	tray.toggle()
	waitForShellState(t, controller, companionruntime.StateStopped)
	if controller.stops != 1 {
		t.Fatalf("after toggle: stops = %d status = %#v", controller.stops, controller.Status())
	}
	tray.toggle()
	waitForShellState(t, controller, companionruntime.StateReady)
	if controller.starts != 1 {
		t.Fatalf("after restart: starts = %d status = %#v", controller.starts, controller.Status())
	}
	tray.quit()
	if err = <-done; err != nil {
		t.Fatalf("Shell.Run() error = %v", err)
	}
	if controller.stops != 2 {
		t.Fatalf("quit stop count = %d, want 2", controller.stops)
	}
}

func (controller *fakeController) accessCount() int {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.accesses
}

func waitForShellState(t *testing.T, controller *fakeController, want companionruntime.State) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for controller.Status().State != want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if controller.Status().State != want {
		t.Fatalf("controller did not reach %q; status = %#v", want, controller.Status())
	}
}
