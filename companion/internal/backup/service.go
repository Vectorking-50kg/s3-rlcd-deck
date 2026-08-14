package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/configmodel"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/secretstore"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/structuredprovider"
)

const fileOperationTimeout = 30 * time.Second

var (
	ErrInvalidMode      = errors.New("backup import mode is invalid")
	ErrConflictDecision = errors.New("backup conflict decisions are incomplete or invalid")
	ErrPreviewRequired  = errors.New("a current backup preview is required")
	ErrSecrets          = errors.New("backup Provider credentials are unavailable")
	ErrCommit           = errors.New("backup import configuration commit failed")
	ErrRollback         = errors.New("backup import secret rollback remains pending")
	ErrCleanupPending   = errors.New("backup import committed with secret cleanup pending")
	ErrFile             = errors.New("backup file operation failed")
)

type ImportMode string

const (
	ModeMerge         ImportMode = "merge"
	ModeReplace       ImportMode = "replace"
	ModeProvidersOnly ImportMode = "providers_only"
)

type ConflictDecision string

const (
	DecisionKeepCurrent ConflictDecision = "keep_current"
	DecisionUseBackup   ConflictDecision = "use_backup"
)

type Conflict struct {
	Key              string `json:"key"`
	Kind             string `json:"kind"`
	CurrentLabel     string `json:"current_label"`
	BackupLabel      string `json:"backup_label"`
	DecisionRequired bool   `json:"decision_required"`
}

type ProviderPreview struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	SecretCount int    `json:"secret_count"`
	Conflict    bool   `json:"conflict"`
}

// Preview contains only display-safe metadata. It never includes Provider
// URLs, request bodies, headers, Secret References, or secret values.
type Preview struct {
	SchemaVersion       SchemaVersion     `json:"schema_version"`
	ExportedAt          string            `json:"exported_at"`
	Mode                ImportMode        `json:"mode"`
	Providers           []ProviderPreview `json:"providers"`
	DeviceProfileCount  int               `json:"device_profile_count"`
	Conflicts           []Conflict        `json:"conflicts"`
	ReplaceWarning      bool              `json:"replace_warning"`
	RestartRequired     bool              `json:"restart_required"`
	PreviewID           string            `json:"preview_id"`
	ExcludedDataClasses []string          `json:"excluded_data_classes"`
}

type ImportResult struct {
	Committed           bool `json:"committed"`
	RestartRequired     bool `json:"restart_required"`
	CleanupPending      bool `json:"cleanup_pending"`
	VerificationWarning bool `json:"verification_warning"`
	ImportedProviders   int  `json:"imported_providers"`
}

type configurationOwner interface {
	Configuration(context.Context) (structuredprovider.RestorableConfiguration, error)
	StageCleanup(context.Context, []secretstore.Reference) error
	ReplaceConfiguration(
		context.Context,
		structuredprovider.RestorableConfiguration,
		structuredprovider.RestorableConfiguration,
		[]secretstore.Reference,
	) (structuredprovider.ReplaceConfigurationResult, error)
	CompleteCleanup(context.Context, []secretstore.Reference) error
}

type providerSecretStore interface {
	Get(context.Context, secretstore.Reference) ([]byte, error)
	PutNew(context.Context, []byte, func(secretstore.Reference) error) (secretstore.Reference, error)
	Delete(context.Context, secretstore.Reference) error
}

type Service struct {
	owner    configurationOwner
	secrets  providerSecretStore
	now      func() time.Time
	random   io.Reader
	previewM sync.Mutex
	previews map[[sha256.Size]byte]previewReceipt
}

type previewReceipt struct {
	archive       [sha256.Size]byte
	configuration [sha256.Size]byte
	mode          ImportMode
	expiresAt     time.Time
}

func NewService(
	owner configurationOwner,
	secrets providerSecretStore,
	now func() time.Time,
) (*Service, error) {
	if owner == nil || secrets == nil {
		return nil, ErrCommit
	}
	if now == nil {
		now = time.Now
	}
	return &Service{
		owner: owner, secrets: secrets, now: now, random: rand.Reader,
		previews: make(map[[sha256.Size]byte]previewReceipt),
	}, nil
}

