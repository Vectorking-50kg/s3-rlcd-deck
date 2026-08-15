package runtime

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/codexappserver"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/configmodel"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/cursorprovider"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/devicelink"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/history"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairing"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/serialhub"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/structuredprovider"
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
	State                      State  `json:"state"`
	Version                    string `json:"version"`
	ManagementAddress          string `json:"management_address"`
	DeviceHubAddress           string `json:"device_hub_address"`
	DeviceHubAdvertisedAddress string `json:"device_hub_advertised_address,omitempty"`
	ConnectedDecks             int    `json:"connected_decks"`
	LANManagementEnabled       bool   `json:"lan_management_enabled"`
	SecurityWarning            string `json:"security_warning,omitempty"`
	LastError                  string `json:"last_error,omitempty"`
	HistoryAvailable           bool   `json:"history_available"`
	HistoryEnabled             bool   `json:"history_enabled"`
}

type Runtime struct {
	config Config

	managementHandler       http.Handler
	deviceHubHandler        http.Handler
	shutdownTimeout         time.Duration
	sessions                *managementSessions
	consoleAccess           *consoleAccessGrants
	pairing                 *pairing.Service
	deviceLink              *devicelink.Hub
	serialHub               *serialhub.Service
	serialObservers         serialObserverRegistry
	codexCollector          CodexCollector
	codexObserver           CodexObserver
	cursorCollector         CursorCollector
	structuredCollectors    []StructuredCollector
	structuredService       *structuredprovider.Service
	structuredProviderOrder []string
	history                 *history.Store
	backup                  BackupService
	configuration           ConfigurationOwner
	deviceProfileUpdates    chan configmodel.DeviceProfile
	advertisedAddress       func(context.Context, string, string) string

	mu                  sync.RWMutex
	snapshotMu          sync.Mutex
	status              Status
	started             bool
	codexUpdate         codexappserver.Update
	hasCodexUpdate      bool
	codexSessions       []aisnapshot.Session
	cursorProvider      aisnapshot.Provider
	hasCursorProvider   bool
	structuredProviders map[string]aisnapshot.Provider
}

func New(config Config) (*Runtime, error) {
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	status := Status{
		State:                      StateNew,
		Version:                    normalized.Version,
		ManagementAddress:          normalized.Management.Address,
		DeviceHubAddress:           normalized.DeviceHub.Address,
		DeviceHubAdvertisedAddress: normalized.DeviceHub.AdvertisedAddress,
		LANManagementEnabled:       normalized.Management.AllowLAN,
	}
	if normalized.Management.AllowLAN {
		status.SecurityWarning = "management Web is exposed beyond loopback"
	}
	var deviceProfileUpdates chan configmodel.DeviceProfile
	var onDeviceProfile func(configmodel.DeviceProfile)
	if normalized.Configuration != nil {
		deviceProfileUpdates = make(chan configmodel.DeviceProfile, 32)
		onDeviceProfile = func(profile configmodel.DeviceProfile) {
			select {
			case deviceProfileUpdates <- profile:
			default:
			}
		}
	}
	serialService, err := serialhub.NewService(serialhub.ServiceConfig{})
	if err != nil {
		return nil, err
	}
	deviceLink, err := devicelink.New(devicelink.Config{
		Authenticator:     normalized.Pairing,
		HeartbeatInterval: normalized.DeviceHub.HeartbeatInterval,
		HeartbeatTimeout:  normalized.DeviceHub.HeartbeatTimeout,
		OnDeviceProfile:   onDeviceProfile,
		OnSerialState: func(deviceID string, sessionID uint64, state string) error {
			return serialService.Reconcile(deviceID, sessionID, serialhub.State(state))
		},
		OnSerialFrame: serialService.Ingest,
		OnDisconnect: func(deviceID string) {
			serialService.RequireOwnerRevocation(deviceID)
		},
		OnSerialOwnerResult: func(deviceID string, result devicelink.SerialOwnerResult) error {
			owner := serialhub.OwnerUnavailable
			if result.SerialState == "usb_tx" {
				owner = serialhub.OwnerUSB
			} else if result.SerialState == "web_tx" {
				owner = serialhub.OwnerWeb
			}
			ownerResult := serialhub.OwnerResult{
				SessionID: result.SerialSessionID, RequestID: result.RequestID,
				Owner: owner, LeaseID: result.LeaseID,
			}
			accepted := result.Code == "applied" || result.Code == "no_change"
			return serialService.ApplyOwnerResult(deviceID, ownerResult, accepted)
		},
		SerialHistoryCursor: func(deviceID string, sessionID uint64) (uint64, bool) {
			status := serialService.Status()
			if status.DeviceID != deviceID || status.SessionID != sessionID {
				return 0, false
			}
			return serialService.Ring().Stats().NewestSequence, true
		},
	})
	if err != nil {
		serialService.Close()
		return nil, err
	}
	return &Runtime{
		config:               normalized,
		shutdownTimeout:      shutdownTimeout,
		sessions:             newManagementSessions(),
		consoleAccess:        &consoleAccessGrants{},
		pairing:              normalized.Pairing,
		deviceLink:           deviceLink,
		serialHub:            serialService,
		codexCollector:       normalized.CodexCollector,
		codexObserver:        normalized.CodexObserver,
		cursorCollector:      normalized.CursorCollector,
		structuredCollectors: normalized.StructuredCollectors,
		structuredService:    normalized.StructuredProviders,
		structuredProviderOrder: func() []string {
			order := make([]string, len(normalized.StructuredCollectors))
			for index, collector := range normalized.StructuredCollectors {
				order[index] = collector.ProviderID()
			}
			return order
		}(),
		history:              normalized.History,
		backup:               normalized.Backup,
		configuration:        normalized.Configuration,
		advertisedAddress:    liveDeviceHubAdvertisedAddress,
		deviceProfileUpdates: deviceProfileUpdates,
		structuredProviders:  make(map[string]aisnapshot.Provider),
		status:               status,
	}, nil
}

