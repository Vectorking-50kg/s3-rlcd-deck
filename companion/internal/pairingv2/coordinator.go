package pairingv2

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairing"
)

const (
	defaultSessionTTL       = 120 * time.Second
	defaultLinkProofTimeout = 30 * time.Second
	maximumSessions         = 8
	transactionEndpoint     = "pairing/transaction"
)

var (
	ErrSessionNotFound   = errors.New("Pairing v2 session not found")
	ErrSessionState      = errors.New("Pairing v2 session is not confirmable")
	ErrPairingFailed     = errors.New("Pairing v2 transaction failed")
	ErrCoordinatorClosed = errors.New("Pairing v2 coordinator is closed")
)

type SessionState string

const (
	SessionAwaitingCode   SessionState = "awaiting_code"
	SessionAuthenticating SessionState = "authenticating"
	SessionProvingLink    SessionState = "proving_link"
	SessionCommitting     SessionState = "committing"
	SessionPaired         SessionState = "paired"
	SessionFailed         SessionState = "failed"
	SessionCancelled      SessionState = "cancelled"
	SessionExpired        SessionState = "expired"
)

// SessionView is the complete browser-safe Pairing projection. It cannot
// represent a route, interface, Token, certificate, Device Identity, nonce, or
// PAKE value.
type SessionView struct {
	Reference string       `json:"session_ref"`
	State     SessionState `json:"state"`
	ExpiresAt time.Time    `json:"expires_at"`
	ErrorCode string       `json:"error_code"`
}

type HubLocator struct {
	Service string
	Address string
}

type SecureChannel interface {
	Exchange(context.Context, string, []byte) ([]byte, error)
	Close()
}

type ConnectSecure func(context.Context, Route, []byte) (SecureChannel, error)

type CoordinatorConfig struct {
	Discovery        *Discovery
	Trust            *pairing.Service
	Connect          ConnectSecure
	Hub              func(context.Context) (HubLocator, error)
	Clock            Clock
	Random           io.Reader
	SessionTTL       time.Duration
	LinkProofTimeout time.Duration
}

type Coordinator struct {
	discovery        *Discovery
	trust            *pairing.Service
	connect          ConnectSecure
	hub              func(context.Context) (HubLocator, error)
	clock            Clock
	random           io.Reader
	sessionTTL       time.Duration
	linkProofTimeout time.Duration
	operationContext context.Context
	cancelOperations context.CancelFunc

	mutex      sync.Mutex
	sessions   map[string]*sessionRecord
	operations sync.WaitGroup
	closed     bool
	closeDone  chan struct{}
}

type sessionRecord struct {
	view           SessionView
	selection      Selection
	sessionID      string
	transactionID  string
	companionNonce string
	deviceID       string
	proof          chan struct{}
	cancel         context.CancelFunc
}

func NewCoordinator(config CoordinatorConfig) (*Coordinator, error) {
	if config.Discovery == nil || config.Trust == nil || config.Hub == nil {
		return nil, errors.New("Pairing v2 coordinator dependencies are required")
	}
	if config.Clock == nil {
		config.Clock = systemClock{}
	}
	if config.Random == nil {
		config.Random = rand.Reader
	}
	if config.SessionTTL == 0 {
		config.SessionTTL = defaultSessionTTL
	}
	if config.LinkProofTimeout == 0 {
		config.LinkProofTimeout = defaultLinkProofTimeout
	}
	if config.SessionTTL < 30*time.Second || config.SessionTTL > 5*time.Minute ||
		config.LinkProofTimeout < time.Second || config.LinkProofTimeout > config.SessionTTL {
		return nil, errors.New("Pairing v2 coordinator timing is invalid")
	}
	if config.Connect == nil {
		proofClient := NewProofClient(config.Random)
		config.Connect = func(ctx context.Context, route Route, code []byte) (SecureChannel, error) {
			return proofClient.Connect(ctx, route, code)
		}
	}
	operationContext, cancelOperations := context.WithCancel(context.Background())
	return &Coordinator{
		discovery:        config.Discovery,
		trust:            config.Trust,
		connect:          config.Connect,
		hub:              config.Hub,
		clock:            config.Clock,
		random:           config.Random,
		sessionTTL:       config.SessionTTL,
		linkProofTimeout: config.LinkProofTimeout,
		operationContext: operationContext,
		cancelOperations: cancelOperations,
		sessions:         make(map[string]*sessionRecord),
		closeDone:        make(chan struct{}),
	}, nil
}

