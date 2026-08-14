// Package secretstore owns user-entered Provider credentials in the current
// user's platform vault. Persisted configuration contains only Reference
// values; vault service names, account names, and secret bytes stay inside this
// module.
package secretstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	providerSecretService = "S3 RLCD Deck Companion Provider"
	referencePrefix       = "secret-"
	referenceRandomBytes  = 16
	// MaximumSecretBytes is the cross-store credential bound. Encrypted
	// backups use it so every accepted archive can be staged into the Vault.
	MaximumSecretBytes          = 2560
	maximumSecretBytes          = MaximumSecretBytes
	maximumMetadata             = 512
	maximumCreateAttempts       = 8
	failedReserveCleanupTimeout = 5 * time.Second
)

var (
	ErrNotFound    = errors.New("secret reference not found")
	ErrDuplicate   = errors.New("secret reference already exists")
	ErrLocked      = errors.New("platform secret store is locked")
	ErrPermission  = errors.New("platform secret store permission denied")
	ErrCanceled    = errors.New("platform secret store operation canceled")
	ErrUnavailable = errors.New("platform secret store unavailable")
	ErrInvalid     = errors.New("invalid secret store input")
	ErrCorrupt     = errors.New("platform secret store metadata is malformed")
)

// Reference is an opaque, non-secret identifier safe to persist in Provider
// configuration. Its internal format is owned by this module.
type Reference string

func (reference Reference) String() string { return string(reference) }

// ParseReference validates a persisted reference without accessing the vault.
func ParseReference(value string) (Reference, error) {
	reference := Reference(value)
	if !validReference(reference) {
		return "", ErrInvalid
	}
	return reference, nil
}

type Metadata struct {
	Reference Reference `json:"reference"`
}

type vault interface {
	Create(context.Context, string, []byte) error
	Update(context.Context, string, []byte) error
	Get(context.Context, string) ([]byte, error)
	Delete(context.Context, string) error
	List(context.Context) ([]string, error)
}

// Store serializes Provider-secret mutations and hides every platform-specific
// naming and lifecycle rule behind four operations.
type Store struct {
	mutex  sync.Mutex
	vault  vault
	random io.Reader
}

// Open creates the production Store for the current user's macOS Keychain or
// Windows Credential Manager.
func Open() (*Store, error) {
	return openService(providerSecretService)
}

// OpenForDataDirectory scopes the native vault namespace to one canonical
// Companion data-directory owner. Multiple valid --data-directory instances
// therefore cannot enumerate, reconcile, or delete each other's credentials.
func OpenForDataDirectory(dataDirectory string) (*Store, error) {
	service, err := providerSecretServiceForDataDirectory(dataDirectory)
	if err != nil {
		return nil, err
	}
	return openService(service)
}

func openService(service string) (*Store, error) {
	adapter, err := platformVault(service)
	if err != nil {
		return nil, err
	}
	return newStore(adapter, rand.Reader)
}

func providerSecretServiceForDataDirectory(dataDirectory string) (string, error) {
	if dataDirectory == "" {
		return "", ErrInvalid
	}
	canonical, err := filepath.Abs(filepath.Clean(dataDirectory))
	if err != nil {
		return "", ErrInvalid
	}
	if resolved, resolveErr := filepath.EvalSymlinks(canonical); resolveErr == nil {
		canonical = resolved
	}
	if runtime.GOOS == "windows" {
		canonical = strings.ToLower(canonical)
	}
	digest := sha256.Sum256([]byte(canonical))
	return providerSecretService + "/owner-" + hex.EncodeToString(digest[:16]), nil
}

func newStore(adapter vault, random io.Reader) (*Store, error) {
	if adapter == nil || random == nil {
		return nil, ErrInvalid
	}
	return &Store{vault: adapter, random: random}, nil
}

// Put creates a new secret when current is empty, or atomically replaces the
// secret at current. A failed update preserves the previous value. Secret is
// caller-owned and is never retained or overwritten by Store.
func (store *Store) Put(
	ctx context.Context,
	current Reference,
	secret []byte,
) (Reference, error) {
	if store == nil || ctx == nil || len(secret) == 0 || len(secret) > maximumSecretBytes {
		return "", ErrInvalid
	}
	if current != "" && !validReference(current) {
		return "", ErrInvalid
	}
	owned := append([]byte(nil), secret...)
	defer overwrite(owned)

	store.mutex.Lock()
	defer store.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if current != "" {
		if err := store.vault.Update(ctx, accountName(current), owned); err != nil {
			return "", normalizeVaultError(err)
		}
		return current, nil
	}
	return store.createLocked(ctx, owned, func(Reference) error { return nil })
}