// StructuredProviders returns configured-order, independently owned Provider
// pages. Raw requests, responses, and credentials remain inside collectors.
func (application *Runtime) StructuredProviders() []aisnapshot.Provider {
	application.mu.RLock()
	providers := make([]aisnapshot.Provider, 0, len(application.structuredProviders))
	for _, providerID := range application.structuredProviderOrder {
		if provider, exists := application.structuredProviders[providerID]; exists {
			providers = append(providers, provider.Clone())
		}
	}
	application.mu.RUnlock()
	return providers
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
	if application.history != nil {
		status.HistoryAvailable = application.history.Available()
		status.HistoryEnabled = status.HistoryAvailable && application.history.Enabled()
	}
	return status
}

func (application *Runtime) deviceHubAdvertisedAddress(ctx context.Context) string {
	application.mu.RLock()
	boundAddress := application.status.DeviceHubAddress
	resolver := application.advertisedAddress
	application.mu.RUnlock()
	if resolver == nil {
		return ""
	}
	return resolver(ctx, boundAddress, application.config.DeviceHub.AdvertisedAddress)
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
	collectorDone := make([]chan error, 0, 5+len(application.structuredCollectors))
	serialLeaseDone := make(chan error, 1)
	collectorDone = append(collectorDone, serialLeaseDone)
	go func() {
		serialLeaseDone <- application.runSerialLeaseSupervisor(collectorContext)
	}()
	if application.deviceProfileUpdates != nil {
		done := make(chan error, 1)
		collectorDone = append(collectorDone, done)
		go func() {
			for {
				select {
				case <-collectorContext.Done():
					done <- nil
					return
				case profile := <-application.deviceProfileUpdates:
					updateContext, cancel := context.WithTimeout(collectorContext, 2*time.Second)
					_ = application.configuration.UpdateDeviceProfile(updateContext, profile)
					cancel()
				}
			}
		}()
	}
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
	if application.structuredService != nil {
		done := make(chan error, 1)
		collectorDone = append(collectorDone, done)
		go func() {
			done <- application.structuredService.Run(
				collectorContext,
				application.publishStructuredState,
			)
		}()
	}
	for _, collector := range application.structuredCollectors {
		if collector == nil {
			continue
		}
		done := make(chan error, 1)
		collectorDone = append(collectorDone, done)
		go func(owned StructuredCollector, result chan<- error) {
			result <- owned.Run(collectorContext, func(ctx context.Context, provider aisnapshot.Provider) error {
				if provider.ID != owned.ProviderID() {
					return structuredprovider.ErrUnavailable
				}
				return application.publishStructuredProvider(ctx, provider)
			})
		}(collector, done)
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
	observerShutdownError := application.closeSerialObservers(shutdownContext)
	serialRevokeError := application.revokeSerialOwnerForShutdown(shutdownContext)
	application.deviceLink.Close()
	application.serialHub.Close()
	shutdownErrors := errors.Join(
		observerShutdownError,
		serialRevokeError,
		shutdownServers(shutdownContext, managementServer, deviceHubServer),
	)
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
	ctx context.Context,
	update codexappserver.Update,
) error {
	application.mu.Lock()
	application.codexUpdate = update.Clone()
	application.hasCodexUpdate = true
	application.mu.Unlock()
	application.captureHistory(ctx, update.Provider)
	application.publishCurrentSnapshot()
	return nil
}

func (application *Runtime) publishCursorProvider(
	ctx context.Context,
	provider aisnapshot.Provider,
) error {
	if provider.ID != "cursor" || !provider.Experimental {
		return cursorprovider.ErrUnavailable
	}
	application.mu.Lock()
	application.cursorProvider = provider.Clone()
	application.hasCursorProvider = true
	application.mu.Unlock()
	application.captureHistory(ctx, provider)
	application.publishCurrentSnapshot()
	return nil
}

func (application *Runtime) publishStructuredProvider(
	ctx context.Context,
	provider aisnapshot.Provider,
) error {
	if err := validateStructuredProvider(provider); err != nil {
		return err
	}
	application.mu.Lock()
	application.structuredProviders[provider.ID] = provider.Clone()
	application.mu.Unlock()
	application.captureHistory(ctx, provider)
	application.publishCurrentSnapshot()
	return nil
}

func (application *Runtime) publishStructuredState(
	ctx context.Context,
	order []string,
	providers []aisnapshot.Provider,
) error {
	if len(order) > 6 || len(providers) > len(order) {
		return structuredprovider.ErrUnavailable
	}
	allowed := make(map[string]struct{}, len(order))
	for _, providerID := range order {
		if providerID == "" || providerID == "codex" || providerID == "cursor" {
			return structuredprovider.ErrUnavailable
		}
		if _, duplicate := allowed[providerID]; duplicate {
			return structuredprovider.ErrUnavailable
		}
		allowed[providerID] = struct{}{}
	}
	next := make(map[string]aisnapshot.Provider, len(providers))
	for _, provider := range providers {
		if _, configured := allowed[provider.ID]; !configured ||
			validateStructuredProvider(provider) != nil {
			return structuredprovider.ErrUnavailable
		}
		if _, duplicate := next[provider.ID]; duplicate {
			return structuredprovider.ErrUnavailable
		}
		next[provider.ID] = provider.Clone()
	}
	var changed []aisnapshot.Provider
	application.mu.Lock()
	for id, provider := range next {
		if previous, exists := application.structuredProviders[id]; !exists || !reflect.DeepEqual(previous, provider) {
			changed = append(changed, provider.Clone())
		}
	}
	application.structuredProviderOrder = append([]string(nil), order...)
	application.structuredProviders = next
	application.mu.Unlock()
	for _, provider := range changed {
		application.captureHistory(ctx, provider)
	}
	application.publishCurrentSnapshot()
	return nil
}

func validateStructuredProvider(provider aisnapshot.Provider) error {
	generatedAt := time.Now().UTC()
	if provider.UpdatedAt != nil {
		parsed, err := time.Parse(time.RFC3339Nano, *provider.UpdatedAt)
		if err != nil {
			return structuredprovider.ErrUnavailable
		}
		generatedAt = parsed
	}
	if provider.ID == "" || len(provider.ID) > 32 || provider.ID == "codex" || provider.ID == "cursor" ||
		(provider.Source == aisnapshot.ProviderSourceStructuredHTTP &&
			(provider.Status != aisnapshot.ProviderOK && provider.Status != aisnapshot.ProviderDegraded ||
				provider.Confidence != aisnapshot.ConfidenceVerified)) ||
		(provider.Source == aisnapshot.ProviderSourceNone &&
			(provider.Status != aisnapshot.ProviderUnavailable ||
				provider.Confidence != aisnapshot.ConfidenceUnavailable)) ||
		provider.Source != aisnapshot.ProviderSourceStructuredHTTP && provider.Source != aisnapshot.ProviderSourceNone ||
		aisnapshot.ValidateProvider(provider, generatedAt) != nil {
		return structuredprovider.ErrUnavailable
	}
	return nil
}

func (application *Runtime) captureHistory(ctx context.Context, provider aisnapshot.Provider) {
	if application.history == nil {
		return
	}
	// Capture is a validated bounded-queue transfer. History backpressure or
	// storage failure is intentionally Provider-independent and cannot escape
	// into collector lifecycle.
	_ = application.history.Capture(ctx, provider, time.Now().UTC())
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
	application.publishCurrentSnapshot()
	return nil
}

func (application *Runtime) composeSnapshotDocument(now time.Time) ([]byte, error) {
	now = now.UTC()
	application.mu.RLock()
	providers := make([]aisnapshot.Provider, 0, 2+len(application.structuredProviderOrder))
	providerOrder := make([]string, 0, cap(providers))
	sessions := make([]aisnapshot.Session, 0, 16)
	if application.hasCodexUpdate {
		providers = append(providers, application.codexUpdate.Provider.Clone())
		providerOrder = append(providerOrder, application.codexUpdate.Provider.ID)
		seen := make(map[string]struct{}, len(application.codexUpdate.Sessions)+len(application.codexSessions))
		for _, session := range application.codexUpdate.Sessions {
			seen[session.ID] = struct{}{}
			sessions = append(sessions, cloneSession(session))
		}
		for _, session := range application.codexSessions {
			if len(sessions) >= 16 {
				break
			}
			if _, duplicate := seen[session.ID]; duplicate {
				continue
			}
			seen[session.ID] = struct{}{}
			sessions = append(sessions, cloneSession(session))
		}
	}
	if application.hasCursorProvider {
		providers = append(providers, application.cursorProvider.Clone())
		providerOrder = append(providerOrder, application.cursorProvider.ID)
	}
	for _, providerID := range application.structuredProviderOrder {
		if provider, exists := application.structuredProviders[providerID]; exists {
			providers = append(providers, provider.Clone())
			providerOrder = append(providerOrder, provider.ID)
		}
	}
	application.mu.RUnlock()
	if len(providers) == 0 {
		return nil, nil
	}
	defaultTimezone := "Asia/Shanghai"
	return aisnapshot.Encode(aisnapshot.Snapshot{
		Type:              "snapshot.ai",
		ProtocolVersion:   protocol.CurrentVersion,
		SchemaVersion:     aisnapshot.SchemaVersion{Major: aisnapshot.SchemaMajor, Minor: aisnapshot.SchemaMinor},
		GeneratedAt:       now.Format(time.RFC3339Nano),
		GeneratedAtUnixMS: now.UnixMilli(),
		Timezone:          &defaultTimezone,
		ProviderOrder:     providerOrder,
		Providers:         providers,
		Sessions:          sessions,
		NextRefresh:       5,
	})
}

func (application *Runtime) publishCurrentSnapshot() {
	application.snapshotMu.Lock()
	defer application.snapshotMu.Unlock()
	document, err := application.composeSnapshotDocument(time.Now())
	if err != nil || len(document) == 0 {
		return
	}
	_ = application.deviceLink.PublishSnapshot(document)
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
	deviceHubAdvertisedAddress := resolveDeviceHubAdvertisedAddress(
		deviceHubAddress,
		application.config.DeviceHub.AdvertisedAddress,
		nil,
	)
	application.mu.Lock()
	defer application.mu.Unlock()
	application.status.State = state
	application.status.ManagementAddress = managementAddress
	application.status.DeviceHubAddress = deviceHubAddress
	application.status.DeviceHubAdvertisedAddress = deviceHubAdvertisedAddress
}
