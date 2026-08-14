package structuredprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/secretstore"
)

const (
	definitionStoreSchemaVersion = 1
	maximumStoredDefinitions     = 6
	maximumPendingCleanup        = 512
	maximumDefinitionStoreBytes  = 512 << 10
	definitionStoreLockName      = ".structured-providers.lock"
)

type definitionStoreState struct {
	SchemaVersion        int                     `json:"schema_version"`
	Definitions          []Definition            `json:"definitions"`
	PendingSecretDeletes []secretstore.Reference `json:"pending_secret_deletes"`
}

// DefinitionStore owns the atomic non-secret Provider configuration and its
// durable secret-cleanup journal. It deliberately cannot read secret values.
type DefinitionStore struct {
	mutex sync.RWMutex
	path  string
	state definitionStoreState
	lock  *protectedfile.Lock
}

func OpenDefinitionStore(path string) (*DefinitionStore, error) {
	if path == "" {
		return nil, ErrInvalidConfig
	}
	path = filepath.Clean(path)
	lock, err := protectedfile.AcquireDirectoryLock(filepath.Dir(path), definitionStoreLockName)
	if err != nil {
		return nil, err
	}
	closeLock := true
	defer func() {
		if closeLock {
			_ = lock.Close()
		}
	}()
	state := emptyDefinitionStoreState()
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("inspect structured Provider store: %w", err)
	case info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular():
		return nil, errors.New("structured Provider store must be a regular non-symlink file")
	default:
		if err = protectedfile.EnsurePrivateFile(path); err != nil {
			return nil, err
		}
		state, err = readDefinitionStore(path)
		if err != nil {
			return nil, err
		}
	}
	closeLock = false
	return &DefinitionStore{path: path, state: state, lock: lock}, nil
}

