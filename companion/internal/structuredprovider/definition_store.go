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

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/configmodel"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/secretstore"
)

const (
	definitionStoreSchemaVersion = 3
	maximumStoredDefinitions     = 6
	maximumPendingCleanup        = 512
	maximumDefinitionStoreBytes  = 512 << 10
	definitionStoreLockName      = ".structured-providers.lock"
)

type definitionStoreState struct {
	SchemaVersion                   int                             `json:"schema_version"`
	Definitions                     []Definition                    `json:"definitions"`
	WebSettings                     configmodel.WebSettings         `json:"web_settings"`
	ApplicationSettings             configmodel.ApplicationSettings `json:"application_settings"`
	ApplyApplicationSettingsOnStart bool                            `json:"apply_application_settings_on_start"`
	DeviceProfiles                  []configmodel.DeviceProfile     `json:"device_profiles"`
	PendingSecretDeletes            []secretstore.Reference         `json:"pending_secret_deletes"`
}

type definitionStoreStateV1 struct {
	SchemaVersion        int                     `json:"schema_version"`
	Definitions          []Definition            `json:"definitions"`
	PendingSecretDeletes []secretstore.Reference `json:"pending_secret_deletes"`
}

type definitionStoreApplicationSettingsV2 struct {
	HistoryEnabled bool `json:"history_enabled"`
}

type definitionStoreStateV2 struct {
	SchemaVersion                   int                                  `json:"schema_version"`
	Definitions                     []Definition                         `json:"definitions"`
	WebSettings                     configmodel.WebSettings              `json:"web_settings"`
	ApplicationSettings             definitionStoreApplicationSettingsV2 `json:"application_settings"`
	ApplyApplicationSettingsOnStart bool                                 `json:"apply_application_settings_on_start"`
	DeviceProfiles                  []configmodel.DeviceProfile          `json:"device_profiles"`
	PendingSecretDeletes            []secretstore.Reference              `json:"pending_secret_deletes"`
}

// RestorableConfiguration is the complete non-secret state published by one
// protected-file replacement. Definition order is Provider display order.
type RestorableConfiguration struct {
	Definitions         []Definition
	WebSettings         configmodel.WebSettings
	ApplicationSettings configmodel.ApplicationSettings
	DeviceProfiles      []configmodel.DeviceProfile
}

type ReplaceConfigurationResult struct {
	Committed bool
	Retired   []secretstore.Reference
}

// DefinitionStore owns the atomic backup-restorable Companion configuration
// and its durable Provider secret-cleanup journal. It deliberately cannot read
// secret values. The historical name is retained for file/API migration.
type DefinitionStore struct {
	mutex sync.RWMutex
	path  string
	state definitionStoreState
	lock  *protectedfile.Lock
}

type ConfigurationStore = DefinitionStore

func OpenConfigurationStore(path string) (*ConfigurationStore, error) {
	return OpenDefinitionStore(path)
}

