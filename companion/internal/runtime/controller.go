package runtime

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	ErrControllerStarting = errors.New("Companion runtime is starting")
	ErrControllerReady    = errors.New("Companion runtime is already running")
)

type RuntimeFactory func() (*Runtime, error)

// Controller owns restartable Runtime generations. It is the only interface a
// desktop shell needs for lifecycle and status; listeners and device state stay
// inside Runtime.
type Controller struct {
	factory RuntimeFactory

	mu           sync.Mutex
	application  *Runtime
	cancel       context.CancelFunc
	done         chan error
	starting     bool
	startDone    chan struct{}
	stopStarting bool
	lastStatus   Status
}

func NewController(factory RuntimeFactory) (*Controller, error) {
	if factory == nil {
		return nil, errors.New("runtime factory is required")
	}
	return &Controller{
		factory:    factory,
		lastStatus: Status{State: StateStopped},
	}, nil
}

func (controller *Controller) Start() error {
	controller.mu.Lock()
	if controller.starting {
		controller.mu.Unlock()
		return ErrControllerStarting
	}
	if controller.application != nil {
		controller.mu.Unlock()
		return ErrControllerReady
	}
	controller.starting = true
	controller.startDone = make(chan struct{})
	controller.stopStarting = false
	startDone := controller.startDone
	controller.mu.Unlock()

	application, err := controller.factory()
	controller.mu.Lock()
	controller.starting = false
	stopStarting := controller.stopStarting
	controller.stopStarting = false
	controller.startDone = nil
	close(startDone)
	if err != nil {
		controller.mu.Unlock()
		return err
	}
	if stopStarting {
		controller.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	controller.application = application
	controller.cancel = cancel
	controller.done = done
	controller.mu.Unlock()

	go func() {
		runErr := application.Run(ctx)
		controller.mu.Lock()
		controller.lastStatus = application.Status()
		controller.application = nil
		controller.cancel = nil
		controller.done = nil
		controller.mu.Unlock()
		done <- runErr
		close(done)
	}()
	return nil
}

func (controller *Controller) Stop(ctx context.Context) error {
	controller.mu.Lock()
	if controller.starting {
		controller.stopStarting = true
		startDone := controller.startDone
		controller.mu.Unlock()
		select {
		case <-startDone:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	application := controller.application
	cancel := controller.cancel
	done := controller.done
	controller.mu.Unlock()
	if application == nil || cancel == nil || done == nil {
		return nil
	}
	cancel()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (controller *Controller) Status() Status {
	controller.mu.Lock()
	application := controller.application
	starting := controller.starting
	status := controller.lastStatus
	controller.mu.Unlock()
	if application != nil {
		return application.Status()
	}
	if starting {
		status.State = StateNew
		return status
	}
	status.State = StateStopped
	status.ConnectedDecks = 0
	return status
}

func (controller *Controller) ConsoleAccessURL(validFor time.Duration) (string, error) {
	if validFor <= 0 {
		return "", errors.New("console access duration must be positive")
	}
	controller.mu.Lock()
	application := controller.application
	controller.mu.Unlock()
	if application == nil || application.Status().State != StateReady {
		return "", errors.New("Companion runtime is not ready")
	}
	return application.IssueConsoleAccess(time.Now().Add(validFor))
}