func (coordinator *Coordinator) Scan(ctx context.Context) ([]Candidate, error) {
	coordinator.mutex.Lock()
	closed := coordinator.closed
	coordinator.mutex.Unlock()
	if closed {
		return nil, ErrCoordinatorClosed
	}
	return coordinator.discovery.Scan(ctx)
}

func (coordinator *Coordinator) Begin(candidateReference string) (SessionView, error) {
	coordinator.mutex.Lock()
	closed := coordinator.closed
	coordinator.mutex.Unlock()
	if closed {
		return SessionView{}, ErrCoordinatorClosed
	}
	selection, err := coordinator.discovery.Resolve(candidateReference)
	if err != nil {
		return SessionView{}, err
	}
	now := coordinator.clock.Now().UTC()
	expiresAt := now.Add(coordinator.sessionTTL)

	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if coordinator.closed {
		return SessionView{}, ErrCoordinatorClosed
	}
	coordinator.pruneLocked(now)
	if len(coordinator.sessions) >= maximumSessions {
		return SessionView{}, errors.New("Pairing v2 session capacity reached")
	}
	reference, err := coordinator.uniqueReferenceLocked()
	if err != nil {
		return SessionView{}, err
	}
	sessionID, err := randomHex(coordinator.random, 16)
	if err != nil {
		return SessionView{}, err
	}
	transactionID, err := distinctRandomHex(coordinator.random, sessionID)
	if err != nil {
		return SessionView{}, err
	}
	windowNonce := hex.EncodeToString(selection.WindowID[:])
	companionNonce, err := distinctRandomHex(
		coordinator.random,
		sessionID,
		transactionID,
		windowNonce,
	)
	if err != nil {
		return SessionView{}, err
	}
	record := &sessionRecord{
		view: SessionView{
			Reference: reference,
			State:     SessionAwaitingCode,
			ExpiresAt: expiresAt,
			ErrorCode: "none",
		},
		selection:      selection,
		sessionID:      sessionID,
		transactionID:  transactionID,
		companionNonce: companionNonce,
		proof:          make(chan struct{}, 1),
	}
	coordinator.sessions[reference] = record
	return record.view, nil
}

func (coordinator *Coordinator) Status(reference string) (SessionView, error) {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	record, exists := coordinator.sessions[reference]
	if !exists {
		return SessionView{}, ErrSessionNotFound
	}
	coordinator.expireLocked(record, coordinator.clock.Now().UTC())
	return record.view, nil
}

func (coordinator *Coordinator) Cancel(reference string) (SessionView, error) {
	coordinator.mutex.Lock()
	record, exists := coordinator.sessions[reference]
	if !exists {
		coordinator.mutex.Unlock()
		return SessionView{}, ErrSessionNotFound
	}
	if record.view.State == SessionPaired || record.view.State == SessionCancelled ||
		record.view.State == SessionExpired {
		view := record.view
		coordinator.mutex.Unlock()
		return view, nil
	}
	record.view.State = SessionCancelled
	record.view.ErrorCode = "cancelled"
	cancel := record.cancel
	view := record.view
	coordinator.mutex.Unlock()
	if cancel != nil {
		cancel()
	}
	coordinator.trust.CancelProvisional(record.transactionID)
	return view, nil
}

