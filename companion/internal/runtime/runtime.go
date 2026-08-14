package runtime

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/codexappserver"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/cursorprovider"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/devicelink"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairing"
)

const shutdownTimeout = 5 * time.Second

var (
	ErrAlreadyRun       = errors.New("companion runtime can only be run once")
	ErrCodexUnavailable = errors.New("Codex collector is unavailable")
)

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
	ConnectedDecks       int    `json:"connected_decks"`
	LANManagementEnabled bool   `json:"lan_management_enabled"`
	SecurityWarning      string `json:"security_warning,omitempty"`
	LastError            string `json:"last_error,omitempty"`
}

type Runtime struct {
	config Config

	managementHandler http.Handler
	deviceHubHandler  http.Handler
	shutdownTimeout   time.Duration
	sessions          *managementSessions
	consoleAccess     *consoleAccessGrants
	pairing           *pairing.Service
	deviceLink        *devicelink.Hub
	codexCollector    CodexCollector
	codexObserver     CodexObserver
	cursorCollector   CursorCollector

	mu                sync.RWMutex
	status            Status
	started           bool
	codexUpdate       codexappserver.Update
	hasCodexUpdate    bool
	codexSessions     []aisnapshot.Session
	cursorProvider    aisnapshot.Provider
	hasCursorProvider bool
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
	deviceLink, err := devicelink.New(devicelink.Config{
		Authenticator:     normalized.Pairing,
		HeartbeatInterval: normalized.DeviceHub.HeartbeatInterval,
		HeartbeatTimeout:  normalized.DeviceHub.HeartbeatTimeout,
	})
	if err != nil {
		return nil, err
	}
	return &Runtime{
		config:          normalized,
		shutdownTimeout: shutdownTimeout,
		sessions:        newManagementSessions(),
		consoleAccess:   &consoleAccessGrants{},
		pairing:         normalized.Pairing,
		deviceLink:      deviceLink,
		codexCollector:  normalized.CodexCollector,
		codexObserver:   normalized.CodexObserver,
		cursorCollector: normalized.CursorCollector,
		status:          status,
	}, nil
}

// CursorProvider returns the latest normalized experimental Cursor page. Raw
// local state, credentials, and private endpoint responses never enter Runtime.
func (application *Runtime) CursorProvider() (aisnapshot.Provider, bool) {
	application.mu.RLock()
	defer application.mu.RUnlock()
	if !application.hasCursorProvider {
		return aisnapshot.Provider{}, false
	}
	return application.cursorProvider.Clone(), true
}

func (application *Runtime) Status() Status {
	application.mu.RLock()
	status := application.status
	application.mu.RUnlock()
	status.ConnectedDecks = application.deviceLink.ConnectedDecks()
	return status
}

// CodexUpdate returns the latest normalized, independently owned adapter DTO.
// Raw App Server responses never enter Runtime.
func (application *Runtime) CodexUpdate() (codexappserver.Update, bool) {
	application.mu.RLock()
	defer application.mu.RUnlock()
	if !application.hasCodexUpdate {
		return codexappserver.Update{}, false
	}
	update := application.codexUpdate.Clone()
	seen := make(map[string]struct{}, len(update.Sessions)+len(application.codexSessions))
	for _, session := range update.Sessions {
		seen[session.ID] = struct{}{}
	}
	for _, session := range application.codexSessions {
		if len(update.Sessions) >= 16 {
			break
		}
		if _, duplicate := seen[session.ID]; duplicate {
			continue
		}
		seen[session.ID] = struct{}{}
		update.Sessions = append(update.Sessions, cloneSession(session))
	}
	return update, true
}

