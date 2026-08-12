package pairing

import (
	"context"
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
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
)

const (
	ProtocolVersion   = int(protocol.CurrentVersion)
	defaultCodeTTL    = 5 * time.Minute
	pairingCodeSpace  = 1_000_000
	deviceTokenBytes  = 32
	maxCodeCollisions = 8
)

var (
	ErrCodeUnavailable     = errors.New("pairing code is unavailable")
	ErrUnsupportedProtocol = errors.New("unsupported protocol version")
	ErrInvalidRequest      = errors.New("invalid pairing request")
	ErrTrustNotFound       = errors.New("device trust not found")
	ErrCodeConflict        = errors.New("pairing code already exists")
)

var (
	deviceIDPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{7,63}$`)
	fingerprintPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	pairingCodePattern = regexp.MustCompile(`^[0-9]{6}$`)
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
	RevokeTrust(context.Context, string, time.Time) error
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
}

type Service struct {
	clock       Clock
	store       Store
	random      Random
	auditor     Auditor
	codeTTL     time.Duration
	fingerprint string
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
	if config.CodeTTL == 0 {
		config.CodeTTL = defaultCodeTTL
	}
	if config.CodeTTL < time.Minute || config.CodeTTL > 15*time.Minute {
		return nil, errors.New("pairing code TTL must be between one and fifteen minutes")
	}
	if !fingerprintPattern.MatchString(config.CertificateFingerprint) {
		return nil, errors.New("certificate fingerprint must be canonical sha256 hex")
	}
	return &Service{
		clock:       config.Clock,
		store:       config.Store,
		random:      config.Random,
		auditor:     config.Auditor,
		codeTTL:     config.CodeTTL,
		fingerprint: config.CertificateFingerprint,
	}, nil
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
			Verifier:  verifier(code),
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
	if err = service.store.ConsumeCode(ctx, verifier(request.Code), now, trust); err != nil {
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