type cleanupSecretStore interface {
	DefinitionSecretStore
	ListMetadata(context.Context) ([]secretstore.Metadata, error)
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
	configmodel.DestroySerialPresets(store.state.ApplicationSettings.SerialPresets)
	store.state.ApplicationSettings.SerialPresets = nil
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

func (store *DefinitionStore) Configuration(ctx context.Context) (RestorableConfiguration, error) {
	if store == nil || ctx == nil {
		return RestorableConfiguration{}, ErrInvalidConfig
	}
	if err := ctx.Err(); err != nil {
		return RestorableConfiguration{}, err
	}
	store.mutex.RLock()
	defer store.mutex.RUnlock()
	if store.lock == nil {
		return RestorableConfiguration{}, ErrDefinitionCommit
	}
	return configurationFromState(store.state), nil
}

func (store *DefinitionStore) PendingApplicationSettings(
	ctx context.Context,
) (configmodel.ApplicationSettings, bool, error) {
	if store == nil || ctx == nil {
		return configmodel.ApplicationSettings{}, false, ErrInvalidConfig
	}
	if err := ctx.Err(); err != nil {
		return configmodel.ApplicationSettings{}, false, err
	}
	store.mutex.RLock()
	defer store.mutex.RUnlock()
	if store.lock == nil {
		return configmodel.ApplicationSettings{}, false, ErrDefinitionCommit
	}
	return configmodel.CloneApplicationSettings(store.state.ApplicationSettings), store.state.ApplyApplicationSettingsOnStart, nil
}

func (store *DefinitionStore) SerialPresets(ctx context.Context) ([]configmodel.SerialPreset, error) {
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
	return configmodel.CloneSerialPresets(store.state.ApplicationSettings.SerialPresets), nil
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
		currentReferences := make(map[secretstore.Reference]struct{})
		if current != nil {
			currentReferences = definitionReferences([]Definition{*current})
		}
		addedReferences := definitionReferences([]Definition{replacement})
		for reference := range currentReferences {
			delete(addedReferences, reference)
		}
		activatedReferences := make(map[secretstore.Reference]struct{}, len(activated))
		for _, reference := range activated {
			if _, duplicate := activatedReferences[reference]; duplicate {
				return ErrDefinitionCommit
			}
			activatedReferences[reference] = struct{}{}
		}
		if !reflect.DeepEqual(addedReferences, activatedReferences) {
			return ErrDefinitionCommit
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
		next.PendingSecretDeletes = sortedReferences(pending)
		return nil
	}, &committed)
	return committed, err
}

// ReplaceConfiguration performs the only non-secret import commit. Every new
// reference must already be in the durable cleanup journal and every retired
// reference is added to that journal by the same file replacement.
func (store *DefinitionStore) ReplaceConfiguration(
	ctx context.Context,
	expected RestorableConfiguration,
	replacement RestorableConfiguration,
	activated []secretstore.Reference,
) (ReplaceConfigurationResult, error) {
	result := ReplaceConfigurationResult{}
	err := store.updateWithCommit(ctx, func(next *definitionStoreState) error {
		current := configurationFromState(*next)
		expectedCopy := cloneRestorableConfiguration(expected)
		matches := reflect.DeepEqual(current, expectedCopy)
		DestroyRestorableConfiguration(&current)
		DestroyRestorableConfiguration(&expectedCopy)
		if !matches {
			return ErrDefinitionCommit
		}
		replacementCopy := cloneRestorableConfiguration(replacement)
		candidate := stateFromConfiguration(replacementCopy, next.PendingSecretDeletes)
		DestroyRestorableConfiguration(&replacementCopy)
		candidateOwned := true
		defer func() {
			if candidateOwned {
				configmodel.DestroySerialPresets(candidate.ApplicationSettings.SerialPresets)
			}
		}()
		candidate.ApplyApplicationSettingsOnStart = true
		currentReferences := definitionReferences(next.Definitions)
		replacementReferences := definitionReferences(candidate.Definitions)
		added := make(map[secretstore.Reference]struct{})
		for reference := range replacementReferences {
			if _, exists := currentReferences[reference]; !exists {
				added[reference] = struct{}{}
			}
		}
		activatedSet := make(map[secretstore.Reference]struct{}, len(activated))
		for _, reference := range activated {
			if _, duplicate := activatedSet[reference]; duplicate {
				return ErrDefinitionCommit
			}
			activatedSet[reference] = struct{}{}
		}
		if !reflect.DeepEqual(added, activatedSet) {
			return ErrDefinitionCommit
		}
		pending := referenceSet(next.PendingSecretDeletes)
		for _, reference := range activated {
			if _, staged := pending[reference]; !staged {
				return ErrDefinitionCommit
			}
			delete(pending, reference)
		}
		for reference := range currentReferences {
			if _, retained := replacementReferences[reference]; retained {
				continue
			}
			pending[reference] = struct{}{}
			result.Retired = append(result.Retired, reference)
		}
		sort.Slice(result.Retired, func(left, right int) bool {
			return result.Retired[left] < result.Retired[right]
		})
		candidate.PendingSecretDeletes = sortedReferences(pending)
		if err := validateDefinitionStoreState(candidate); err != nil {
			return err
		}
		configmodel.DestroySerialPresets(next.ApplicationSettings.SerialPresets)
		*next = candidate
		candidateOwned = false
		return nil
	}, &result.Committed)
	return result, err
}

