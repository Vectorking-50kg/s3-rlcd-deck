package pairing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
)

const (
	fileStoreSchemaVersion = 1
	maxFileStoreBytes      = 1 << 20
	maxActiveCodes         = 64
	maxStoredTrusts        = 1024
	maxStoredAuditEvents   = 2048
)

var verifierPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var auditDeviceRefPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)
var auditWordPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type fileStoreState struct {
	SchemaVersion int                    `json:"schema_version"`
	Codes         map[string]StoredCode  `json:"codes"`
	Trusts        map[string]StoredTrust `json:"trusts"`
	Audit         []AuditEvent           `json:"audit"`
}

type FileStore struct {
	mu    sync.RWMutex
	path  string
	state fileStoreState
	lock  *protectedfile.Lock
}

func OpenFileStore(path string) (*FileStore, error) {
	if path == "" {
		return nil, errors.New("pairing store path is required")
	}
	path = filepath.Clean(path)
	parent := filepath.Dir(path)
	lock, err := protectedfile.AcquireDirectoryLock(parent, ".pairing.lock")
	if err != nil {
		return nil, err
	}
	closeLock := true
	defer func() {
		if closeLock {
			_ = lock.Close()
		}
	}()
	state := emptyFileStoreState()
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("inspect pairing store: %w", err)
	case info.Mode()&os.ModeSymlink != 0:
		return nil, errors.New("pairing store must not be a symbolic link")
	case !info.Mode().IsRegular():
		return nil, errors.New("pairing store must be a regular file")
	default:
		if err = protectedfile.EnsurePrivateFile(path); err != nil {
			return nil, err
		}
		state, err = readFileStore(path)
		if err != nil {
			return nil, err
		}
	}
	closeLock = false
	return &FileStore{path: path, state: state, lock: lock}, nil
}

func (store *FileStore) Close() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.lock == nil {
		return nil
	}
	err := store.lock.Close()
	store.lock = nil
	return err
}

func (store *FileStore) SaveCode(ctx context.Context, code StoredCode) error {
	return store.update(ctx, func(state *fileStoreState) error {
		if err := saveFileCode(state, code); err != nil {
			return err
		}
		appendFileAudit(state, "pairing_code_issued", "success", "", code.IssuedAt)
		return nil
	})
}

func (store *FileStore) SaveRotationCode(ctx context.Context, code StoredCode) error {
	return store.update(ctx, func(state *fileStoreState) error {
		if code.DeviceID == "" {
			return ErrInvalidRequest
		}
		if _, found := state.Trusts[code.DeviceID]; !found {
			return ErrTrustNotFound
		}
		if err := saveFileCode(state, code); err != nil {
			return err
		}
		appendFileAudit(state, "device_token_rotation_code_issued", "success", code.DeviceID, code.IssuedAt)
		return nil
	})
}

func saveFileCode(state *fileStoreState, code StoredCode) error {
	for key, existing := range state.Codes {
		if !code.IssuedAt.Before(existing.ExpiresAt) {
			delete(state.Codes, key)
		}
	}
	if _, found := state.Codes[code.Verifier]; found {
		return ErrCodeConflict
	}
	if len(state.Codes) >= maxActiveCodes {
		return errors.New("active pairing code capacity reached")
	}
	state.Codes[code.Verifier] = code
	return nil
}

func (store *FileStore) ConsumeCode(
	ctx context.Context,
	codeVerifier string,
	now time.Time,
	trust StoredTrust,
) error {
	return store.update(ctx, func(state *fileStoreState) error {
		code, found := state.Codes[codeVerifier]
		if !found || !now.Before(code.ExpiresAt) {
			if found {
				delete(state.Codes, codeVerifier)
			}
			return ErrCodeUnavailable
		}
		if _, exists := state.Trusts[trust.DeviceID]; !exists && len(state.Trusts) >= maxStoredTrusts {
			return errors.New("device trust capacity reached")
		}
		if code.DeviceID != "" {
			existing, exists := state.Trusts[code.DeviceID]
			if !exists || trust.DeviceID != code.DeviceID ||
				trust.DeviceIdentityVerifier != existing.DeviceIdentityVerifier ||
				trust.ProtocolVersion != existing.ProtocolVersion {
				return ErrCodeUnavailable
			}
			trust.CreatedAt = existing.CreatedAt
			trust.RotatedAt = now
		}
		delete(state.Codes, codeVerifier)
		state.Trusts[trust.DeviceID] = trust
		appendFileAudit(state, "pairing_redeem", "success", trust.DeviceID, now)
		return nil
	})
}