// StartConfirm transfers the Pairing transaction to the Coordinator's bounded
// lifecycle and returns immediately. HTTP request cancellation must not abort a
// transaction after the browser has received an accepted response.
func (coordinator *Coordinator) StartConfirm(reference string, code string) (SessionView, error) {
	record, operationContext, cancel, view, err := coordinator.claimConfirm(
		coordinator.operationContext,
		reference,
		code,
	)
	if err != nil {
		return view, err
	}
	go func() {
		defer coordinator.operations.Done()
		_, _ = coordinator.finishConfirm(operationContext, cancel, record, code)
	}()
	return view, nil
}

func (coordinator *Coordinator) Confirm(
	ctx context.Context,
	reference string,
	code string,
) (SessionView, error) {
	record, operationContext, cancel, view, err := coordinator.claimConfirm(ctx, reference, code)
	if err != nil {
		return view, err
	}
	defer coordinator.operations.Done()
	return coordinator.finishConfirm(operationContext, cancel, record, code)
}

func (coordinator *Coordinator) claimConfirm(
	parent context.Context,
	reference string,
	code string,
) (*sessionRecord, context.Context, context.CancelFunc, SessionView, error) {
	if !validSixDigitCode(code) {
		return nil, nil, nil, SessionView{}, ErrPairingFailed
	}
	operationContext, cancel := context.WithCancel(parent)
	coordinator.mutex.Lock()
	if coordinator.closed {
		coordinator.mutex.Unlock()
		cancel()
		return nil, nil, nil, SessionView{}, ErrCoordinatorClosed
	}
	record, exists := coordinator.sessions[reference]
	if !exists {
		coordinator.mutex.Unlock()
		cancel()
		return nil, nil, nil, SessionView{}, ErrSessionNotFound
	}
	coordinator.expireLocked(record, coordinator.clock.Now().UTC())
	if record.view.State != SessionAwaitingCode {
		view := record.view
		coordinator.mutex.Unlock()
		cancel()
		return nil, nil, nil, view, ErrSessionState
	}
	record.view.State = SessionAuthenticating
	record.cancel = cancel
	coordinator.operations.Add(1)
	view := record.view
	coordinator.mutex.Unlock()
	return record, operationContext, cancel, view, nil
}

func (coordinator *Coordinator) finishConfirm(
	operationContext context.Context,
	cancel context.CancelFunc,
	record *sessionRecord,
	code string,
) (SessionView, error) {
	err := coordinator.confirm(operationContext, record, code)
	cancel()
	coordinator.mutex.Lock()
	record.cancel = nil
	if err != nil && record.view.State != SessionCancelled && record.view.State != SessionExpired {
		record.view.State = SessionFailed
		if record.view.ErrorCode == "none" {
			record.view.ErrorCode = "pairing_failed"
		}
	}
	view := record.view
	coordinator.mutex.Unlock()
	if err != nil {
		return view, errors.Join(ErrPairingFailed, err)
	}
	return view, nil
}