func (store *DefinitionStore) UpdateWebSettings(
	ctx context.Context,
	settings configmodel.WebSettings,
) error {
	return store.update(ctx, func(next *definitionStoreState) error {
		if !configmodel.ValidateWebSettings(settings) {
			return ErrInvalidConfig
		}
		next.WebSettings = settings
		return nil
	})
}

// ReorderDefinitions atomically changes only Provider display order. The set
// must match exactly so a stale browser cannot silently drop or resurrect a
// concurrently edited Provider.
func (store *DefinitionStore) ReorderDefinitions(
	ctx context.Context,
	providerIDs []string,
) error {
	return store.update(ctx, func(next *definitionStoreState) error {
		if len(providerIDs) != len(next.Definitions) {
			return ErrDefinitionCommit
		}
		byID := make(map[string]Definition, len(next.Definitions))
		for _, definition := range next.Definitions {
			byID[definition.ID] = definition
		}
		ordered := make([]Definition, 0, len(providerIDs))
		for _, providerID := range providerIDs {
			definition, exists := byID[providerID]
			if !exists {
				return ErrDefinitionCommit
			}
			ordered = append(ordered, cloneDefinition(definition))
			delete(byID, providerID)
		}
		if len(byID) != 0 {
			return ErrDefinitionCommit
		}
		next.Definitions = ordered
		return nil
	})
}

func (store *DefinitionStore) UpdateApplicationSettings(
	ctx context.Context,
	settings configmodel.ApplicationSettings,
) error {
	if !configmodel.ValidateSerialPresets(settings.SerialPresets) {
		return ErrInvalidConfig
	}
	return store.update(ctx, func(next *definitionStoreState) error {
		configmodel.DestroySerialPresets(next.ApplicationSettings.SerialPresets)
		next.ApplicationSettings = configmodel.CloneApplicationSettings(settings)
		next.ApplyApplicationSettingsOnStart = false
		return nil
	})
}

func (store *DefinitionStore) UpdateSerialPresets(
	ctx context.Context,
	presets []configmodel.SerialPreset,
) error {
	if !configmodel.ValidateSerialPresets(presets) {
		return ErrInvalidConfig
	}
	return store.update(ctx, func(next *definitionStoreState) error {
		configmodel.DestroySerialPresets(next.ApplicationSettings.SerialPresets)
		next.ApplicationSettings.SerialPresets = configmodel.CloneSerialPresets(presets)
		next.ApplyApplicationSettingsOnStart = false
		return nil
	})
}

func (store *DefinitionStore) UpdateHistoryEnabled(ctx context.Context, enabled bool) error {
	return store.update(ctx, func(next *definitionStoreState) error {
		next.ApplicationSettings.HistoryEnabled = enabled
		next.ApplyApplicationSettingsOnStart = false
		return nil
	})
}

