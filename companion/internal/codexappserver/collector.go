package codexappserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
)

const (
	defaultRequestTimeout = 5 * time.Second
	defaultReconnectDelay = 2 * time.Second
	maximumThreadIDBytes  = 512
	maximumOwnedThreads   = 16
)

var (
	ErrAlreadyRunning = errors.New("Codex App Server collector is already running")
	ErrThreadLimit    = errors.New("Codex App Server owned-thread limit reached")
)

type publisherError struct {
	err error
}

func (problem *publisherError) Error() string {
	return "publish Codex App Server update"
}

func (problem *publisherError) Unwrap() error {
	return problem.err
}

type Collector struct {
	config Config

	mutex        sync.RWMutex
	started      bool
	client       *rpcClient
	provider     aisnapshot.Provider
	ownedThreads map[string]struct{}
	pendingLoads map[string]*rpcClient
	sessions     map[string]aisnapshot.Session
}

func New(config Config) (*Collector, error) {
	if config.AdapterVersion == 0 {
		config.AdapterVersion = AdapterVersion
	}
	if config.AdapterVersion != AdapterVersion {
		return nil, fmt.Errorf("unsupported Codex App Server adapter version %d", config.AdapterVersion)
	}
	if config.ClientVersion == "" {
		return nil, errors.New("Companion version is required")
	}
	if config.Connector == nil {
		config.Connector = ProcessConnector{}
	}
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = defaultRequestTimeout
	}
	if config.ReconnectDelay <= 0 {
		config.ReconnectDelay = defaultReconnectDelay
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.MaximumDocument <= 0 {
		config.MaximumDocument = defaultMaximumDocument
	}
	if config.MaximumDocument > defaultMaximumDocument {
		return nil, fmt.Errorf("maximum App Server document exceeds %d bytes", defaultMaximumDocument)
	}
	return &Collector{
		config:       config,
		ownedThreads: make(map[string]struct{}),
		pendingLoads: make(map[string]*rpcClient),
		sessions:     make(map[string]aisnapshot.Session),
	}, nil
}

// Run supervises a private App Server process. Connection and provider errors
// are converted to a Codex-only degraded update and retried; they never escape
// into another provider's lifecycle.
func (collector *Collector) Run(ctx context.Context, publish Publisher) error {
	if publish == nil {
		return errors.New("Codex publisher is required")
	}
	collector.mutex.Lock()
	if collector.started {
		collector.mutex.Unlock()
		return ErrAlreadyRunning
	}
	collector.started = true
	collector.mutex.Unlock()
	defer func() {
		collector.mutex.Lock()
		collector.client = nil
		collector.started = false
		collector.clearSessionsLocked()
		collector.mutex.Unlock()
	}()

	for {
		err := collector.runConnection(ctx, publish)
		if ctx.Err() != nil {
			return nil
		}
		var publishFailure *publisherError
		if errors.As(err, &publishFailure) {
			return publishFailure.err
		}
		if err == nil {
			err = ErrProcessExited
		}
		if publishErr := publish(ctx, Update{
			Provider: degradedProvider(err),
			Sessions: []aisnapshot.Session{},
		}); publishErr != nil {
			return publishErr
		}
		timer := time.NewTimer(collector.config.ReconnectDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
	}
}

func (collector *Collector) runConnection(ctx context.Context, publish Publisher) error {
	connection, err := collector.config.Connector.Connect(ctx)
	if err != nil {
		return ErrUnavailable
	}
	client := newRPCClient(connection, collector.config.MaximumDocument)
	defer func() {
		_ = client.Close()
		collector.mutex.Lock()
		if collector.client == client {
			collector.client = nil
		}
		collector.clearSessionsLocked()
		collector.mutex.Unlock()
	}()
	requestContext, cancel := context.WithTimeout(ctx, collector.config.RequestTimeout)
	err = initialize(requestContext, client, collector.config.ClientVersion)
	cancel()
	if err != nil {
		return err
	}
	collector.mutex.Lock()
	collector.client = client
	collector.mutex.Unlock()
	provider, err := collector.collectWithTimeout(ctx, client)
	if err != nil {
		return err
	}
	collector.mutex.Lock()
	collector.provider = provider
	initial := collector.updateLocked()
	collector.mutex.Unlock()
	if err = publish(ctx, initial); err != nil {
		return &publisherError{err: err}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-client.Done():
			return client.TerminalError()
		case event, open := <-client.Notifications():
			if !open {
				return client.TerminalError()
			}
			switch event.method {
			case "account/rateLimits/updated":
				if err = validateRateLimitNotification(event.params); err != nil {
					return err
				}
				provider, err = collector.collectWithTimeout(ctx, client)
				if err != nil {
					return err
				}
				collector.mutex.Lock()
				collector.provider = provider
				update := collector.updateLocked()
				collector.mutex.Unlock()
				if err = publish(ctx, update); err != nil {
					return &publisherError{err: err}
				}
			case "thread/status/changed":
				changed, update, updateErr := collector.acceptThreadStatus(event.params)
				if updateErr != nil {
					return updateErr
				}
				if changed {
					if err = publish(ctx, update); err != nil {
						return &publisherError{err: err}
					}
				}
			}
		}
	}
}