func (application *Runtime) LoadCodexThread(ctx context.Context, threadID string) error {
	if application.codexCollector == nil {
		return ErrCodexUnavailable
	}
	return application.codexCollector.LoadThread(ctx, threadID)
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
	if certificate := application.config.DeviceHub.TLSCertificate; certificate != nil {
		deviceHubListener = tls.NewListener(deviceHubListener, &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{*certificate},
		})
	}

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
	collectorContext, stopCollector := context.WithCancel(ctx)
	collectorDone := make([]chan error, 0, 3)
	if application.codexCollector != nil {
		done := make(chan error, 1)
		collectorDone = append(collectorDone, done)
		go func() {
			done <- application.codexCollector.Run(
				collectorContext,
				application.publishCodexUpdate,
			)
		}()
	}
	if application.codexObserver != nil {
		done := make(chan error, 1)
		collectorDone = append(collectorDone, done)
		go func() {
			done <- application.codexObserver.Run(
				collectorContext,
				application.publishCodexSessions,
			)
		}()
	}
	if application.cursorCollector != nil {
		done := make(chan error, 1)
		collectorDone = append(collectorDone, done)
		go func() {
			done <- application.cursorCollector.Run(
				collectorContext,
				application.publishCursorProvider,
			)
		}()
	}

	var trigger serveResult
	select {
	case trigger = <-serveResults:
	case <-ctx.Done():
		trigger = serveResult{name: "context", err: http.ErrServerClosed}
	}
	stopCollector()

	shutdownContext, cancel := context.WithTimeout(context.Background(), application.shutdownTimeout)
	defer cancel()
	application.deviceLink.Close()
	shutdownErrors := shutdownServers(shutdownContext, managementServer, deviceHubServer)
	for _, done := range collectorDone {
		select {
		case <-done:
		case <-shutdownContext.Done():
			// Collector failure and latency remain scoped to the Codex Provider.
			// Listener shutdown must not inherit that failure mode.
		}
	}
	application.setState(StateStopped, managementListener.Addr().String(), deviceHubListener.Addr().String())

	if trigger.name != "context" && !errors.Is(trigger.err, http.ErrServerClosed) {
		return fmt.Errorf("serve %s: %w", trigger.name, trigger.err)
	}
	if shutdownErrors != nil {
		return fmt.Errorf("shut down Companion listeners: %w", shutdownErrors)
	}
	return nil
}

func (application *Runtime) publishCodexUpdate(
	_ context.Context,
	update codexappserver.Update,
) error {
	application.mu.Lock()
	application.codexUpdate = update.Clone()
	application.hasCodexUpdate = true
	application.mu.Unlock()
	return nil
}

func (application *Runtime) publishCursorProvider(
	_ context.Context,
	provider aisnapshot.Provider,
) error {
	if provider.ID != "cursor" || !provider.Experimental {
		return cursorprovider.ErrUnavailable
	}
	application.mu.Lock()
	application.cursorProvider = provider.Clone()
	application.hasCursorProvider = true
	application.mu.Unlock()
	return nil
}

func (application *Runtime) publishCodexSessions(
	_ context.Context,
	sessions []aisnapshot.Session,
) error {
	if len(sessions) > 16 {
		return ErrCodexUnavailable
	}
	for _, session := range sessions {
		if !validInferredCodexSession(session) {
			return ErrCodexUnavailable
		}
	}
	cloned := make([]aisnapshot.Session, len(sessions))
	for index := range sessions {
		cloned[index] = cloneSession(sessions[index])
	}
	application.mu.Lock()
	application.codexSessions = cloned
	application.mu.Unlock()
	return nil
}

func validInferredCodexSession(session aisnapshot.Session) bool {
	if session.SchemaVersion != (aisnapshot.SchemaVersion{Major: 1, Minor: 0}) ||
		session.ProviderID != "codex" || session.DisplayName != nil ||
		session.Source != aisnapshot.SessionSourceProcessJSONL ||
		session.Confidence != aisnapshot.ConfidenceInferred ||
		session.StartedAt != nil || session.StartedAtUnixMS != nil ||
		session.LastActivityAtUnixMS != nil || session.DurationSeconds != nil ||
		session.TurnTokens != nil || session.ContextUsedBasisPoints != nil ||
		(session.State != aisnapshot.SessionRunning &&
			session.State != aisnapshot.SessionRecent &&
			session.State != aisnapshot.SessionEnded &&
			session.State != aisnapshot.SessionUnknown) {
		return false
	}
	if len(session.ID) != len("codex_")+16 || !strings.HasPrefix(session.ID, "codex_") {
		return false
	}
	for _, character := range session.ID[len("codex_"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	if session.LastActivityAt != nil {
		parsed, err := time.Parse(time.RFC3339Nano, *session.LastActivityAt)
		if err != nil || parsed.Location() != time.UTC ||
			parsed.Format(time.RFC3339Nano) != *session.LastActivityAt {
			return false
		}
	}
	return true
}

func cloneSession(source aisnapshot.Session) aisnapshot.Session {
	cloned := source
	cloned.DisplayName = cloneRuntimePointer(source.DisplayName)
	cloned.StartedAt = cloneRuntimePointer(source.StartedAt)
	cloned.StartedAtUnixMS = cloneRuntimePointer(source.StartedAtUnixMS)
	cloned.LastActivityAt = cloneRuntimePointer(source.LastActivityAt)
	cloned.LastActivityAtUnixMS = cloneRuntimePointer(source.LastActivityAtUnixMS)
	cloned.DurationSeconds = cloneRuntimePointer(source.DurationSeconds)
	cloned.TurnTokens = cloneRuntimePointer(source.TurnTokens)
	cloned.ContextUsedBasisPoints = cloneRuntimePointer(source.ContextUsedBasisPoints)
	return cloned
}

func cloneRuntimePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
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
