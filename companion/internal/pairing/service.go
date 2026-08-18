package pairing

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
)

const (
	ProtocolVersion       = int(protocol.CurrentVersion)
	defaultCodeTTL        = 5 * time.Minute
	pairingCodeSpace      = 1_000_000
	deviceTokenBytes      = 32
	maxCertificateDER     = 1024
	maxCodeCollisions     = 8
	maxProvisionalTrusts  = 16
	maximumProvisionalTTL = 5 * time.Minute
)

var (
	ErrCodeUnavailable     = errors.New("pairing code is unavailable")
	ErrUnsupportedProtocol = errors.New("unsupported protocol version")
	ErrInvalidRequest      = errors.New("invalid pairing request")
	ErrTrustNotFound       = errors.New("device trust not found")
	ErrCodeConflict        = errors.New("pairing code already exists")
	ErrProvisionalConflict = errors.New("provisional trust conflicts with an active transaction")
	ErrProvisionalNotFound = errors.New("provisional trust not found")
	ErrProvisionalCapacity = errors.New("provisional trust capacity reached")
)

var (
	deviceIDPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{7,63}$`)
	fingerprintPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	pairingCodePattern   = regexp.MustCompile(`^[0-9]{6}$`)
	transactionIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	deviceTokenPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

type Clock interface {
	Now() time.Time
}

type Random interface {
	Number(limit int) (int, error)
	Bytes(count int) ([]byte, error)
}

type Store interface {
	SaveCode(context.Context, StoredCode) error
	SaveRotationCode(context.Context, StoredCode) error
	ConsumeCode(context.Context, string, time.Time, StoredTrust) error
	LookupTrust(context.Context, string) (StoredTrust, error)
	ListTrusts(context.Context) ([]StoredTrust, error)
	RevokeTrust(context.Context, string, time.Time) error
	CommitTrust(context.Context, StoredTrust) error
}

func sortedTrusts(source map[string]StoredTrust) []StoredTrust {
	trusts := make([]StoredTrust, 0, len(source))
	for _, trust := range source {
		trusts = append(trusts, trust)
	}
	sort.Slice(trusts, func(left, right int) bool {
		return trusts[left].DeviceID < trusts[right].DeviceID
	})
	return trusts
}

type Auditor interface {
	Record(AuditEvent)
}

type AuditEvent struct {
	Action     string    `json:"action"`
	Outcome    string    `json:"outcome"`
	DeviceRef  string    `json:"device_ref,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}

type Config struct {
	Clock                  Clock
	Store                  Store
	Random                 Random
	Auditor                Auditor
	CodeTTL                time.Duration
	CertificateFingerprint string
	CertificateDER         []byte
	CodePepper             []byte
}

type Service struct {
	clock          Clock
	store          Store
	random         Random
	auditor        Auditor
	codeTTL        time.Duration
	fingerprint    string
	certificateDER string
	codePepper     []byte
	provisionalMu  sync.Mutex
	provisional    map[string]provisionalTrust
}

type provisionalTrust struct {
	sessionID     string
	transactionID string
	trust         StoredTrust
	expiresAt     time.Time
}

// ProvisionalTrustRequest contains only one Pairing Session's authenticated
// result. The raw Token and Device Identity are reduced to verifiers before
// StageProvisional returns and are never written to the normal trust Store.
type ProvisionalTrustRequest struct {
	SessionID       string
	TransactionID   string
	DeviceID        string
	DeviceIdentity  string
	Token           string
	ProtocolVersion int
	ExpiresAt       time.Time
}

type IssuedCode struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
}

type RedeemRequest struct {
	Code            string `json:"code"`
	DeviceID        string `json:"device_id"`
	DeviceIdentity  string `json:"device_identity"`
	ProtocolVersion int    `json:"protocol_version"`
}

type Credential struct {
	DeviceID               string `json:"device_id"`
	Token                  string `json:"token"`
	CertificateFingerprint string `json:"certificate_fingerprint"`
	CertificateDER         string `json:"certificate_der"`
	ProtocolVersion        int    `json:"protocol_version"`
}