func (service *Service) Export(ctx context.Context, passphrase []byte) ([]byte, error) {
	if service == nil || ctx == nil {
		return nil, ErrCommit
	}
	if !validPassphrase(passphrase) {
		return nil, ErrInvalidPassphrase
	}
	configuration, err := service.owner.Configuration(ctx)
	if err != nil {
		return nil, ErrCommit
	}
	document := Document{
		Type:                ArchiveType,
		SchemaVersion:       SchemaVersion{Major: SchemaMajor, Minor: SchemaMinor},
		ExportedAt:          service.now().UTC().Format(time.RFC3339Nano),
		Providers:           make([]Provider, 0, len(configuration.Definitions)),
		ProviderOrder:       make([]string, 0, len(configuration.Definitions)),
		WebSettings:         configuration.WebSettings,
		ApplicationSettings: configuration.ApplicationSettings,
		DeviceProfiles:      configmodel.CloneDeviceProfiles(configuration.DeviceProfiles),
	}
	defer document.Destroy()
	for _, storedDefinition := range configuration.Definitions {
		draft := cloneDefinition(storedDefinition)
		document.Providers = append(document.Providers, Provider{
			Definition: draft,
			Secrets:    make([]ProviderSecret, len(draft.Request.Headers)),
		})
		provider := &document.Providers[len(document.Providers)-1]
		for headerIndex := range provider.Definition.Request.Headers {
			reference := provider.Definition.Request.Headers[headerIndex].SecretReference
			secret, secretErr := service.secrets.Get(ctx, reference)
			if secretErr != nil {
				overwrite(secret)
				return nil, ErrSecrets
			}
			provider.Definition.Request.Headers[headerIndex].SecretReference = ""
			provider.Secrets[headerIndex] = ProviderSecret{
				HeaderIndex: headerIndex,
				Value:       secret,
			}
		}
		document.ProviderOrder = append(document.ProviderOrder, draft.ID)
	}
	return Encrypt(ctx, &document, passphrase)
}

func (service *Service) ExportFile(
	ctx context.Context,
	path string,
	passphrase []byte,
) error {
	if path == "" {
		return ErrFile
	}
	encrypted, err := service.Export(ctx, passphrase)
	if err != nil {
		return err
	}
	defer overwrite(encrypted)
	committed, err := protectedfile.ReplaceFile(filepath.Clean(path), encrypted)
	if err != nil || !committed {
		return ErrFile
	}
	return nil
}

func ReadFile(path string) ([]byte, error) {
	if path == "" {
		return nil, ErrFile
	}
	path = filepath.Clean(path)
	contents, err := protectedfile.Read(path, MaxEncryptedBytes)
	if err != nil || len(contents) == 0 {
		return nil, ErrFile
	}
	return contents, nil
}

func (service *Service) Preview(
	ctx context.Context,
	encrypted []byte,
	passphrase []byte,
	mode ImportMode,
) (Preview, error) {
	if service == nil || ctx == nil {
		return Preview{}, ErrCommit
	}
	document, err := Decrypt(ctx, encrypted, passphrase)
	if err != nil {
		return Preview{}, err
	}
	defer document.Destroy()
	current, err := service.owner.Configuration(ctx)
	if err != nil {
		return Preview{}, ErrCommit
	}
	preview, err := previewDocument(*document, current, mode)
	if err != nil {
		return Preview{}, err
	}
	previewID, err := service.issuePreview(encrypted, current, mode)
	if err != nil {
		return Preview{}, err
	}
	preview.PreviewID = previewID
	return preview, nil
}