// PutNew first atomically reserves a collision-free reference in the vault
// using a non-secret placeholder. beforeSecret then durably records that
// reference before the placeholder is replaced by the actual secret. The
// callback must not call back into Store. A duplicate reference is retried
// before the callback and therefore can never be journaled or deleted by this
// transaction.
func (store *Store) PutNew(
	ctx context.Context,
	secret []byte,
	beforeSecret func(Reference) error,
) (Reference, error) {
	if store == nil || ctx == nil || beforeSecret == nil || len(secret) == 0 ||
		len(secret) > maximumSecretBytes {
		return "", ErrInvalid
	}
	owned := append([]byte(nil), secret...)
	defer overwrite(owned)
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	for range maximumCreateAttempts {
		reference, err := store.newReference()
		if err != nil {
			return "", ErrUnavailable
		}
		placeholder := []byte{0}
		err = store.vault.Create(ctx, accountName(reference), placeholder)
		overwrite(placeholder)
		if errors.Is(err, ErrDuplicate) {
			continue
		}
		if err != nil {
			return "", normalizeVaultError(err)
		}
		if err = beforeSecret(reference); err != nil {
			cleanupContext, cancel := context.WithTimeout(
				context.Background(), failedReserveCleanupTimeout,
			)
			deleteErr := store.vault.Delete(cleanupContext, accountName(reference))
			cancel()
			if deleteErr != nil && !errors.Is(deleteErr, ErrNotFound) {
				return "", normalizeVaultError(deleteErr)
			}
			if errors.Is(err, ErrDuplicate) {
				continue
			}
			return "", err
		}
		if err = store.vault.Update(ctx, accountName(reference), owned); err != nil {
			// The durable intent is now responsible for idempotent cleanup of
			// the placeholder if the secret write did not commit.
			return "", normalizeVaultError(err)
		}
		return reference, nil
	}
	return "", ErrUnavailable
}

func (store *Store) createLocked(
	ctx context.Context,
	secret []byte,
	beforeCreate func(Reference) error,
) (Reference, error) {
	for range maximumCreateAttempts {
		reference, err := store.newReference()
		if err != nil {
			return "", ErrUnavailable
		}
		if err = beforeCreate(reference); errors.Is(err, ErrDuplicate) {
			continue
		} else if err != nil {
			return "", err
		}
		err = store.vault.Create(ctx, accountName(reference), secret)
		if errors.Is(err, ErrDuplicate) {
			continue
		}
		if err != nil {
			return "", normalizeVaultError(err)
		}
		return reference, nil
	}
	return "", ErrUnavailable
}

// Get returns caller-owned secret bytes. The caller must overwrite them as
// soon as the one operation needing the secret has finished.
func (store *Store) Get(ctx context.Context, reference Reference) ([]byte, error) {
	if store == nil || ctx == nil || !validReference(reference) {
		return nil, ErrInvalid
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	secret, err := store.vault.Get(ctx, accountName(reference))
	if err != nil {
		overwrite(secret)
		return nil, normalizeVaultError(err)
	}
	if len(secret) == 0 || len(secret) > maximumSecretBytes {
		overwrite(secret)
		return nil, ErrCorrupt
	}
	return secret, nil
}

// Delete is idempotent so uninstall and compensation can safely retry it.
func (store *Store) Delete(ctx context.Context, reference Reference) error {
	if store == nil || ctx == nil || !validReference(reference) {
		return ErrInvalid
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	err := store.vault.Delete(ctx, accountName(reference))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return normalizeVaultError(err)
}

// ListMetadata returns only references owned by this Provider store. It never
// reads or returns secret values.
func (store *Store) ListMetadata(ctx context.Context) ([]Metadata, error) {
	if store == nil || ctx == nil {
		return nil, ErrInvalid
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	accounts, err := store.vault.List(ctx)
	if err != nil {
		return nil, normalizeVaultError(err)
	}
	if len(accounts) > maximumMetadata {
		return nil, ErrCorrupt
	}
	seen := make(map[Reference]struct{}, len(accounts))
	metadata := make([]Metadata, 0, len(accounts))
	for _, account := range accounts {
		reference, ok := referenceFromAccount(account)
		if !ok {
			return nil, ErrCorrupt
		}
		if _, duplicate := seen[reference]; duplicate {
			return nil, ErrCorrupt
		}
		seen[reference] = struct{}{}
		metadata = append(metadata, Metadata{Reference: reference})
	}
	sort.Slice(metadata, func(left, right int) bool {
		return metadata[left].Reference < metadata[right].Reference
	})
	return metadata, nil
}

func (store *Store) newReference() (Reference, error) {
	random := make([]byte, referenceRandomBytes)
	if _, err := io.ReadFull(store.random, random); err != nil {
		overwrite(random)
		return "", err
	}
	reference := Reference(referencePrefix + hex.EncodeToString(random))
	overwrite(random)
	return reference, nil
}

func validReference(reference Reference) bool {
	value := string(reference)
	if len(value) != len(referencePrefix)+referenceRandomBytes*2 ||
		!strings.HasPrefix(value, referencePrefix) {
		return false
	}
	for _, character := range value[len(referencePrefix):] {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func accountName(reference Reference) string { return string(reference) }

func referenceFromAccount(account string) (Reference, bool) {
	reference := Reference(account)
	return reference, validReference(reference)
}

func normalizeVaultError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	for _, known := range []error{
		ErrNotFound, ErrDuplicate, ErrLocked, ErrPermission, ErrCanceled,
		ErrUnavailable, ErrInvalid, ErrCorrupt,
	} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ErrUnavailable
}

func overwrite(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