// Close cancels all in-flight transactions and waits for their secret-bearing
// channels and provisional Trust entries to be cleared. A timed-out Close can
// be retried with a fresh context.
func (coordinator *Coordinator) Close(ctx context.Context) error {
	coordinator.mutex.Lock()
	if !coordinator.closed {
		coordinator.closed = true
		coordinator.cancelOperations()
		for _, record := range coordinator.sessions {
			if record.cancel != nil {
				record.cancel()
			}
		}
		go func() {
			coordinator.operations.Wait()
			close(coordinator.closeDone)
		}()
	}
	closeDone := coordinator.closeDone
	coordinator.mutex.Unlock()
	select {
	case <-closeDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (coordinator *Coordinator) confirm(
	ctx context.Context,
	record *sessionRecord,
	code string,
) (resultErr error) {
	material, err := coordinator.trust.IssueProvisionalMaterial(ctx)
	if err != nil {
		return err
	}
	locator, err := coordinator.hub(ctx)
	if err != nil {
		return err
	}
	channel, err := coordinator.connectRoutes(ctx, record.selection.Routes, code)
	if err != nil {
		return err
	}
	defer channel.Close()

	credentials := Credentials{
		Type: "pairing.credentials", ProtocolVersion: ContractVersion,
		SessionID: record.sessionID, TransactionID: record.transactionID, Sequence: 1,
		WindowNonce:    hex.EncodeToString(record.selection.WindowID[:]),
		CompanionNonce: record.companionNonce,
		HubService:     locator.Service, HubAddress: locator.Address,
		Token: material.Token, CertificateFingerprint: material.CertificateFingerprint,
		CertificateDER: material.CertificateDER, DeviceLinkProtocol: uint32(material.ProtocolVersion),
	}
	document, err := json.Marshal(credentials)
	if err != nil || len(document) > MaximumContractMessage {
		clearBytes(document)
		return ErrMalformedContract
	}
	response, err := channel.Exchange(ctx, transactionEndpoint, document)
	clearBytes(document)
	if err != nil {
		clearBytes(response)
		return err
	}
	message, err := DecodeContractMessage(response)
	clearBytes(response)
	ready, ok := message.(*CommitReady)
	if err != nil || !ok || !coordinator.readyMatches(record, credentials, ready) {
		return ErrMalformedContract
	}
	transcript, err := TranscriptSHA256(credentials, *ready)
	if err != nil || subtle.ConstantTimeCompare(
		[]byte(transcript), []byte(ready.TranscriptSHA256),
	) != 1 {
		return ErrMalformedContract
	}

	coordinator.mutex.Lock()
	record.deviceID = ready.DeviceID
	record.view.State = SessionProvingLink
	coordinator.mutex.Unlock()
	staged := false
	trustCommitted := false
	defer func() {
		if trustCommitted {
			cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
			cleanupErr := coordinator.trust.Revoke(cleanupContext, ready.DeviceID)
			cancelCleanup()
			if cleanupErr != nil {
				resultErr = errors.Join(resultErr, errors.New("Pairing v2 trust rollback failed"))
			}
		} else if staged {
			if !coordinator.trust.CancelProvisional(record.transactionID) {
				resultErr = errors.Join(resultErr, errors.New("Pairing v2 provisional cleanup failed"))
			}
		}
	}()
	if err = coordinator.trust.StageProvisional(ctx, pairing.ProvisionalTrustRequest{
		SessionID: record.sessionID, TransactionID: record.transactionID,
		DeviceID: ready.DeviceID, DeviceIdentity: ready.DeviceIdentity,
		Token: material.Token, ProtocolVersion: material.ProtocolVersion,
		ExpiresAt: record.view.ExpiresAt,
	}); err != nil {
		return err
	}
	staged = true
	proofTimer := time.NewTimer(coordinator.linkProofTimeout)
	defer proofTimer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-proofTimer.C:
		return errors.New("Pairing v2 Device Link proof timed out")
	case <-record.proof:
	}

	coordinator.mutex.Lock()
	record.view.State = SessionCommitting
	coordinator.mutex.Unlock()
	if err = coordinator.trust.CommitProvisional(ctx, record.transactionID, ready.DeviceID); err != nil {
		return err
	}
	staged = false
	trustCommitted = true
	commit := Commit{
		Type: "pairing.commit", ProtocolVersion: ContractVersion,
		SessionID: record.sessionID, TransactionID: record.transactionID,
		Sequence: 3, DeckNonce: ready.DeckNonce, TranscriptSHA256: transcript,
	}
	document, err = json.Marshal(commit)
	if err != nil {
		return err
	}
	response, err = channel.Exchange(ctx, transactionEndpoint, document)
	clearBytes(document)
	if err != nil {
		clearBytes(response)
		return err
	}
	message, err = DecodeContractMessage(response)
	clearBytes(response)
	receipt, ok := message.(*CommitReceipt)
	if err != nil || !ok || receipt.SessionID != record.sessionID ||
		receipt.TransactionID != record.transactionID || receipt.ProfileID != ready.ProfileID ||
		receipt.TranscriptSHA256 != transcript || receipt.ProfileGeneration == 0 {
		return ErrMalformedContract
	}
	trustCommitted = false
	coordinator.mutex.Lock()
	record.view.State = SessionPaired
	record.view.ErrorCode = "none"
	coordinator.mutex.Unlock()
	return nil
}

func (coordinator *Coordinator) ObserveProvisionalHeartbeat(deviceID, transactionID string) error {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	for _, record := range coordinator.sessions {
		if record.transactionID != transactionID || record.deviceID != deviceID ||
			record.view.State != SessionProvingLink {
			continue
		}
		select {
		case record.proof <- struct{}{}:
		default:
		}
		return nil
	}
	return ErrSessionNotFound
}

func (coordinator *Coordinator) readyMatches(
	record *sessionRecord,
	credentials Credentials,
	ready *CommitReady,
) bool {
	return ready != nil && ready.SessionID == record.sessionID &&
		ready.TransactionID == record.transactionID && ready.WindowNonce == credentials.WindowNonce &&
		ready.CompanionNonce == credentials.CompanionNonce &&
		ready.ProfileID == credentials.CertificateFingerprint
}

func (coordinator *Coordinator) connectRoutes(
	ctx context.Context,
	routes []Route,
	code string,
) (SecureChannel, error) {
	var attempts []error
	for _, route := range routes {
		codeBytes := []byte(code)
		channel, err := coordinator.connect(ctx, route, codeBytes)
		clearBytes(codeBytes)
		if err == nil {
			return channel, nil
		}
		attempts = append(attempts, err)
		if ctx.Err() != nil {
			break
		}
	}
	if len(attempts) == 0 {
		return nil, ErrNoUsableInterface
	}
	return nil, errors.Join(attempts...)
}

func (coordinator *Coordinator) expireLocked(record *sessionRecord, now time.Time) {
	if now.Before(record.view.ExpiresAt) || record.view.State == SessionPaired ||
		record.view.State == SessionCancelled || record.view.State == SessionFailed {
		return
	}
	record.view.State = SessionExpired
	record.view.ErrorCode = "expired"
	if record.cancel != nil {
		record.cancel()
	}
}

func (coordinator *Coordinator) pruneLocked(now time.Time) {
	for reference, record := range coordinator.sessions {
		coordinator.expireLocked(record, now)
		if !now.Before(record.view.ExpiresAt.Add(coordinator.sessionTTL)) {
			delete(coordinator.sessions, reference)
		}
	}
}

func (coordinator *Coordinator) uniqueReferenceLocked() (string, error) {
	for attempt := 0; attempt < maxReferenceAttempts; attempt++ {
		value := make([]byte, candidateRefBytes)
		if _, err := io.ReadFull(coordinator.random, value); err != nil {
			clearBytes(value)
			return "", err
		}
		reference := base64.RawURLEncoding.EncodeToString(value)
		clearBytes(value)
		if _, exists := coordinator.sessions[reference]; !exists {
			return reference, nil
		}
	}
	return "", errors.New("generate unique Pairing v2 session reference")
}

func randomHex(random io.Reader, size int) (string, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(random, value); err != nil {
		clearBytes(value)
		return "", err
	}
	encoded := hex.EncodeToString(value)
	clearBytes(value)
	return encoded, nil
}

func distinctRandomHex(random io.Reader, excluded ...string) (string, error) {
	for attempt := 0; attempt < maxReferenceAttempts; attempt++ {
		value, err := randomHex(random, 16)
		if err != nil {
			return "", err
		}
		distinct := true
		for _, existing := range excluded {
			if value == existing {
				distinct = false
				break
			}
		}
		if distinct {
			return value, nil
		}
	}
	return "", errors.New("generate distinct Pairing v2 identifier")
}

func validSixDigitCode(value string) bool {
	if len(value) != 6 {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}