func (service *Service) Import(
	ctx context.Context,
	encrypted []byte,
	passphrase []byte,
	mode ImportMode,
	decisions map[string]ConflictDecision,
	previewID string,
) (ImportResult, error) {
	if service == nil || ctx == nil {
		return ImportResult{}, ErrCommit
	}
	document, err := Decrypt(ctx, encrypted, passphrase)
	if err != nil {
		return ImportResult{}, err
	}
	defer document.Destroy()
	current, err := service.owner.Configuration(ctx)
	if err != nil {
		return ImportResult{}, ErrCommit
	}
	if !service.consumePreview(previewID, encrypted, current, mode) {
		return ImportResult{}, ErrPreviewRequired
	}
	plan, err := planImport(*document, current, mode, decisions)
	if err != nil {
		return ImportResult{}, err
	}
	if len(plan.configuration.Definitions) > 6 ||
		len(plan.configuration.DeviceProfiles) > configmodel.MaximumDeviceProfiles {
		return ImportResult{}, ErrCommit
	}
	staged := make([]secretstore.Reference, 0)
	rollback := func() error {
		var deleted []secretstore.Reference
		var pending bool
		cleanupContext, cancel := context.WithTimeout(context.Background(), fileOperationTimeout)
		defer cancel()
		for index := len(staged) - 1; index >= 0; index-- {
			if deleteErr := service.secrets.Delete(cleanupContext, staged[index]); deleteErr != nil {
				pending = true
			} else {
				deleted = append(deleted, staged[index])
			}
		}
		if len(deleted) != 0 && service.owner.CompleteCleanup(cleanupContext, deleted) != nil {
			pending = true
		}
		if pending {
			return ErrRollback
		}
		return nil
	}
	activated := make([]secretstore.Reference, 0)
	for definitionIndex := range plan.configuration.Definitions {
		definition := &plan.configuration.Definitions[definitionIndex]
		provider, fromBackup := plan.backupProviders[definition.ID]
		if !fromBackup {
			continue
		}
		for secretIndex := range provider.Secrets {
			secret := provider.Secrets[secretIndex]
			reference, putErr := service.secrets.PutNew(
				ctx,
				secret.Value,
				func(reference secretstore.Reference) error {
					if stageErr := service.owner.StageCleanup(ctx, []secretstore.Reference{reference}); stageErr != nil {
						return stageErr
					}
					staged = append(staged, reference)
					return nil
				},
			)
			if putErr != nil {
				if rollbackErr := rollback(); rollbackErr != nil {
					return ImportResult{}, rollbackErr
				}
				return ImportResult{}, ErrSecrets
			}
			definition.Request.Headers[secret.HeaderIndex].SecretReference = reference
			activated = append(activated, reference)
		}
	}
	replaceResult, replaceErr := service.owner.ReplaceConfiguration(
		ctx, current, plan.configuration, activated,
	)
	if !replaceResult.Committed {
		if rollbackErr := rollback(); rollbackErr != nil {
			return ImportResult{}, rollbackErr
		}
		return ImportResult{}, ErrCommit
	}
	result := ImportResult{
		Committed:         true,
		RestartRequired:   true,
		ImportedProviders: len(plan.backupProviders),
	}
	cleanupErr := service.cleanupRetired(replaceResult.Retired)
	if replaceErr != nil {
		result.VerificationWarning = true
		return result, ErrCommit
	}
	if cleanupErr != nil {
		result.CleanupPending = true
		return result, ErrCleanupPending
	}
	return result, nil
}

func (service *Service) issuePreview(
	encrypted []byte,
	configuration structuredprovider.RestorableConfiguration,
	mode ImportMode,
) (string, error) {
	configurationDigest, ok := configurationDigest(configuration)
	if !ok {
		return "", ErrCommit
	}
	now := service.now()
	for range 8 {
		random := make([]byte, sha256.Size)
		if _, err := io.ReadFull(service.random, random); err != nil {
			overwrite(random)
			return "", ErrCommit
		}
		token := sha256.Sum256(random)
		overwrite(random)
		service.previewM.Lock()
		for existing, receipt := range service.previews {
			if !now.Before(receipt.expiresAt) {
				delete(service.previews, existing)
			}
		}
		if len(service.previews) >= 64 {
			service.previewM.Unlock()
			return "", ErrCommit
		}
		if _, collision := service.previews[token]; collision {
			service.previewM.Unlock()
			continue
		}
		service.previews[token] = previewReceipt{
			archive: sha256.Sum256(encrypted), configuration: configurationDigest,
			mode: mode, expiresAt: now.Add(10 * time.Minute),
		}
		service.previewM.Unlock()
		return base64.RawURLEncoding.EncodeToString(token[:]), nil
	}
	return "", ErrCommit
}

