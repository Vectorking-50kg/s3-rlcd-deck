package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/configmodel"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/secretstore"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/structuredprovider"
)

func TestAgeArchiveRoundTripAndPrivacyBoundary(t *testing.T) {
	scryptWorkFactor = 10
	t.Cleanup(func() { scryptWorkFactor = 18 })
	const canary = "PRIVATE_BACKUP_PROVIDER_API_KEY_CANARY"
	const presetCanary = "PRIVATE_SERIAL_PRESET_CANARY"
	document := testDocument([]byte(canary))
	document.ApplicationSettings.SerialPresets = []configmodel.SerialPreset{{
		ID: "login", Name: "Login", Mode: configmodel.SerialPresetText,
		Payload: []byte(presetCanary), LineEnding: configmodel.SerialLineEndingCRLF,
	}}
	encrypted, err := Encrypt(context.Background(), &document, []byte("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte(canary)) || bytes.Contains(encrypted, []byte(presetCanary)) ||
		bytes.Contains(encrypted, []byte("AIHubMix")) {
		t.Fatal("age ciphertext exposed plaintext Provider state")
	}
	decoded, err := Decrypt(context.Background(), encrypted, []byte("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(decoded.Providers[0].Secrets[0].Value); got != canary {
		t.Fatalf("secret = %q", got)
	}
	if decoded.ProviderOrder[0] != "aihubmix" || !decoded.ApplicationSettings.HistoryEnabled ||
		decoded.DeviceProfiles[0].Board != "esp32-s3-rlcd-4.2" {
		t.Fatalf("decoded document = %#v", decoded)
	}
	if got := string(decoded.ApplicationSettings.SerialPresets[0].Payload); got != presetCanary {
		t.Fatalf("Serial Preset = %q", got)
	}
	secret := decoded.Providers[0].Secrets[0].Value
	presetSecret := decoded.ApplicationSettings.SerialPresets[0].Payload
	decoded.Destroy()
	if !allZero(secret) || !allZero(presetSecret) || decoded.Providers[0].Secrets != nil {
		t.Fatal("decrypted secret was not overwritten")
	}
}

func TestBackupSchemaMinorOneMigratesMinorZeroWithoutSerialPresets(t *testing.T) {
	if SchemaMinor != 1 {
		t.Fatalf("SchemaMinor = %d, want 1 for Serial Presets", SchemaMinor)
	}
	scryptWorkFactor = 10
	t.Cleanup(func() { scryptWorkFactor = 18 })
	document := testDocument([]byte("private-api-key"))
	document.SchemaVersion.Minor = 0
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err = json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy["application_settings"].(map[string]any), "serial_presets")
	raw, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decrypt(
		context.Background(), encryptRawArchive(t, raw, "correct horse battery staple"),
		[]byte("correct horse battery staple"),
	)
	if err != nil {
		t.Fatalf("minor-zero archive = %v", err)
	}
	defer decoded.Destroy()
	if decoded.ApplicationSettings.SerialPresets != nil {
		t.Fatalf("legacy Serial Presets = %#v, want nil", decoded.ApplicationSettings.SerialPresets)
	}
	document.SchemaVersion.Minor = 2
	if _, err = Encrypt(
		context.Background(), &document, []byte("correct horse battery staple"),
	); !errors.Is(err, ErrArchiveSchema) {
		t.Fatalf("future minor export error = %v", err)
	}
}

func TestAgeArchiveWrongPasswordTamperAndUnknownMajorFailClosed(t *testing.T) {
	scryptWorkFactor = 10
	t.Cleanup(func() { scryptWorkFactor = 18 })
	document := testDocument([]byte("private-api-key"))
	encrypted, err := Encrypt(context.Background(), &document, []byte("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Decrypt(context.Background(), encrypted, []byte("wrong passphrase value")); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("wrong password error = %v", err)
	}
	tampered := append([]byte(nil), encrypted...)
	tampered[len(tampered)-1] ^= 0x01
	if _, err = Decrypt(context.Background(), tampered, []byte("correct horse battery staple")); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("tamper error = %v", err)
	}
	document.SchemaVersion.Major = 2
	if _, err = Encrypt(context.Background(), &document, []byte("correct horse battery staple")); !errors.Is(err, ErrArchiveSchema) {
		t.Fatalf("unknown major export error = %v", err)
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	unknownMajor := encryptRawArchive(t, raw, "correct horse battery staple")
	if _, err = Decrypt(context.Background(), unknownMajor, []byte("correct horse battery staple")); !errors.Is(err, ErrArchiveSchema) {
		t.Fatalf("unknown major import error = %v", err)
	}
	document.SchemaVersion.Major = SchemaMajor
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unexpected":true}`)...)
	unknownField := encryptRawArchive(t, raw, "correct horse battery staple")
	if _, err = Decrypt(context.Background(), unknownField, []byte("correct horse battery staple")); !errors.Is(err, ErrArchiveSchema) {
		t.Fatalf("unknown field import error = %v", err)
	}
}

func encryptRawArchive(t *testing.T, plaintext []byte, passphrase string) []byte {
	t.Helper()
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		t.Fatal(err)
	}
	recipient.SetWorkFactor(10)
	var encrypted bytes.Buffer
	writer, err := age.Encrypt(&encrypted, recipient)
	if err == nil {
		_, err = writer.Write(plaintext)
	}
	if err == nil {
		err = writer.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	return encrypted.Bytes()
}

func TestAgeArchiveRejectsMalformedProviderOrderAndBounds(t *testing.T) {
	document := testDocument([]byte("private-api-key"))
	document.ProviderOrder = []string{"missing"}
	if _, err := Encrypt(context.Background(), &document, []byte("correct horse battery staple")); !errors.Is(err, ErrArchiveSchema) {
		t.Fatalf("invalid order error = %v", err)
	}
	if _, err := Decrypt(context.Background(), make([]byte, MaxEncryptedBytes+1), []byte("correct horse battery staple")); !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("large ciphertext error = %v", err)
	}
	if _, err := Encrypt(context.Background(), &document, []byte("short")); !errors.Is(err, ErrInvalidPassphrase) {
		t.Fatalf("short passphrase error = %v", err)
	}
	tooLargePlaintext := bytes.Repeat([]byte{'x'}, MaxPlaintextBytes+1)
	tooLargeArchive := encryptRawArchive(t, tooLargePlaintext, "correct horse battery staple")
	if _, err := Decrypt(context.Background(), tooLargeArchive, []byte("correct horse battery staple")); !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("large plaintext error = %v", err)
	}
}

func TestAgeArchiveAcceptsEveryProviderCredentialSize(t *testing.T) {
	scryptWorkFactor = 10
	t.Cleanup(func() { scryptWorkFactor = 18 })
	maximum := bytes.Repeat([]byte{'s'}, secretstore.MaximumSecretBytes)
	document := testDocument(maximum)
	encrypted, err := Encrypt(
		context.Background(), &document, []byte("correct horse battery staple"),
	)
	if err != nil {
		t.Fatalf("maximum credential export error = %v", err)
	}
	decoded, err := Decrypt(
		context.Background(), encrypted, []byte("correct horse battery staple"),
	)
	if err != nil {
		t.Fatalf("maximum credential import error = %v", err)
	}
	if got := len(decoded.Providers[0].Secrets[0].Value); got != len(maximum) {
		t.Fatalf("credential bytes = %d, want %d", got, len(maximum))
	}
	decoded.Destroy()
	document.Providers[0].Secrets[0].Value = append(maximum, 'x')
	if _, err = Encrypt(
		context.Background(), &document, []byte("correct horse battery staple"),
	); !errors.Is(err, ErrArchiveSchema) {
		t.Fatalf("oversized credential error = %v", err)
	}
}

func TestAgeArchiveBoundsConcurrentScryptWorkAndLetsWaitersCancel(t *testing.T) {
	scryptOperations <- struct{}{}
	t.Cleanup(releaseScrypt)
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(10*time.Millisecond, cancel)
	document := testDocument([]byte("private-api-key"))
	if _, err := Encrypt(
		ctx, &document, []byte("correct horse battery staple"),
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting Encrypt error = %v", err)
	}
	timer.Stop()
	ctx, cancel = context.WithCancel(context.Background())
	timer = time.AfterFunc(10*time.Millisecond, cancel)
	if _, err := Decrypt(
		ctx, []byte("not-an-age-document"), []byte("correct horse battery staple"),
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting Decrypt error = %v", err)
	}
	timer.Stop()
}

func testDocument(secret []byte) Document {
	definition := structuredprovider.Templates()[0].Definition
	definition.RequestTimeoutSeconds = 10
	definition.MaximumResponseBytes = structuredprovider.DefaultMaximumResponse
	definition.Mapping.BalanceDivisor = 500_000
	return Document{
		Type:          ArchiveType,
		SchemaVersion: SchemaVersion{Major: SchemaMajor, Minor: SchemaMinor},
		ExportedAt:    time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Providers: []Provider{{
			Definition: definition,
			Secrets:    []ProviderSecret{{HeaderIndex: 0, Value: append([]byte(nil), secret...)}},
		}},
		ProviderOrder: []string{"aihubmix"},
		WebSettings: WebSettings{
			ManagementAddress: configmodel.DefaultManagementAddress,
		},
		ApplicationSettings: ApplicationSettings{HistoryEnabled: true},
		DeviceProfiles: []DeviceProfile{{
			DeviceID:        "deck-12345678",
			FirmwareVersion: "0.2.0-dev",
			Board:           "esp32-s3-rlcd-4.2",
			Capabilities:    []string{"display", "serial"},
			LastSeenUTC:     "2026-08-15T09:59:00Z",
		}},
	}
}

func allZero(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}