func (store *FileStore) LookupTrust(ctx context.Context, deviceID string) (StoredTrust, error) {
	if err := ctx.Err(); err != nil {
		return StoredTrust{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	trust, found := store.state.Trusts[deviceID]
	if !found {
		return StoredTrust{}, ErrTrustNotFound
	}
	return trust, nil
}

func (store *FileStore) RevokeTrust(ctx context.Context, deviceID string, now time.Time) error {
	return store.update(ctx, func(state *fileStoreState) error {
		if _, found := state.Trusts[deviceID]; !found {
			return ErrTrustNotFound
		}
		delete(state.Trusts, deviceID)
		appendFileAudit(state, "device_revoked", "success", deviceID, now)
		return nil
	})
}

func (store *FileStore) update(
	ctx context.Context,
	mutate func(*fileStoreState) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.lock == nil {
		return errors.New("pairing store is closed")
	}
	next := cloneFileStoreState(store.state)
	if err := mutate(&next); err != nil {
		return err
	}
	if err := validateFileStoreState(next); err != nil {
		return fmt.Errorf("invalid pairing state update: %w", err)
	}
	committed, err := writeFileStore(store.path, next)
	if committed {
		store.state = next
	}
	if err != nil {
		return err
	}
	if !committed {
		return errors.New("pairing store update was not committed")
	}
	return nil
}

func emptyFileStoreState() fileStoreState {
	return fileStoreState{
		SchemaVersion: fileStoreSchemaVersion,
		Codes:         make(map[string]StoredCode),
		Trusts:        make(map[string]StoredTrust),
		Audit:         make([]AuditEvent, 0),
	}
}

func cloneFileStoreState(state fileStoreState) fileStoreState {
	clone := emptyFileStoreState()
	for key, code := range state.Codes {
		clone.Codes[key] = code
	}
	for key, trust := range state.Trusts {
		clone.Trusts[key] = trust
	}
	clone.Audit = append(clone.Audit, state.Audit...)
	return clone
}

func readFileStore(path string) (fileStoreState, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileStoreState{}, fmt.Errorf("inspect pairing store size: %w", err)
	}
	if info.Size() > maxFileStoreBytes {
		return fileStoreState{}, errors.New("pairing store is too large")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return fileStoreState{}, fmt.Errorf("read pairing store: %w", err)
	}
	var state fileStoreState
	if err = protocol.DecodeStrictDocumentLimit(contents, maxFileStoreBytes, &state); err != nil {
		return fileStoreState{}, fmt.Errorf("decode pairing store: %w", err)
	}
	if err = validateFileStoreState(state); err != nil {
		return fileStoreState{}, fmt.Errorf("validate pairing store: %w", err)
	}
	return state, nil
}

func validateFileStoreState(state fileStoreState) error {
	if state.SchemaVersion != fileStoreSchemaVersion || state.Codes == nil ||
		state.Trusts == nil || state.Audit == nil {
		return errors.New("unsupported or incomplete pairing store schema")
	}
	if len(state.Codes) > maxActiveCodes || len(state.Trusts) > maxStoredTrusts {
		return errors.New("pairing store exceeds capacity")
	}
	if len(state.Audit) > maxStoredAuditEvents {
		return errors.New("pairing audit exceeds capacity")
	}
	for key, code := range state.Codes {
		if key != code.Verifier || !verifierPattern.MatchString(key) || code.IssuedAt.IsZero() ||
			!code.ExpiresAt.After(code.IssuedAt) ||
			(code.DeviceID != "" && !deviceIDPattern.MatchString(code.DeviceID)) {
			return errors.New("invalid stored pairing code")
		}
	}
	for key, trust := range state.Trusts {
		if key != trust.DeviceID || !deviceIDPattern.MatchString(key) ||
			!verifierPattern.MatchString(trust.DeviceIdentityVerifier) ||
			!verifierPattern.MatchString(trust.TokenVerifier) ||
			trust.ProtocolVersion != ProtocolVersion || trust.CreatedAt.IsZero() {
			return errors.New("invalid stored device trust")
		}
	}
	for _, event := range state.Audit {
		if !auditWordPattern.MatchString(event.Action) || !auditWordPattern.MatchString(event.Outcome) ||
			event.OccurredAt.IsZero() ||
			(event.DeviceRef != "" && !auditDeviceRefPattern.MatchString(event.DeviceRef)) {
			return errors.New("invalid pairing audit event")
		}
	}
	return nil
}

func appendFileAudit(
	state *fileStoreState,
	action string,
	outcome string,
	deviceID string,
	occurredAt time.Time,
) {
	deviceRef := ""
	if deviceID != "" {
		deviceRef = verifier(deviceID)[:16]
	}
	state.Audit = append(state.Audit, AuditEvent{
		Action:     action,
		Outcome:    outcome,
		DeviceRef:  deviceRef,
		OccurredAt: occurredAt.UTC(),
	})
	if len(state.Audit) > maxStoredAuditEvents {
		state.Audit = append([]AuditEvent(nil), state.Audit[len(state.Audit)-maxStoredAuditEvents:]...)
	}
}

func writeFileStore(path string, state fileStoreState) (bool, error) {
	contents, err := json.Marshal(state)
	if err != nil {
		return false, fmt.Errorf("encode pairing store: %w", err)
	}
	return protectedfile.Replace(path, contents)
}