func (store *DefinitionStore) UpdateDeviceProfile(
	ctx context.Context,
	profile configmodel.DeviceProfile,
) error {
	return store.update(ctx, func(next *definitionStoreState) error {
		if !configmodel.ValidateDeviceProfile(profile) {
			return ErrInvalidConfig
		}
		for index := range next.DeviceProfiles {
			if next.DeviceProfiles[index].DeviceID == profile.DeviceID {
				if reflect.DeepEqual(next.DeviceProfiles[index], profile) {
					return nil
				}
				next.DeviceProfiles[index] = profile
				return nil
			}
		}
		if len(next.DeviceProfiles) >= configmodel.MaximumDeviceProfiles {
			return ErrDefinitionCommit
		}
		next.DeviceProfiles = append(next.DeviceProfiles, profile)
		sort.Slice(next.DeviceProfiles, func(left, right int) bool {
			return next.DeviceProfiles[left].DeviceID < next.DeviceProfiles[right].DeviceID
		})
		return nil
	})
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

// ReconcileCleanup recovers the only cross-store crash window: a process may
// stop after PutNew reserves a non-secret vault placeholder but before its
// cleanup intent reaches this file. Every vault reference not owned by an
// active definition or an existing cleanup record is first journaled and then
// idempotently deleted. Production calls this before accepting config edits.
func (store *DefinitionStore) ReconcileCleanup(
	ctx context.Context,
	secrets cleanupSecretStore,
) error {
	if store == nil || ctx == nil || secrets == nil {
		return ErrInvalidConfig
	}
	metadata, err := secrets.ListMetadata(ctx)
	if err != nil {
		return normalizeSecretStoreError(err)
	}
	store.mutex.RLock()
	active := definitionReferences(store.state.Definitions)
	pending := referenceSet(store.state.PendingSecretDeletes)
	store.mutex.RUnlock()
	orphans := make([]secretstore.Reference, 0, len(metadata))
	for _, item := range metadata {
		if _, owned := active[item.Reference]; owned {
			continue
		}
		if _, alreadyPending := pending[item.Reference]; alreadyPending {
			continue
		}
		orphans = append(orphans, item.Reference)
	}
	if len(orphans) != 0 {
		if err = store.StageCleanup(ctx, orphans); err != nil {
			return ErrSecretRollback
		}
	}
	return store.RetryCleanup(ctx, secrets)
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
	nextOwned := true
	defer func() {
		if nextOwned {
			configmodel.DestroySerialPresets(next.ApplicationSettings.SerialPresets)
		}
	}()
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
	defer clear(contents)
	*committed, err = protectedfile.Replace(store.path, contents)
	if *committed {
		configmodel.DestroySerialPresets(store.state.ApplicationSettings.SerialPresets)
		store.state = next
		nextOwned = false
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
		WebSettings:          configmodel.WebSettings{ManagementAddress: configmodel.DefaultManagementAddress},
		ApplicationSettings:  configmodel.ApplicationSettings{HistoryEnabled: true},
		DeviceProfiles:       []configmodel.DeviceProfile{},
		PendingSecretDeletes: []secretstore.Reference{},
	}
}

func cloneDefinitionStoreState(source definitionStoreState) definitionStoreState {
	clone := emptyDefinitionStoreState()
	for _, definition := range source.Definitions {
		clone.Definitions = append(clone.Definitions, cloneDefinition(definition))
	}
	clone.WebSettings = source.WebSettings
	clone.ApplicationSettings = configmodel.CloneApplicationSettings(source.ApplicationSettings)
	clone.ApplyApplicationSettingsOnStart = source.ApplyApplicationSettingsOnStart
	clone.DeviceProfiles = configmodel.CloneDeviceProfiles(source.DeviceProfiles)
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
	defer clear(contents)
	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if json.Unmarshal(contents, &header) != nil {
		return definitionStoreState{}, errors.New("structured Provider store is malformed")
	}
	var state definitionStoreState
	stateReturned := false
	defer func() {
		if !stateReturned {
			configmodel.DestroySerialPresets(state.ApplicationSettings.SerialPresets)
			state.ApplicationSettings.SerialPresets = nil
		}
	}()
	switch header.SchemaVersion {
	case 1:
		var previous definitionStoreStateV1
		if protocol.DecodeStrictDocumentLimit(contents, maximumDefinitionStoreBytes, &previous) != nil {
			return definitionStoreState{}, errors.New("structured Provider store is malformed")
		}
		state = emptyDefinitionStoreState()
		state.Definitions = previous.Definitions
		state.PendingSecretDeletes = previous.PendingSecretDeletes
	case 2:
		var previous definitionStoreStateV2
		if protocol.DecodeStrictDocumentLimit(contents, maximumDefinitionStoreBytes, &previous) != nil {
			return definitionStoreState{}, errors.New("structured Provider store is malformed")
		}
		state = emptyDefinitionStoreState()
		state.Definitions = previous.Definitions
		state.WebSettings = previous.WebSettings
		state.ApplicationSettings.HistoryEnabled = previous.ApplicationSettings.HistoryEnabled
		state.ApplyApplicationSettingsOnStart = previous.ApplyApplicationSettingsOnStart
		state.DeviceProfiles = previous.DeviceProfiles
		state.PendingSecretDeletes = previous.PendingSecretDeletes
	case definitionStoreSchemaVersion:
		if protocol.DecodeStrictDocumentLimit(contents, maximumDefinitionStoreBytes, &state) != nil {
			return definitionStoreState{}, errors.New("structured Provider store is malformed")
		}
	default:
		return definitionStoreState{}, errors.New("structured Provider store is malformed")
	}
	if validateDefinitionStoreState(state) != nil {
		return definitionStoreState{}, errors.New("structured Provider store is malformed")
	}
	stateReturned = true
	return state, nil
}

func validateDefinitionStoreState(state definitionStoreState) error {
	if state.SchemaVersion != definitionStoreSchemaVersion || state.Definitions == nil ||
		state.DeviceProfiles == nil || state.PendingSecretDeletes == nil ||
		len(state.Definitions) > maximumStoredDefinitions ||
		len(state.DeviceProfiles) > configmodel.MaximumDeviceProfiles ||
		len(state.PendingSecretDeletes) > maximumPendingCleanup {
		return ErrInvalidConfig
	}
	if !configmodel.ValidateWebSettings(state.WebSettings) {
		return ErrInvalidConfig
	}
	if !configmodel.ValidateSerialPresets(state.ApplicationSettings.SerialPresets) {
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
	deviceIDs := make(map[string]struct{}, len(state.DeviceProfiles))
	for _, profile := range state.DeviceProfiles {
		if !configmodel.ValidateDeviceProfile(profile) {
			return ErrInvalidConfig
		}
		if _, duplicate := deviceIDs[profile.DeviceID]; duplicate {
			return ErrInvalidConfig
		}
		deviceIDs[profile.DeviceID] = struct{}{}
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

func configurationFromState(state definitionStoreState) RestorableConfiguration {
	configuration := RestorableConfiguration{
		Definitions:         make([]Definition, len(state.Definitions)),
		WebSettings:         state.WebSettings,
		ApplicationSettings: configmodel.CloneApplicationSettings(state.ApplicationSettings),
		DeviceProfiles:      configmodel.CloneDeviceProfiles(state.DeviceProfiles),
	}
	for index := range state.Definitions {
		configuration.Definitions[index] = cloneDefinition(state.Definitions[index])
	}
	return configuration
}

func cloneRestorableConfiguration(source RestorableConfiguration) RestorableConfiguration {
	configuration := RestorableConfiguration{
		Definitions:         make([]Definition, len(source.Definitions)),
		WebSettings:         source.WebSettings,
		ApplicationSettings: configmodel.CloneApplicationSettings(source.ApplicationSettings),
		DeviceProfiles:      configmodel.CloneDeviceProfiles(source.DeviceProfiles),
	}
	for index := range source.Definitions {
		configuration.Definitions[index] = cloneDefinition(source.Definitions[index])
	}
	return configuration
}

// DestroyRestorableConfiguration clears user-authored Serial Preset payloads
// owned by a configuration copy. It does not mutate the store.
func DestroyRestorableConfiguration(configuration *RestorableConfiguration) {
	if configuration == nil {
		return
	}
	configmodel.DestroySerialPresets(configuration.ApplicationSettings.SerialPresets)
	configuration.ApplicationSettings.SerialPresets = nil
}

func stateFromConfiguration(
	configuration RestorableConfiguration,
	pending []secretstore.Reference,
) definitionStoreState {
	state := emptyDefinitionStoreState()
	state.Definitions = make([]Definition, len(configuration.Definitions))
	for index := range configuration.Definitions {
		state.Definitions[index] = cloneDefinition(configuration.Definitions[index])
	}
	state.WebSettings = configuration.WebSettings
	state.ApplicationSettings = configmodel.CloneApplicationSettings(configuration.ApplicationSettings)
	state.DeviceProfiles = configmodel.CloneDeviceProfiles(configuration.DeviceProfiles)
	state.PendingSecretDeletes = append([]secretstore.Reference(nil), pending...)
	return state
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