func (store *DefinitionStore) Close() error {
	if store == nil {
		return nil
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if store.lock == nil {
		return nil
	}
	err := store.lock.Close()
	store.lock = nil
	return err
}

func (store *DefinitionStore) Definitions(ctx context.Context) ([]Definition, error) {
	if store == nil || ctx == nil {
		return nil, ErrInvalidConfig
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mutex.RLock()
	defer store.mutex.RUnlock()
	if store.lock == nil {
		return nil, ErrDefinitionCommit
	}
	definitions := make([]Definition, len(store.state.Definitions))
	for index := range store.state.Definitions {
		definitions[index] = cloneDefinition(store.state.Definitions[index])
	}
	return definitions, nil
}

func (store *DefinitionStore) StageCleanup(
	ctx context.Context,
	references []secretstore.Reference,
) error {
	return store.update(ctx, func(next *definitionStoreState) error {
		active := definitionReferences(next.Definitions)
		pending := referenceSet(next.PendingSecretDeletes)
		for _, reference := range references {
			if _, err := secretstore.ParseReference(reference.String()); err != nil {
				return ErrInvalidConfig
			}
			if _, exists := active[reference]; exists {
				return secretstore.ErrDuplicate
			}
			pending[reference] = struct{}{}
		}
		next.PendingSecretDeletes = sortedReferences(pending)
		return nil
	})
}

func (store *DefinitionStore) Publish(
	ctx context.Context,
	current *Definition,
	replacement Definition,
	activated []secretstore.Reference,
) (bool, error) {
	committed := false
	err := store.updateWithCommit(ctx, func(next *definitionStoreState) error {
		normalized, normalizeErr := normalizeConfig(Config{Definition: replacement})
		if normalizeErr != nil || !reflect.DeepEqual(normalized.Definition, replacement) {
			return ErrInvalidConfig
		}
		index := -1
		if current != nil {
			for candidate := range next.Definitions {
				if next.Definitions[candidate].ID == current.ID {
					index = candidate
					break
				}
			}
			if index < 0 || !reflect.DeepEqual(next.Definitions[index], *current) {
				return ErrDefinitionCommit
			}
		} else {
			for _, definition := range next.Definitions {
				if definition.ID == replacement.ID {
					return ErrDefinitionCommit
				}
			}
			if len(next.Definitions) >= maximumStoredDefinitions {
				return ErrDefinitionCommit
			}
		}
		for candidate, definition := range next.Definitions {
			if candidate != index && definition.ID == replacement.ID {
				return ErrDefinitionCommit
			}
		}
		pending := referenceSet(next.PendingSecretDeletes)
		for _, reference := range activated {
			if _, staged := pending[reference]; !staged {
				return ErrDefinitionCommit
			}
			delete(pending, reference)
		}
		for _, reference := range retiredReferences(current, replacement) {
			pending[reference] = struct{}{}
		}
		if index < 0 {
			next.Definitions = append(next.Definitions, cloneDefinition(replacement))
		} else {
			next.Definitions[index] = cloneDefinition(replacement)
		}
		sort.Slice(next.Definitions, func(left, right int) bool {
			return next.Definitions[left].ID < next.Definitions[right].ID
		})
		next.PendingSecretDeletes = sortedReferences(pending)
		return nil
	}, &committed)
	return committed, err
}

func (store *DefinitionStore) CompleteCleanup(
	ctx context.Context,
	references []secretstore.Reference,
) error {
	return store.update(ctx, func(next *definitionStoreState) error {
		pending := referenceSet(next.PendingSecretDeletes)
		for _, reference := range references {
			delete(pending, reference)
		}
		next.PendingSecretDeletes = sortedReferences(pending)
		return nil
	})
}

// DeleteDefinition first removes the definition and journals all of its secret
// references in one file replacement, then performs idempotent vault cleanup.
// A cleanup failure leaves the journal durable for RetryCleanup.
func (store *DefinitionStore) DeleteDefinition(
	ctx context.Context,
	id string,
	secrets DefinitionSecretStore,
) error {
	if secrets == nil {
		return ErrInvalidConfig
	}
	var retired []secretstore.Reference
	committed := false
	err := store.updateWithCommit(ctx, func(next *definitionStoreState) error {
		index := -1
		for candidate := range next.Definitions {
			if next.Definitions[candidate].ID == id {
				index = candidate
				break
			}
		}
		if index < 0 {
			return ErrInvalidConfig
		}
		retired = retiredReferences(&next.Definitions[index], Definition{})
		next.Definitions = append(next.Definitions[:index], next.Definitions[index+1:]...)
		pending := referenceSet(next.PendingSecretDeletes)
		for _, reference := range retired {
			pending[reference] = struct{}{}
		}
		next.PendingSecretDeletes = sortedReferences(pending)
		return nil
	}, &committed)
	if !committed {
		if err != nil {
			return err
		}
		return ErrDefinitionCommit
	}
	if err != nil {
		return ErrDefinitionCommit
	}
	return store.cleanupReferences(ctx, secrets, retired)
}

// RetryCleanup replays only references already recorded in the protected
// journal. It is safe to call at every startup and after any interrupted edit.
func (store *DefinitionStore) RetryCleanup(
	ctx context.Context,
	secrets DefinitionSecretStore,
) error {
	if store == nil || ctx == nil || secrets == nil {
		return ErrInvalidConfig
	}
	store.mutex.RLock()
	references := append([]secretstore.Reference(nil), store.state.PendingSecretDeletes...)
	store.mutex.RUnlock()
	return store.cleanupReferences(ctx, secrets, references)
}

func (store *DefinitionStore) cleanupReferences(
	ctx context.Context,
	secrets DefinitionSecretStore,
	references []secretstore.Reference,
) error {
	var deleted []secretstore.Reference
	var pending []secretstore.Reference
	for _, reference := range references {
		if err := secrets.Delete(ctx, reference); err != nil {
			pending = append(pending, reference)
		} else {
			deleted = append(deleted, reference)
		}
	}
	if len(deleted) != 0 {
		if err := store.CompleteCleanup(ctx, deleted); err != nil {
			pending = append(pending, deleted...)
		}
	}
	if len(pending) != 0 {
		return &SecretRollbackError{pending: pending}
	}
	return nil
}

func (store *DefinitionStore) update(
	ctx context.Context,
	mutate func(*definitionStoreState) error,
) error {
	committed := false
	return store.updateWithCommit(ctx, mutate, &committed)
}

func (store *DefinitionStore) updateWithCommit(
	ctx context.Context,
	mutate func(*definitionStoreState) error,
	committed *bool,
) error {
	if store == nil || ctx == nil || mutate == nil || committed == nil {
		return ErrInvalidConfig
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if store.lock == nil {
		return ErrDefinitionCommit
	}
	next := cloneDefinitionStoreState(store.state)
	if err := mutate(&next); err != nil {
		return err
	}
	if err := validateDefinitionStoreState(next); err != nil {
		return err
	}
	contents, err := json.Marshal(next)
	if err != nil {
		return ErrDefinitionCommit
	}
	*committed, err = protectedfile.Replace(store.path, contents)
	if *committed {
		store.state = next
	}
	if err != nil {
		return ErrDefinitionCommit
	}
	if !*committed {
		return ErrDefinitionCommit
	}
	return nil
}

func emptyDefinitionStoreState() definitionStoreState {
	return definitionStoreState{
		SchemaVersion:        definitionStoreSchemaVersion,
		Definitions:          []Definition{},
		PendingSecretDeletes: []secretstore.Reference{},
	}
}

func cloneDefinitionStoreState(source definitionStoreState) definitionStoreState {
	clone := emptyDefinitionStoreState()
	for _, definition := range source.Definitions {
		clone.Definitions = append(clone.Definitions, cloneDefinition(definition))
	}
	clone.PendingSecretDeletes = append(clone.PendingSecretDeletes, source.PendingSecretDeletes...)
	return clone
}

func readDefinitionStore(path string) (definitionStoreState, error) {
	info, err := os.Stat(path)
	if err != nil || info.Size() > maximumDefinitionStoreBytes {
		return definitionStoreState{}, errors.New("structured Provider store is unavailable")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return definitionStoreState{}, errors.New("structured Provider store is unavailable")
	}
	var state definitionStoreState
	if protocol.DecodeStrictDocumentLimit(contents, maximumDefinitionStoreBytes, &state) != nil ||
		validateDefinitionStoreState(state) != nil {
		return definitionStoreState{}, errors.New("structured Provider store is malformed")
	}
	return state, nil
}

func validateDefinitionStoreState(state definitionStoreState) error {
	if state.SchemaVersion != definitionStoreSchemaVersion || state.Definitions == nil ||
		state.PendingSecretDeletes == nil || len(state.Definitions) > maximumStoredDefinitions ||
		len(state.PendingSecretDeletes) > maximumPendingCleanup {
		return ErrInvalidConfig
	}
	ids := make(map[string]struct{}, len(state.Definitions))
	active := make(map[secretstore.Reference]struct{})
	for _, definition := range state.Definitions {
		normalized, err := normalizeConfig(Config{Definition: definition})
		if err != nil || !reflect.DeepEqual(normalized.Definition, definition) {
			return ErrInvalidConfig
		}
		if _, duplicate := ids[definition.ID]; duplicate {
			return ErrInvalidConfig
		}
		ids[definition.ID] = struct{}{}
		local := make(map[secretstore.Reference]struct{})
		for _, header := range definition.Request.Headers {
			reference := header.SecretReference
			if reference == "" {
				continue
			}
			if _, duplicate := local[reference]; duplicate {
				return ErrInvalidConfig
			}
			local[reference] = struct{}{}
			if _, duplicate := active[reference]; duplicate {
				return ErrInvalidConfig
			}
			active[reference] = struct{}{}
		}
	}
	pending := make(map[secretstore.Reference]struct{}, len(state.PendingSecretDeletes))
	for _, reference := range state.PendingSecretDeletes {
		if _, err := secretstore.ParseReference(reference.String()); err != nil {
			return ErrInvalidConfig
		}
		if _, inUse := active[reference]; inUse {
			return ErrInvalidConfig
		}
		if _, duplicate := pending[reference]; duplicate {
			return ErrInvalidConfig
		}
		pending[reference] = struct{}{}
	}
	return nil
}

func definitionReferences(definitions []Definition) map[secretstore.Reference]struct{} {
	references := make(map[secretstore.Reference]struct{})
	for _, definition := range definitions {
		for _, header := range definition.Request.Headers {
			if header.SecretReference != "" {
				references[header.SecretReference] = struct{}{}
			}
		}
	}
	return references
}

func referenceSet(references []secretstore.Reference) map[secretstore.Reference]struct{} {
	set := make(map[secretstore.Reference]struct{}, len(references))
	for _, reference := range references {
		set[reference] = struct{}{}
	}
	return set
}

func sortedReferences(set map[secretstore.Reference]struct{}) []secretstore.Reference {
	references := make([]secretstore.Reference, 0, len(set))
	for reference := range set {
		references = append(references, reference)
	}
	sort.Slice(references, func(left, right int) bool { return references[left] < references[right] })
	return references
}