type StoredCode struct {
	Verifier  string    `json:"verifier"`
	DeviceID  string    `json:"device_id,omitempty"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type StoredTrust struct {
	DeviceID               string    `json:"device_id"`
	DeviceIdentityVerifier string    `json:"device_identity_verifier"`
	TokenVerifier          string    `json:"token_verifier"`
	ProtocolVersion        int       `json:"protocol_version"`
	CreatedAt              time.Time `json:"created_at"`
	RotatedAt              time.Time `json:"rotated_at,omitempty"`
}

// TrustView is the complete management-safe view of paired Deck trust. Secret
// verifiers deliberately have no representation in this type.
type TrustView struct {
	DeviceID        string    `json:"device_id"`
	ProtocolVersion int       `json:"protocol_version"`
	CreatedAt       time.Time `json:"created_at"`
	RotatedAt       time.Time `json:"rotated_at,omitempty"`
}

type Authentication struct {
	DeviceID        string
	Token           string
	DeviceIdentity  string
	ProtocolVersion int
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

type cryptoRandom struct{}

func (cryptoRandom) Number(limit int) (int, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(int64(limit)))
	if err != nil {
		return 0, err
	}
	return int(value.Int64()), nil
}

func (cryptoRandom) Bytes(count int) ([]byte, error) {
	value := make([]byte, count)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return nil, err
	}
	return value, nil
}

func New(config Config) (*Service, error) {
	if config.Store == nil {
		return nil, errors.New("pairing store is required")
	}
	if config.Clock == nil {
		config.Clock = systemClock{}
	}
	if config.Random == nil {
		config.Random = cryptoRandom{}
	}
	if len(config.CodePepper) == 0 {
		var err error
		config.CodePepper, err = config.Random.Bytes(deviceTokenBytes)
		if err != nil {
			return nil, fmt.Errorf("generate ephemeral pairing-code pepper: %w", err)
		}
	}
	if len(config.CodePepper) != deviceTokenBytes {
		return nil, errors.New("pairing-code pepper must contain exactly 32 bytes")
	}
	if config.CodeTTL == 0 {
		config.CodeTTL = defaultCodeTTL
	}
	if config.CodeTTL < time.Minute || config.CodeTTL > 15*time.Minute {
		return nil, errors.New("pairing code TTL must be between one and fifteen minutes")
	}
	if !fingerprintPattern.MatchString(config.CertificateFingerprint) {
		return nil, errors.New("certificate fingerprint must be canonical sha256 hex")
	}
	if len(config.CertificateDER) == 0 || len(config.CertificateDER) > maxCertificateDER {
		return nil, errors.New("Device Hub certificate DER is required")
	}
	certificateDigest := sha256.Sum256(config.CertificateDER)
	if "sha256:"+hex.EncodeToString(certificateDigest[:]) != config.CertificateFingerprint {
		return nil, errors.New("Device Hub certificate does not match its fingerprint")
	}
	return &Service{
		clock:          config.Clock,
		store:          config.Store,
		random:         config.Random,
		auditor:        config.Auditor,
		codeTTL:        config.CodeTTL,
		fingerprint:    config.CertificateFingerprint,
		certificateDER: base64.StdEncoding.EncodeToString(config.CertificateDER),
		codePepper:     append([]byte(nil), config.CodePepper...),
		provisional:    make(map[string]provisionalTrust),
	}, nil
}

// StageProvisional installs verifier-only trust for the dedicated Pairing v2
// Device Link probe. Verify deliberately does not consult this map, so staged
// trust grants no normal Device Hub capability.
func (service *Service) StageProvisional(ctx context.Context, request ProvisionalTrustRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now := service.clock.Now().UTC()
	identity, err := validateProvisionalRequest(request, now)
	if err != nil {
		return err
	}
	staged := provisionalTrust{
		sessionID: request.SessionID, transactionID: request.TransactionID,
		trust: StoredTrust{
			DeviceID:               request.DeviceID,
			DeviceIdentityVerifier: verifierBytes(identity),
			TokenVerifier:          verifier(request.Token),
			ProtocolVersion:        request.ProtocolVersion,
			CreatedAt:              now,
		},
		expiresAt: request.ExpiresAt.UTC(),
	}
	clear(identity)

	service.provisionalMu.Lock()
	defer service.provisionalMu.Unlock()
	service.pruneProvisionalLocked(now)
	if existing, found := service.provisional[request.TransactionID]; found {
		if provisionalEqual(existing, staged) {
			return nil
		}
		return ErrProvisionalConflict
	}
	for _, existing := range service.provisional {
		if existing.trust.DeviceID == request.DeviceID || existing.sessionID == request.SessionID {
			return ErrProvisionalConflict
		}
	}
	if len(service.provisional) >= maxProvisionalTrusts {
		return ErrProvisionalCapacity
	}
	service.provisional[request.TransactionID] = staged
	return nil
}

// VerifyProvisional is the only verifier for the restricted first-link probe.
// It returns the exact transaction capability that authenticated the Deck.
func (service *Service) VerifyProvisional(
	ctx context.Context,
	authentication Authentication,
) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if !deviceIDPattern.MatchString(authentication.DeviceID) ||
		!deviceTokenPattern.MatchString(authentication.Token) ||
		authentication.ProtocolVersion != ProtocolVersion {
		return "", false, nil
	}
	identity, err := base64.RawURLEncoding.DecodeString(authentication.DeviceIdentity)
	if err != nil || len(identity) < 16 || len(identity) > 512 {
		clear(identity)
		return "", false, nil
	}
	actualToken := sha256.Sum256([]byte(authentication.Token))
	actualIdentity := sha256.Sum256(identity)
	clear(identity)

	now := service.clock.Now().UTC()
	service.provisionalMu.Lock()
	defer service.provisionalMu.Unlock()
	service.pruneProvisionalLocked(now)
	for transactionID, staged := range service.provisional {
		if staged.trust.DeviceID != authentication.DeviceID {
			continue
		}
		expectedToken, tokenErr := decodeVerifier(staged.trust.TokenVerifier)
		expectedIdentity, identityErr := decodeVerifier(staged.trust.DeviceIdentityVerifier)
		if tokenErr != nil || identityErr != nil {
			clear(expectedToken)
			clear(expectedIdentity)
			return "", false, errors.New("provisional trust verifier is invalid")
		}
		matches := subtle.ConstantTimeCompare(expectedToken, actualToken[:]) &
			subtle.ConstantTimeCompare(expectedIdentity, actualIdentity[:]) &
			subtle.ConstantTimeEq(int32(staged.trust.ProtocolVersion), int32(authentication.ProtocolVersion))
		clear(expectedToken)
		clear(expectedIdentity)
		return transactionID, matches == 1, nil
	}
	return "", false, nil
}

// CommitProvisional atomically publishes one exact staged verifier set to the
// durable normal Trust Store. A storage failure keeps the provisional entry so
// the bounded coordinator can retry or cancel it.
func (service *Service) CommitProvisional(
	ctx context.Context,
	transactionID string,
	deviceID string,
) error {
	if !transactionIDPattern.MatchString(transactionID) || !deviceIDPattern.MatchString(deviceID) {
		return ErrInvalidRequest
	}
	now := service.clock.Now().UTC()
	service.provisionalMu.Lock()
	defer service.provisionalMu.Unlock()
	service.pruneProvisionalLocked(now)
	staged, found := service.provisional[transactionID]
	if !found || staged.trust.DeviceID != deviceID {
		return ErrProvisionalNotFound
	}
	if err := service.store.CommitTrust(ctx, staged.trust); err != nil {
		return fmt.Errorf("commit provisional device trust: %w", err)
	}
	delete(service.provisional, transactionID)
	service.audit("pairing_v2_commit", "success", deviceID, now)
	return nil
}

func (service *Service) CancelProvisional(transactionID string) bool {
	if !transactionIDPattern.MatchString(transactionID) {
		return false
	}
	service.provisionalMu.Lock()
	defer service.provisionalMu.Unlock()
	if _, found := service.provisional[transactionID]; !found {
		return false
	}
	delete(service.provisional, transactionID)
	return true
}

func (service *Service) pruneProvisionalLocked(now time.Time) {
	for transactionID, staged := range service.provisional {
		if !now.Before(staged.expiresAt) {
			delete(service.provisional, transactionID)
		}
	}
}

func validateProvisionalRequest(request ProvisionalTrustRequest, now time.Time) ([]byte, error) {
	if !transactionIDPattern.MatchString(request.SessionID) ||
		!transactionIDPattern.MatchString(request.TransactionID) ||
		request.SessionID == request.TransactionID ||
		!deviceIDPattern.MatchString(request.DeviceID) ||
		!deviceTokenPattern.MatchString(request.Token) ||
		request.ProtocolVersion != ProtocolVersion {
		return nil, ErrInvalidRequest
	}
	identity, err := base64.RawURLEncoding.DecodeString(request.DeviceIdentity)
	if err != nil || len(identity) < 16 || len(identity) > 512 {
		clear(identity)
		return nil, ErrInvalidRequest
	}
	expiresAt := request.ExpiresAt.UTC()
	if !expiresAt.After(now) || expiresAt.After(now.Add(maximumProvisionalTTL)) {
		clear(identity)
		return nil, ErrInvalidRequest
	}
	return identity, nil
}

func provisionalEqual(left, right provisionalTrust) bool {
	return left.sessionID == right.sessionID && left.transactionID == right.transactionID &&
		left.trust == right.trust && left.expiresAt.Equal(right.expiresAt)
}

func (service *Service) Issue(ctx context.Context) (IssuedCode, error) {
	return service.issueCode(ctx, "pairing_code_issued", func(code StoredCode) error {
		return service.store.SaveCode(ctx, code)
	})
}

func (service *Service) IssueRotation(ctx context.Context, deviceID string) (IssuedCode, error) {
	if !deviceIDPattern.MatchString(deviceID) {
		return IssuedCode{}, ErrInvalidRequest
	}
	return service.issueCode(ctx, "device_token_rotation_code_issued", func(code StoredCode) error {
		code.DeviceID = deviceID
		return service.store.SaveRotationCode(ctx, code)
	})
}

func (service *Service) issueCode(
	ctx context.Context,
	auditAction string,
	save func(StoredCode) error,
) (IssuedCode, error) {
	for range maxCodeCollisions {
		number, err := service.random.Number(pairingCodeSpace)
		if err != nil || number < 0 || number >= pairingCodeSpace {
			return IssuedCode{}, fmt.Errorf("generate pairing code: %w", errors.Join(err, ErrInvalidRequest))
		}
		code := fmt.Sprintf("%06d", number)
		now := service.clock.Now().UTC()
		issued := IssuedCode{Code: code, ExpiresAt: now.Add(service.codeTTL)}
		err = save(StoredCode{
			Verifier:  service.codeVerifier(code),
			IssuedAt:  now,
			ExpiresAt: issued.ExpiresAt,
		})
		if err == nil {
			service.audit(auditAction, "success", "", now)
			return issued, nil
		}
		if !errors.Is(err, ErrCodeConflict) {
			return IssuedCode{}, fmt.Errorf("save pairing code: %w", err)
		}
	}
	return IssuedCode{}, errors.New("generate unique pairing code: collision budget exhausted")
}

func (service *Service) Redeem(ctx context.Context, request RedeemRequest) (Credential, error) {
	identityVerifier, err := validateRedeemRequest(request)
	if err != nil {
		service.audit("pairing_redeem", "rejected", request.DeviceID, service.clock.Now())
		return Credential{}, err
	}
	tokenBytes, err := service.random.Bytes(deviceTokenBytes)
	if err != nil || len(tokenBytes) != deviceTokenBytes {
		return Credential{}, errors.New("generate device token")
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	now := service.clock.Now().UTC()
	trust := StoredTrust{
		DeviceID:               request.DeviceID,
		DeviceIdentityVerifier: identityVerifier,
		TokenVerifier:          verifier(token),
		ProtocolVersion:        request.ProtocolVersion,
		CreatedAt:              now,
	}
	if err = service.store.ConsumeCode(ctx, service.codeVerifier(request.Code), now, trust); err != nil {
		service.audit("pairing_redeem", "rejected", request.DeviceID, now)
		if errors.Is(err, ErrCodeUnavailable) {
			return Credential{}, ErrCodeUnavailable
		}
		return Credential{}, fmt.Errorf("persist device trust: %w", err)
	}
	service.audit("pairing_redeem", "success", request.DeviceID, now)
	return Credential{
		DeviceID:               request.DeviceID,
		Token:                  token,
		CertificateFingerprint: service.fingerprint,
		CertificateDER:         service.certificateDER,
		ProtocolVersion:        ProtocolVersion,
	}, nil
}

func (service *Service) Verify(ctx context.Context, authentication Authentication) (bool, error) {
	if !deviceIDPattern.MatchString(authentication.DeviceID) || authentication.Token == "" ||
		authentication.ProtocolVersion != ProtocolVersion {
		return false, nil
	}
	identity, err := base64.RawURLEncoding.DecodeString(authentication.DeviceIdentity)
	if err != nil || len(identity) < 16 || len(identity) > 512 {
		return false, nil
	}
	trust, err := service.store.LookupTrust(ctx, authentication.DeviceID)
	if errors.Is(err, ErrTrustNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lookup device trust: %w", err)
	}
	expectedToken, err := decodeVerifier(trust.TokenVerifier)
	if err != nil {
		return false, errors.New("stored token verifier is invalid")
	}
	expectedIdentity, err := decodeVerifier(trust.DeviceIdentityVerifier)
	if err != nil {
		return false, errors.New("stored device identity verifier is invalid")
	}
	actualToken := sha256.Sum256([]byte(authentication.Token))
	actualIdentity := sha256.Sum256(identity)
	tokenMatches := subtle.ConstantTimeCompare(expectedToken, actualToken[:])
	identityMatches := subtle.ConstantTimeCompare(expectedIdentity, actualIdentity[:])
	protocolMatches := subtle.ConstantTimeEq(int32(trust.ProtocolVersion), int32(authentication.ProtocolVersion))
	return tokenMatches&identityMatches&protocolMatches == 1, nil
}

func (service *Service) ListTrusts(ctx context.Context) ([]TrustView, error) {
	trusts, err := service.store.ListTrusts(ctx)
	if err != nil {
		return nil, fmt.Errorf("list device trust: %w", err)
	}
	views := make([]TrustView, 0, len(trusts))
	for _, trust := range trusts {
		views = append(views, TrustView{
			DeviceID:        trust.DeviceID,
			ProtocolVersion: trust.ProtocolVersion,
			CreatedAt:       trust.CreatedAt.UTC(),
			RotatedAt:       trust.RotatedAt.UTC(),
		})
	}
	return views, nil
}

func (service *Service) Revoke(ctx context.Context, deviceID string) error {
	if !deviceIDPattern.MatchString(deviceID) {
		return ErrInvalidRequest
	}
	now := service.clock.Now().UTC()
	if err := service.store.RevokeTrust(ctx, deviceID, now); err != nil {
		service.audit("device_revoked", "rejected", deviceID, now)
		return fmt.Errorf("revoke device trust: %w", err)
	}
	service.audit("device_revoked", "success", deviceID, now)
	return nil
}

func validateRedeemRequest(request RedeemRequest) (string, error) {
	if request.ProtocolVersion != ProtocolVersion {
		return "", ErrUnsupportedProtocol
	}
	if !pairingCodePattern.MatchString(request.Code) || !deviceIDPattern.MatchString(request.DeviceID) {
		return "", ErrInvalidRequest
	}
	identity, err := base64.RawURLEncoding.DecodeString(request.DeviceIdentity)
	if err != nil || len(identity) < 16 || len(identity) > 512 {
		return "", ErrInvalidRequest
	}
	return verifierBytes(identity), nil
}

func verifier(secret string) string { return verifierBytes([]byte(secret)) }

func (service *Service) codeVerifier(code string) string {
	mac := hmac.New(sha256.New, service.codePepper)
	_, _ = mac.Write([]byte(code))
	return hex.EncodeToString(mac.Sum(nil))
}

func verifierBytes(secret []byte) string {
	digest := sha256.Sum256(secret)
	return hex.EncodeToString(digest[:])
}

func decodeVerifier(encoded string) ([]byte, error) {
	decoded, err := hex.DecodeString(encoded)
	if err != nil || len(decoded) != sha256.Size {
		return nil, errors.New("invalid verifier")
	}
	return decoded, nil
}

func (service *Service) audit(action string, outcome string, deviceID string, occurredAt time.Time) {
	if service.auditor == nil {
		return
	}
	deviceRef := ""
	if deviceID != "" {
		deviceRef = verifier(deviceID)[:16]
	}
	service.auditor.Record(AuditEvent{
		Action:     action,
		Outcome:    outcome,
		DeviceRef:  deviceRef,
		OccurredAt: occurredAt.UTC(),
	})
}