func (service *Service) consumePreview(
	previewID string,
	encrypted []byte,
	configuration structuredprovider.RestorableConfiguration,
	mode ImportMode,
) bool {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(previewID)
	if err != nil || len(decoded) != sha256.Size {
		overwrite(decoded)
		return false
	}
	var token [sha256.Size]byte
	copy(token[:], decoded)
	overwrite(decoded)
	configurationDigest, ok := configurationDigest(configuration)
	if !ok {
		return false
	}
	service.previewM.Lock()
	receipt, exists := service.previews[token]
	delete(service.previews, token)
	service.previewM.Unlock()
	return exists && service.now().Before(receipt.expiresAt) && receipt.mode == mode &&
		receipt.archive == sha256.Sum256(encrypted) && receipt.configuration == configurationDigest
}

func configurationDigest(configuration structuredprovider.RestorableConfiguration) ([sha256.Size]byte, bool) {
	encoded, err := json.Marshal(configuration)
	if err != nil {
		return [sha256.Size]byte{}, false
	}
	digest := sha256.Sum256(encoded)
	overwrite(encoded)
	return digest, true
}

type importPlan struct {
	configuration   structuredprovider.RestorableConfiguration
	backupProviders map[string]Provider
}

func previewDocument(
	document Document,
	current structuredprovider.RestorableConfiguration,
	mode ImportMode,
) (Preview, error) {
	if !validMode(mode) {
		return Preview{}, ErrInvalidMode
	}
	currentProviders := providerDefinitionsByID(current.Definitions)
	preview := Preview{
		SchemaVersion:      document.SchemaVersion,
		ExportedAt:         document.ExportedAt,
		Mode:               mode,
		Providers:          make([]ProviderPreview, 0, len(document.Providers)),
		DeviceProfileCount: len(document.DeviceProfiles),
		ReplaceWarning:     mode == ModeReplace,
		RestartRequired:    true,
		ExcludedDataClasses: []string{
			"auto_discovered_credentials", "pairing_tokens", "web_sessions",
			"sqlite_history", "serial_buffers",
		},
	}
	required := mode != ModeReplace
	for _, provider := range providersInOrder(document) {
		_, conflict := currentProviders[provider.Definition.ID]
		preview.Providers = append(preview.Providers, ProviderPreview{
			ID: provider.Definition.ID, DisplayName: provider.Definition.DisplayName,
			SecretCount: len(provider.Secrets), Conflict: conflict,
		})
		if conflict {
			preview.Conflicts = append(preview.Conflicts, Conflict{
				Key: "provider:" + provider.Definition.ID, Kind: "provider",
				CurrentLabel: currentProviders[provider.Definition.ID].DisplayName,
				BackupLabel:  provider.Definition.DisplayName, DecisionRequired: required,
			})
		}
	}
	if mode != ModeReplace {
		keepOrder, backupOrder := providerOrderChoices(current.Definitions, document.ProviderOrder)
		if !slices.Equal(keepOrder, backupOrder) {
			preview.Conflicts = append(preview.Conflicts, Conflict{
				Key: "provider_order", Kind: "provider_order",
				CurrentLabel: "current Provider order", BackupLabel: "backup Provider order",
				DecisionRequired: true,
			})
		}
	}
	if mode != ModeProvidersOnly {
		if !reflect.DeepEqual(current.WebSettings, document.WebSettings) {
			preview.Conflicts = append(preview.Conflicts, settingConflict("web_settings", required))
		}
		if !reflect.DeepEqual(current.ApplicationSettings, document.ApplicationSettings) {
			preview.Conflicts = append(preview.Conflicts, settingConflict("application_settings", required))
		}
		currentDevices := deviceProfilesByID(current.DeviceProfiles)
		for _, profile := range document.DeviceProfiles {
			if existing, conflict := currentDevices[profile.DeviceID]; conflict && !reflect.DeepEqual(existing, profile) {
				preview.Conflicts = append(preview.Conflicts, Conflict{
					Key: "device:" + profile.DeviceID, Kind: "device_profile",
					CurrentLabel: profile.DeviceID, BackupLabel: profile.DeviceID,
					DecisionRequired: required,
				})
			}
		}
	}
	return preview, nil
}

