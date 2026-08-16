package desktop

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	companionruntime "github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/runtime"
)

const (
	statusRefreshInterval = time.Second
	consoleAccessDuration = 30 * time.Second
)

type Controller interface {
	Start() error
	Stop(context.Context) error
	Status() companionruntime.Status
	ConsoleAccessURL(time.Duration) (string, error)
}

type Tray interface {
	SetIcon([]byte)
	SetTemplateIcon([]byte)
	SetTooltip(string)
	SetStatus(string)
	SetRunning(bool)
	OnOpenConsole(func())
	OnToggleRunning(func())
	OnQuit(func())
	Show()
	Run() error
	Close()
}

type Shell struct {
	controller Controller
	tray       Tray
	openURL    func(string) error
	icon       []byte
	stop       chan struct{}
	done       chan struct{}
	actionMu   sync.Mutex
	stopOnce   sync.Once
	closeOnce  sync.Once
}

func NewShell(controller Controller, tray Tray, icon []byte, openURL func(string) error) (*Shell, error) {
	if controller == nil || tray == nil || len(icon) == 0 || openURL == nil {
		return nil, errors.New("desktop shell requires controller, tray, icon, and URL opener")
	}
	return &Shell{
		controller: controller,
		tray:       tray,
		openURL:    openURL,
		icon:       append([]byte(nil), icon...),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}, nil
}

func (shell *Shell) Run() error {
	shell.tray.SetIcon(shell.icon)
	if runtime.GOOS == "darwin" {
		shell.tray.SetTemplateIcon(shell.icon)
	}
	shell.tray.OnOpenConsole(func() { go shell.openConsole() })
	shell.tray.OnToggleRunning(func() { go shell.toggleRuntime() })
	shell.tray.OnQuit(func() { go shell.Close() })
	shell.refresh()
	shell.tray.Show()
	go shell.refreshLoop()
	err := shell.tray.Run()
	shell.stopRefresh()
	<-shell.done
	shell.Close()
	return err
}

func (shell *Shell) stopRefresh() {
	shell.stopOnce.Do(func() { close(shell.stop) })
}

func (shell *Shell) refreshLoop() {
	defer close(shell.done)
	ticker := time.NewTicker(statusRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			shell.refresh()
		case <-shell.stop:
			return
		}
	}
}

func (shell *Shell) refresh() {
	status := shell.controller.Status()
	label := "Stopped"
	if status.State == companionruntime.StateReady {
		deckLabel := "Decks"
		if status.ConnectedDecks == 1 {
			deckLabel = "Deck"
		}
		label = fmt.Sprintf("Running · %d %s connected", status.ConnectedDecks, deckLabel)
	} else if status.State == companionruntime.StateNew {
		label = "Starting"
	} else if status.LastError != "" {
		label = "Error · " + status.LastError
	}
	visibleStatus := label
	if status.Version != "" {
		visibleStatus = status.Version + " · " + label
	}
	shell.tray.SetStatus(visibleStatus)
	shell.tray.SetRunning(status.State != companionruntime.StateStopped)
	shell.tray.SetTooltip("S3 RLCD Deck Companion · " + visibleStatus)
}

func (shell *Shell) openConsole() {
	accessURL, err := shell.controller.ConsoleAccessURL(consoleAccessDuration)
	if err != nil {
		shell.showActionError(err)
		return
	}
	if err = shell.openURL(accessURL); err != nil {
		shell.showActionError(err)
	}
}

func (shell *Shell) toggleRuntime() {
	shell.actionMu.Lock()
	defer shell.actionMu.Unlock()
	if shell.controller.Status().State == companionruntime.StateStopped {
		if err := shell.controller.Start(); err != nil {
			shell.showActionError(err)
			return
		}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		if err := shell.controller.Stop(ctx); err != nil {
			cancel()
			shell.showActionError(err)
			return
		}
		cancel()
	}
	shell.refresh()
}

func (shell *Shell) showActionError(err error) {
	shell.tray.SetStatus("Error · " + err.Error())
	shell.tray.SetTooltip("S3 RLCD Deck Companion · Error · " + err.Error())
}

func (shell *Shell) Close() {
	shell.closeOnce.Do(func() {
		// Stop new UI refreshes and release the native event loop before
		// waiting for Runtime teardown. This keeps SIGTERM responsive even
		// when a subsystem needs the full bounded shutdown budget.
		shell.stopRefresh()
		shell.tray.Close()
		shell.actionMu.Lock()
		defer shell.actionMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		_ = shell.controller.Stop(ctx)
		cancel()
	})
}