func (collector *Collector) collectWithTimeout(
	ctx context.Context,
	client *rpcClient,
) (aisnapshot.Provider, error) {
	requestContext, cancel := context.WithTimeout(ctx, collector.config.RequestTimeout)
	defer cancel()
	return collectProvider(requestContext, client, collector.config.Now())
}

// LoadThread establishes Verified-state authority by successfully loading the
// thread through this exact App Server connection. Merely observing a thread ID
// or a notification can never enter the owned set.
func (collector *Collector) LoadThread(ctx context.Context, threadID string) error {
	if threadID == "" || len(threadID) > maximumThreadIDBytes {
		return errors.New("invalid Codex thread ID")
	}
	collector.mutex.Lock()
	client := collector.client
	if client == nil {
		collector.mutex.Unlock()
		return ErrUnavailable
	}
	if _, loaded := collector.ownedThreads[threadID]; loaded {
		collector.mutex.Unlock()
		return nil
	}
	if _, pending := collector.pendingLoads[threadID]; pending {
		collector.mutex.Unlock()
		return ErrUnavailable
	}
	if len(collector.ownedThreads)+len(collector.pendingLoads) >= maximumOwnedThreads {
		collector.mutex.Unlock()
		return ErrThreadLimit
	}
	collector.pendingLoads[threadID] = client
	collector.mutex.Unlock()
	defer func() {
		collector.mutex.Lock()
		if collector.pendingLoads[threadID] == client {
			delete(collector.pendingLoads, threadID)
		}
		collector.mutex.Unlock()
	}()
	requestContext, cancel := context.WithTimeout(ctx, collector.config.RequestTimeout)
	defer cancel()
	var response struct {
		Thread json.RawMessage `json:"thread"`
	}
	if err := client.Call(requestContext, "thread/resume", map[string]any{
		"threadId": threadID,
	}, &response); err != nil {
		return err
	}
	defer clear(response.Thread)
	if len(response.Thread) == 0 || string(response.Thread) == "null" {
		return ErrSchemaChanged
	}
	var rawThread map[string]json.RawMessage
	if err := json.Unmarshal(response.Thread, &rawThread); err != nil {
		return ErrSchemaChanged
	}
	defer func() {
		for key, value := range rawThread {
			clear(value)
			delete(rawThread, key)
		}
	}()
	encodedID := rawThread["id"]
	var loadedID string
	if err := strictDecode(encodedID, &loadedID); err != nil || loadedID != threadID {
		return ErrSchemaChanged
	}
	var loadedStatus rawThreadStatus
	if err := strictDecode(rawThread["status"], &loadedStatus); err != nil ||
		loadedStatus.Type == "notLoaded" {
		return ErrSchemaChanged
	}
	if _, err := normalizeThreadState(loadedStatus); err != nil {
		return err
	}
	collector.mutex.Lock()
	if collector.client != client {
		collector.mutex.Unlock()
		return ErrProcessExited
	}
	collector.ownedThreads[threadID] = struct{}{}
	if collector.pendingLoads[threadID] == client {
		delete(collector.pendingLoads, threadID)
	}
	collector.mutex.Unlock()
	return nil
}

