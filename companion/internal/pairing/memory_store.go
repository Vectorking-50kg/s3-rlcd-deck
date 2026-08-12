package pairing

import (
	"context"
	"sync"
	"time"
)

type MemoryStore struct {
	mu     sync.RWMutex
	codes  map[string]StoredCode
	trusts map[string]StoredTrust
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		codes:  make(map[string]StoredCode),
		trusts: make(map[string]StoredTrust),
	}
}

func (store *MemoryStore) SaveCode(_ context.Context, code StoredCode) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.saveCodeLocked(code)
}

func (store *MemoryStore) SaveRotationCode(_ context.Context, code StoredCode) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if code.DeviceID == "" {
		return ErrInvalidRequest
	}
	if _, found := store.trusts[code.DeviceID]; !found {
		return ErrTrustNotFound
	}
	return store.saveCodeLocked(code)
}

func (store *MemoryStore) saveCodeLocked(code StoredCode) error {
	for key, existing := range store.codes {
		if !code.IssuedAt.Before(existing.ExpiresAt) {
			delete(store.codes, key)
		}
	}
	if _, found := store.codes[code.Verifier]; found {
		return ErrCodeConflict
	}
	store.codes[code.Verifier] = code
	return nil
}

func (store *MemoryStore) ConsumeCode(
	_ context.Context,
	codeVerifier string,
	now time.Time,
	trust StoredTrust,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	code, found := store.codes[codeVerifier]
	if !found || !now.Before(code.ExpiresAt) {
		if found {
			delete(store.codes, codeVerifier)
		}
		return ErrCodeUnavailable
	}
	if code.DeviceID != "" {
		existing, exists := store.trusts[code.DeviceID]
		if !exists || trust.DeviceID != code.DeviceID ||
			trust.DeviceIdentityVerifier != existing.DeviceIdentityVerifier ||
			trust.ProtocolVersion != existing.ProtocolVersion {
			return ErrCodeUnavailable
		}
		trust.CreatedAt = existing.CreatedAt
		trust.RotatedAt = now
	}
	delete(store.codes, codeVerifier)
	store.trusts[trust.DeviceID] = trust
	return nil
}

func (store *MemoryStore) LookupTrust(_ context.Context, deviceID string) (StoredTrust, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	trust, found := store.trusts[deviceID]
	if !found {
		return StoredTrust{}, ErrTrustNotFound
	}
	return trust, nil
}

func (store *MemoryStore) RevokeTrust(_ context.Context, deviceID string, _ time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, found := store.trusts[deviceID]; !found {
		return ErrTrustNotFound
	}
	delete(store.trusts, deviceID)
	return nil
}
