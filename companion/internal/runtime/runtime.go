package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	webapp "github.com/Vectorking-50kg/s3-rlcd-deck/companion/web"
)

const shutdownTimeout = 5 * time.Second

var (
	ErrAlreadyRun                   = errors.New("companion runtime can only be run once")
	ErrManagementAddressNotLoopback = errors.New("management address must be loopback")
)

type State string

const (
	StateNew     State = "new"
	StateReady   State = "ready"
	StateStopped State = "stopped"
)

type Config struct {
	ManagementAddress string
	Version           string
}

type Status struct {
	State             State  `json:"state"`
	Version           string `json:"version"`
	ManagementAddress string `json:"management_address"`
}

type Runtime struct {
	config Config

	mu      sync.RWMutex
	status  Status
	started bool
}

func New(config Config) (*Runtime, error) {
	if config.ManagementAddress == "" {
		config.ManagementAddress = "127.0.0.1:7777"
	}
	if config.Version == "" {
		return nil, errors.New("companion version is required")
	}
	host, _, err := net.SplitHostPort(config.ManagementAddress)
	if err != nil {
		return nil, fmt.Errorf("invalid management address: %w", err)
	}
	address := net.ParseIP(host)
	if address == nil || !address.IsLoopback() {
		return nil, ErrManagementAddressNotLoopback
	}
	return &Runtime{
		config: config,
		status: Status{
			State:             StateNew,
			Version:           config.Version,
			ManagementAddress: config.ManagementAddress,
		},
	}, nil
}

func (application *Runtime) Status() Status {
	application.mu.RLock()
	defer application.mu.RUnlock()
	return application.status
}

func (application *Runtime) Run(ctx context.Context) error {
	application.mu.Lock()
	if application.started {
		application.mu.Unlock()
		return ErrAlreadyRun
	}
	application.started = true
	application.mu.Unlock()

	listener, err := net.Listen("tcp", application.config.ManagementAddress)
	if err != nil {
		application.setState(StateStopped, application.config.ManagementAddress)
		return fmt.Errorf("listen on management address: %w", err)
	}
	application.setState(StateReady, listener.Addr().String())

	server := &http.Server{
		Handler:           application.routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()

	select {
	case err = <-serveResult:
		application.setState(StateStopped, listener.Addr().String())
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve management web: %w", err)
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		err = server.Shutdown(shutdownContext)
		serveErr := <-serveResult
		application.setState(StateStopped, listener.Addr().String())
		if err != nil {
			return fmt.Errorf("shut down management web: %w", err)
		}
		if !errors.Is(serveErr, http.ErrServerClosed) {
			return fmt.Errorf("serve management web: %w", serveErr)
		}
		return nil
	}
}

func (application *Runtime) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/status", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Cache-Control", "no-store")
		if err := json.NewEncoder(response).Encode(application.Status()); err != nil {
			return
		}
	})
	mux.Handle("/", webapp.Handler())
	return mux
}

func (application *Runtime) setState(state State, managementAddress string) {
	application.mu.Lock()
	defer application.mu.Unlock()
	application.status.State = state
	application.status.ManagementAddress = managementAddress
}