type rawThreadStatusNotification struct {
	ThreadID string          `json:"threadId"`
	Status   rawThreadStatus `json:"status"`
}

type rawThreadStatus struct {
	Type        string    `json:"type"`
	ActiveFlags *[]string `json:"activeFlags,omitempty"`
}

func (collector *Collector) acceptThreadStatus(
	document json.RawMessage,
) (bool, Update, error) {
	var event rawThreadStatusNotification
	if err := strictDecode(document, &event); err != nil || event.ThreadID == "" {
		return false, Update{}, ErrSchemaChanged
	}
	collector.mutex.Lock()
	defer collector.mutex.Unlock()
	if _, owned := collector.ownedThreads[event.ThreadID]; !owned {
		return false, Update{}, nil
	}
	if event.Status.Type == "notLoaded" {
		if event.Status.ActiveFlags != nil {
			return false, Update{}, ErrSchemaChanged
		}
		_, hadSession := collector.sessions[event.ThreadID]
		delete(collector.ownedThreads, event.ThreadID)
		delete(collector.sessions, event.ThreadID)
		return hadSession, collector.updateLocked(), nil
	}
	state, err := normalizeThreadState(event.Status)
	if err != nil {
		return false, Update{}, err
	}
	lastActivity := canonicalTime(collector.config.Now())
	identifier := anonymousThreadID(event.ThreadID)
	collector.sessions[event.ThreadID] = aisnapshot.Session{
		SchemaVersion:  aisnapshot.SchemaVersion{Major: 1, Minor: 0},
		ID:             identifier,
		ProviderID:     providerID,
		State:          state,
		Source:         aisnapshot.SessionSourceCodexAppServerOwned,
		Confidence:     aisnapshot.ConfidenceVerified,
		LastActivityAt: &lastActivity,
	}
	return true, collector.updateLocked(), nil
}

func normalizeThreadState(status rawThreadStatus) (aisnapshot.SessionState, error) {
	switch status.Type {
	case "active":
		if status.ActiveFlags == nil {
			return "", ErrSchemaChanged
		}
		state := aisnapshot.SessionRunning
		for _, flag := range *status.ActiveFlags {
			switch flag {
			case "waitingOnApproval":
				return aisnapshot.SessionWaitingApproval, nil
			case "waitingOnUserInput":
				state = aisnapshot.SessionWaitingInput
			default:
				return "", ErrSchemaChanged
			}
		}
		return state, nil
	case "idle":
		if status.ActiveFlags != nil {
			return "", ErrSchemaChanged
		}
		return aisnapshot.SessionCompleted, nil
	case "systemError":
		if status.ActiveFlags != nil {
			return "", ErrSchemaChanged
		}
		return aisnapshot.SessionFailed, nil
	default:
		return "", ErrSchemaChanged
	}
}

func anonymousThreadID(threadID string) string {
	digest := sha256.Sum256([]byte(threadID))
	return "codex_" + hex.EncodeToString(digest[:8])
}

func (collector *Collector) updateLocked() Update {
	keys := make([]string, 0, len(collector.sessions))
	for threadID := range collector.sessions {
		keys = append(keys, threadID)
	}
	// Sort by anonymized ID so raw thread identifiers do not influence a
	// user-visible ordering beyond their stable digest.
	sessions := make([]aisnapshot.Session, 0, len(keys))
	for _, threadID := range keys {
		sessions = append(sessions, collector.sessions[threadID])
	}
	sortSessions(sessions)
	return Update{Provider: collector.provider, Sessions: sessions}
}

func sortSessions(sessions []aisnapshot.Session) {
	for left := 1; left < len(sessions); left++ {
		for right := left; right > 0 && sessions[right].ID < sessions[right-1].ID; right-- {
			sessions[right], sessions[right-1] = sessions[right-1], sessions[right]
		}
	}
}

func (collector *Collector) clearSessionsLocked() {
	clear(collector.ownedThreads)
	clear(collector.pendingLoads)
	clear(collector.sessions)
}