func planImport(
	document Document,
	current structuredprovider.RestorableConfiguration,
	mode ImportMode,
	decisions map[string]ConflictDecision,
) (importPlan, error) {
	preview, err := previewDocument(document, current, mode)
	if err != nil {
		return importPlan{}, err
	}
	required := make(map[string]struct{})
	for _, conflict := range preview.Conflicts {
		if conflict.DecisionRequired {
			required[conflict.Key] = struct{}{}
		}
	}
	if mode == ModeReplace && len(decisions) != 0 {
		return importPlan{}, ErrConflictDecision
	}
	for key, decision := range decisions {
		if _, known := required[key]; !known ||
			(decision != DecisionKeepCurrent && decision != DecisionUseBackup) {
			return importPlan{}, ErrConflictDecision
		}
		delete(required, key)
	}
	if len(required) != 0 {
		return importPlan{}, ErrConflictDecision
	}
	plan := importPlan{backupProviders: make(map[string]Provider)}
	switch mode {
	case ModeReplace:
		plan.configuration = configurationFromDocument(document)
		for _, provider := range document.Providers {
			plan.backupProviders[provider.Definition.ID] = provider
		}
	case ModeMerge, ModeProvidersOnly:
		plan.configuration = cloneConfiguration(current)
		backupByID := providerArchiveByID(document.Providers)
		currentIndex := make(map[string]int, len(plan.configuration.Definitions))
		for index, definition := range plan.configuration.Definitions {
			currentIndex[definition.ID] = index
		}
		for _, provider := range providersInOrder(document) {
			index, conflict := currentIndex[provider.Definition.ID]
			if conflict {
				if decisions["provider:"+provider.Definition.ID] == DecisionUseBackup {
					plan.configuration.Definitions[index] = cloneDefinition(provider.Definition)
					plan.backupProviders[provider.Definition.ID] = backupByID[provider.Definition.ID]
				}
				continue
			}
			currentIndex[provider.Definition.ID] = len(plan.configuration.Definitions)
			plan.configuration.Definitions = append(
				plan.configuration.Definitions, cloneDefinition(provider.Definition),
			)
			plan.backupProviders[provider.Definition.ID] = backupByID[provider.Definition.ID]
		}
		if decisions["provider_order"] == DecisionUseBackup {
			_, backupOrder := providerOrderChoices(current.Definitions, document.ProviderOrder)
			plan.configuration.Definitions = definitionsInOrder(
				plan.configuration.Definitions, backupOrder,
			)
		}
		if mode == ModeMerge {
			if !reflect.DeepEqual(current.WebSettings, document.WebSettings) &&
				decisions["web_settings"] == DecisionUseBackup {
				plan.configuration.WebSettings = document.WebSettings
			}
			if !reflect.DeepEqual(current.ApplicationSettings, document.ApplicationSettings) &&
				decisions["application_settings"] == DecisionUseBackup {
				plan.configuration.ApplicationSettings = document.ApplicationSettings
			}
			plan.configuration.DeviceProfiles = mergeDeviceProfiles(
				current.DeviceProfiles, document.DeviceProfiles, decisions,
			)
		}
	}
	return plan, nil
}

func (service *Service) cleanupRetired(references []secretstore.Reference) error {
	if len(references) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), fileOperationTimeout)
	defer cancel()
	deleted := make([]secretstore.Reference, 0, len(references))
	pending := false
	for _, reference := range references {
		if err := service.secrets.Delete(ctx, reference); err != nil {
			pending = true
		} else {
			deleted = append(deleted, reference)
		}
	}
	if len(deleted) != 0 && service.owner.CompleteCleanup(ctx, deleted) != nil {
		pending = true
	}
	if pending {
		return ErrCleanupPending
	}
	return nil
}

func validMode(mode ImportMode) bool {
	return mode == ModeMerge || mode == ModeReplace || mode == ModeProvidersOnly
}

func settingConflict(key string, required bool) Conflict {
	return Conflict{
		Key: key, Kind: "settings", CurrentLabel: "current settings",
		BackupLabel: "backup settings", DecisionRequired: required,
	}
}

func configurationFromDocument(document Document) structuredprovider.RestorableConfiguration {
	configuration := structuredprovider.RestorableConfiguration{
		Definitions:         make([]structuredprovider.Definition, 0, len(document.Providers)),
		WebSettings:         document.WebSettings,
		ApplicationSettings: document.ApplicationSettings,
		DeviceProfiles:      configmodel.CloneDeviceProfiles(document.DeviceProfiles),
	}
	for _, provider := range providersInOrder(document) {
		configuration.Definitions = append(configuration.Definitions, cloneDefinition(provider.Definition))
	}
	return configuration
}

