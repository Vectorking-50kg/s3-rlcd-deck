package runtime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairing"
)

const shutdownTimeout = 5 * time.Second

var ErrAlreadyRun = errors.New("companion runtime can only be run once")

type State string

const (
	StateNew     State = "new"
	StateReady   State = "ready"
	StateStopped State = "stopped"
)

type Status struct {
	State                State  `json:"state"`
	Version              string `json:"version"`
	ManagementAddress    string `json:"management_address"`
	DeviceHubAddress     string `json:"device_hub_address"`
	LANManagementEnabled bool   `json:"lan_management_enabled"`
	SecurityWarning      string `json:"security_warning,omitempty"`
}

type Runtime struct {
	config Config

	managementHandler http.Handler
	deviceHubHandler  http.Handler
	shutdownTimeout   time.Duration
	sessions          *managementSessions
	pairing           *pairing.Service

	mu      sync.RWMutex
	status  Status
	started bool
}

func New(config Config) (*Runtime, error) {
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	status := Status{
		State:                StateNew,
		Version:              normalized.Version,
		ManagementAddress:    normalized.Management.Address,
		DeviceHubAddress:     normalized.DeviceHub.Address,
		LANManagementEnabled: normalized.Management.AllowLAN,
	}
	if normalized.Management.AllowLAN {
		status.SecurityWarning = "management Web is exposed beyond loopback"
	}
	return &Runtime{
		config:          normalized,
		shutdownTimeout: shutdownTimeout,
		sessions:        newManagementSessions(),
		pairing:         normalized.Pairing,
		status:          status,
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

	managementListener, err := net.Listen("tcp", application.config.Management.Address)
	if err != nil {
		application.setState(StateStopped, application.config.Management.Address, application.config.DeviceHub.Address)
		return fmt.Errorf("listen on management address: %w", err)
	}
	deviceHubListener, err := net.Listen("tcp", application.config.DeviceHub.Address)
	if err != nil {
		_ = managementListener.Close()
		application.setState(StateStopped, managementListener.Addr().String(), application.config.DeviceHub.Address)
		return fmt.Errorf("listen on Device Hub address: %w", err)
	}

	managementHandler := application.managementHandler
	if managementHandler == nil {
		managementHandler = application.managementRoutes()
	}
	deviceHubHandler := application.deviceHubHandler
	if deviceHubHandler == nil {
		deviceHubHandler = application.deviceHubRoutes()
	}
	managementLimits := application.config.Management.Limits
	managementServer := &http.Server{
		Handler:           managementHandler,
		ReadHeaderTimeout: managementLimits.ReadHeaderTimeout,
		ReadTimeout:       managementLimits.ReadTimeout,
		WriteTimeout:      managementLimits.WriteTimeout,
		IdleTimeout:       managementLimits.IdleTimeout,
		MaxHeaderBytes:    managementLimits.MaxHeaderBytes,
	}
	limits := application.config.DeviceHub.Limits
	deviceHubServer := &http.Server{
		Handler:           deviceHubHandler,
		ReadHeaderTimeout: limits.ReadHeaderTimeout,
		ReadTimeout:       limits.ReadTimeout,
		WriteTimeout:      limits.WriteTimeout,
		IdleTimeout:       limits.IdleTimeout,
		MaxHeaderBytes:    limits.MaxHeaderBytes,
	}
	application.setState(StateReady, managementListener.Addr().String(), deviceHubListener.Addr().String())
	managementListener = newConnectionLimitedListener(
		managementListener,
		managementLimits.MaxConcurrent,
		managementLimits.MaxConcurrentPerIP,
	)
	deviceHubListener = newConnectionLimitedListener(
		deviceHubListener,
		limits.MaxConcurrent,
		limits.MaxConcurrentPerIP,
	)

	type serveResult struct {
		name string
		err  error
	}
	serveResults := make(chan serveResult, 2)
	go func() {
		serveResults <- serveResult{name: "management web", err: managementServer.Serve(managementListener)}
	}()
	go func() {
		serveResults <- serveResult{name: "Device Hub", err: deviceHubServer.Serve(deviceHubListener)}
	}()

	var trigger serveResult
	select {
	case trigger = <-serveResults:
	case <-ctx.Done():
		trigger = serveResult{name: "context", err: http.ErrServerClosed}
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), application.shutdownTimeout)
	defer cancel()
	shutdownErrors := shutdownServers(shutdownContext, managementServer, deviceHubServer)
	application.setState(StateStopped, managementListener.Addr().String(), deviceHubListener.Addr().String())

	if trigger.name != "context" && !errors.Is(trigger.err, http.ErrServerClosed) {
		return fmt.Errorf("serve %s: %w", trigger.name, trigger.err)
	}
	if shutdownErrors != nil {
		return fmt.Errorf("shut down Companion listeners: %w", shutdownErrors)
	}
	return nil
}

func shutdownServers(ctx context.Context, servers ...*http.Server) error {
	type shutdownResult struct {
		server *http.Server
		err    error
	}
	results := make(chan shutdownResult, len(servers))
	for _, server := range servers {
		go func(server *http.Server) {
			results <- shutdownResult{server: server, err: server.Shutdown(ctx)}
		}(server)
	}
	var joined error
	for range servers {
		result := <-results
		if result.err != nil {
			joined = errors.Join(joined, result.err, result.server.Close())
		}
	}
	return joined
}

func (application *Runtime) setState(state State, managementAddress string, deviceHubAddress string) {
	application.mu.Lock()
	defer application.mu.Unlock()
	application.status.State = state
	application.status.ManagementAddress = managementAddress
	application.status.DeviceHubAddress = deviceHubAddress
}
