package structuredprovider

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/configmodel"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/secretstore"
)

func TestDefinitionStoreMigratesV1AndPreservesExplicitProviderOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "structured-providers.json")
	legacy := definitionStoreStateV1{
		SchemaVersion:        1,
		Definitions:          []Definition{},
		PendingSecretDeletes: []secretstore.Reference{},
	}
	contents, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if committed, replaceErr := protectedfile.Replace(path, contents); !committed || replaceErr != nil {
		t.Fatalf("seed legacy store committed=%v err=%v", committed, replaceErr)
	}
	owner, err := OpenDefinitionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	secrets := newTransactionSecretStore()
	templates := Templates()
	for _, templateIndex := range []int{1, 0} {
		if _, err = CommitDefinition(
			context.Background(), nil, templates[templateIndex].Definition,
			[]SecretBinding{{HeaderIndex: 0, Value: []byte("provider-key")}},
			secrets, owner,
		); err != nil {
			t.Fatal(err)
		}
	}
	configuration, err := owner.Configuration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(configuration.Definitions) != 2 || configuration.Definitions[0].ID != "deepseek" ||
		configuration.Definitions[1].ID != "aihubmix" ||
		!configuration.ApplicationSettings.HistoryEnabled ||
		configuration.DeviceProfiles == nil {
		t.Fatalf("migrated configuration = %#v", configuration)
	}
	if err = owner.Close(); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err = json.Unmarshal(stored, &header); err != nil || header.SchemaVersion != 3 {
		t.Fatalf("persisted schema = %d, %v", header.SchemaVersion, err)
	}
}

func TestDefinitionStoreMigratesV2WithoutInventingSerialPresets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "structured-providers.json")
	legacy := definitionStoreStateV2{
		SchemaVersion:        2,
		Definitions:          []Definition{},
		WebSettings:          configmodel.WebSettings{ManagementAddress: configmodel.DefaultManagementAddress},
		ApplicationSettings:  definitionStoreApplicationSettingsV2{HistoryEnabled: false},
		DeviceProfiles:       []configmodel.DeviceProfile{},
		PendingSecretDeletes: []secretstore.Reference{},
	}
	contents, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if committed, replaceErr := protectedfile.Replace(path, contents); !committed || replaceErr != nil {
		t.Fatalf("seed v2 store committed=%v err=%v", committed, replaceErr)
	}
	owner, err := OpenDefinitionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	settings, pending, err := owner.PendingApplicationSettings(context.Background())
	if err != nil || pending || settings.HistoryEnabled || settings.SerialPresets != nil {
		t.Fatalf("migrated settings=%#v pending=%v err=%v", settings, pending, err)
	}
	if err = owner.UpdateHistoryEnabled(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err = owner.Close(); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err = json.Unmarshal(stored, &header); err != nil || header.SchemaVersion != 3 {
		t.Fatalf("persisted schema=%d err=%v", header.SchemaVersion, err)
	}
}

func TestReplaceConfigurationPublishesAllNonSecretStateOnce(t *testing.T) {
	owner, err := OpenDefinitionStore(filepath.Join(t.TempDir(), "structured-providers.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	secrets := newTransactionSecretStore()
	currentDefinition, err := CommitDefinition(
		context.Background(), nil, Templates()[0].Definition,
		[]SecretBinding{{HeaderIndex: 0, Value: []byte("old-key")}}, secrets, owner,
	)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := owner.Configuration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	newReference := secretstore.Reference("secret-77777777777777777777777777777777")
	secrets.values[newReference] = []byte("new-key")
	if err = owner.StageCleanup(context.Background(), []secretstore.Reference{newReference}); err != nil {
		t.Fatal(err)
	}
	replacementDefinition := Templates()[1].Definition
	normalized, err := NormalizeBackupDefinition(replacementDefinition)
	if err != nil {
		t.Fatal(err)
	}
	replacementDefinition = normalized
	replacementDefinition.Request.Headers[0].SecretReference = newReference
	replacement := RestorableConfiguration{
		Definitions: []Definition{replacementDefinition},
		WebSettings: configmodel.WebSettings{
			ManagementAddress: "0.0.0.0:7777", AllowLAN: true,
			AllowedOrigin: "https://companion.example.test",
		},
		ApplicationSettings: configmodel.ApplicationSettings{HistoryEnabled: false},
		DeviceProfiles: []configmodel.DeviceProfile{{
			DeviceID:        "deck-12345678",
			FirmwareVersion: "0.2.0-dev",
			Board:           "esp32-s3-rlcd-4.2",
			Capabilities:    []string{"display", "serial"},
			LastSeenUTC:     "2026-08-15T12:00:00Z",
		}},
	}
	result, err := owner.ReplaceConfiguration(
		context.Background(), expected, replacement, []secretstore.Reference{newReference},
	)
	if err != nil || !result.Committed ||
		!reflect.DeepEqual(result.Retired, []secretstore.Reference{currentDefinition.Request.Headers[0].SecretReference}) {
		t.Fatalf("replace result=%#v err=%v", result, err)
	}
	actual, err := owner.Configuration(context.Background())
	if err != nil || !reflect.DeepEqual(actual, replacement) {
		t.Fatalf("replacement=%#v err=%v", actual, err)
	}
	settings, pending, err := owner.PendingApplicationSettings(context.Background())
	if err != nil || !pending || settings.HistoryEnabled {
		t.Fatalf("pending application settings=%#v pending=%v err=%v", settings, pending, err)
	}
	if err = owner.UpdateApplicationSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	_, pending, err = owner.PendingApplicationSettings(context.Background())
	if err != nil || pending {
		t.Fatalf("application settings acknowledgement pending=%v err=%v", pending, err)
	}
	if _, err = owner.ReplaceConfiguration(context.Background(), expected, expected, nil); err == nil {
		t.Fatal("stale expected configuration was accepted")
	}
	if err = owner.cleanupReferences(context.Background(), secrets, result.Retired); err != nil {
		t.Fatal(err)
	}
	if _, exists := secrets.values[currentDefinition.Request.Headers[0].SecretReference]; exists {
		t.Fatal("retired secret survived cleanup")
	}
}

func TestSerialPresetsPersistWithoutBeingOverwrittenByHistorySettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "structured-providers.json")
	owner, err := OpenDefinitionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	presets := []configmodel.SerialPreset{{
		ID: "status", Name: "Status", Mode: configmodel.SerialPresetText,
		Payload:    []byte("status --token PRIVATE_PRESET"),
		LineEnding: configmodel.SerialLineEndingCRLF,
	}}
	if err = owner.UpdateSerialPresets(context.Background(), presets); err != nil {
		t.Fatal(err)
	}
	presets[0].Payload[0] = 'X'
	if err = owner.UpdateHistoryEnabled(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	loaded, err := owner.SerialPresets(context.Background())
	if err != nil || string(loaded[0].Payload) != "status --token PRIVATE_PRESET" {
		t.Fatalf("loaded presets=%#v err=%v", loaded, err)
	}
	configmodel.DestroySerialPresets(loaded)
	if err = owner.Close(); err != nil {
		t.Fatal(err)
	}
	owner, err = OpenDefinitionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	loaded, err = owner.SerialPresets(context.Background())
	if err != nil || string(loaded[0].Payload) != "status --token PRIVATE_PRESET" {
		t.Fatalf("reopened presets=%#v err=%v", loaded, err)
	}
	configmodel.DestroySerialPresets(loaded)
}