func cloneConfiguration(source structuredprovider.RestorableConfiguration) structuredprovider.RestorableConfiguration {
	clone := structuredprovider.RestorableConfiguration{
		Definitions:         make([]structuredprovider.Definition, len(source.Definitions)),
		WebSettings:         source.WebSettings,
		ApplicationSettings: source.ApplicationSettings,
		DeviceProfiles:      configmodel.CloneDeviceProfiles(source.DeviceProfiles),
	}
	for index := range source.Definitions {
		clone.Definitions[index] = cloneDefinition(source.Definitions[index])
	}
	return clone
}

func cloneDefinition(source structuredprovider.Definition) structuredprovider.Definition {
	clone := source
	clone.Request.Headers = append([]structuredprovider.Header(nil), source.Request.Headers...)
	clone.Request.Body = append([]byte(nil), source.Request.Body...)
	return clone
}

func providersInOrder(document Document) []Provider {
	byID := providerArchiveByID(document.Providers)
	ordered := make([]Provider, 0, len(document.ProviderOrder))
	for _, providerID := range document.ProviderOrder {
		ordered = append(ordered, byID[providerID])
	}
	return ordered
}

func providerArchiveByID(providers []Provider) map[string]Provider {
	result := make(map[string]Provider, len(providers))
	for _, provider := range providers {
		result[provider.Definition.ID] = provider
	}
	return result
}

func providerDefinitionsByID(
	definitions []structuredprovider.Definition,
) map[string]structuredprovider.Definition {
	result := make(map[string]structuredprovider.Definition, len(definitions))
	for _, definition := range definitions {
		result[definition.ID] = definition
	}
	return result
}

func providerOrderChoices(
	current []structuredprovider.Definition,
	backupOrder []string,
) ([]string, []string) {
	currentIDs := make([]string, 0, len(current))
	currentSet := make(map[string]struct{}, len(current))
	for _, definition := range current {
		currentIDs = append(currentIDs, definition.ID)
		currentSet[definition.ID] = struct{}{}
	}
	backupSet := make(map[string]struct{}, len(backupOrder))
	for _, providerID := range backupOrder {
		backupSet[providerID] = struct{}{}
	}
	keep := append([]string(nil), currentIDs...)
	for _, providerID := range backupOrder {
		if _, exists := currentSet[providerID]; !exists {
			keep = append(keep, providerID)
		}
	}
	useBackup := append([]string(nil), backupOrder...)
	for _, providerID := range currentIDs {
		if _, exists := backupSet[providerID]; !exists {
			useBackup = append(useBackup, providerID)
		}
	}
	return keep, useBackup
}

func definitionsInOrder(
	definitions []structuredprovider.Definition,
	order []string,
) []structuredprovider.Definition {
	byID := providerDefinitionsByID(definitions)
	result := make([]structuredprovider.Definition, 0, len(definitions))
	for _, providerID := range order {
		definition, exists := byID[providerID]
		if exists {
			result = append(result, cloneDefinition(definition))
		}
	}
	return result
}

func deviceProfilesByID(profiles []configmodel.DeviceProfile) map[string]configmodel.DeviceProfile {
	result := make(map[string]configmodel.DeviceProfile, len(profiles))
	for _, profile := range profiles {
		result[profile.DeviceID] = profile
	}
	return result
}

func mergeDeviceProfiles(
	current []configmodel.DeviceProfile,
	fromBackup []configmodel.DeviceProfile,
	decisions map[string]ConflictDecision,
) []configmodel.DeviceProfile {
	result := configmodel.CloneDeviceProfiles(current)
	indexes := make(map[string]int, len(result))
	for index, profile := range result {
		indexes[profile.DeviceID] = index
	}
	for _, profile := range fromBackup {
		index, exists := indexes[profile.DeviceID]
		if !exists {
			indexes[profile.DeviceID] = len(result)
			result = append(result, profile)
			continue
		}
		if reflect.DeepEqual(result[index], profile) ||
			decisions["device:"+profile.DeviceID] == DecisionUseBackup {
			result[index] = profile
		}
	}
	sort.SliceStable(result, func(left, right int) bool {
		return result[left].DeviceID < result[right].DeviceID
	})
	return result
}
