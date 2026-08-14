// Package backup owns the password-encrypted, versioned Companion backup
// archive. It deliberately works on an explicit privacy-safe document rather
// than serializing live stores, whose files contain unrelated trust, history,
// session, and cleanup state.
package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"filippo.io/age"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/configmodel"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/secretstore"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/structuredprovider"
)

const (
	ArchiveType                   = "s3-rlcd-deck.backup"
	SchemaMajor            uint16 = 1
	SchemaMinor            uint16 = 0
	MaxPlaintextBytes             = 4 << 20
	MaxEncryptedBytes             = 8 << 20
	minimumPassphraseBytes        = 12
	maximumPassphraseBytes        = 1024
)

var (
	ErrInvalidPassphrase = errors.New("backup passphrase is invalid")
	ErrDecrypt           = errors.New("backup cannot be decrypted")
	ErrArchiveSchema     = errors.New("backup schema is unsupported or malformed")
	ErrArchiveTooLarge   = errors.New("backup exceeds the size limit")
)

var scryptWorkFactor = 18

// age's default scrypt work factor deliberately consumes substantial memory.
// One process-wide slot prevents concurrent authenticated requests from
// multiplying that bound while still allowing a waiting request to cancel.
var scryptOperations = make(chan struct{}, 1)

type SchemaVersion struct {
	Major uint16 `json:"major"`
	Minor uint16 `json:"minor"`
}

type WebSettings = configmodel.WebSettings

type ApplicationSettings = configmodel.ApplicationSettings

type DeviceProfile = configmodel.DeviceProfile

type ProviderSecret struct {
	HeaderIndex int    `json:"header_index"`
	Value       []byte `json:"value"`
}

// Provider contains a draft Definition whose SecretReference fields are all
// empty. Secrets are bound by header index only while this decrypted Document
// is alive.
type Provider struct {
	Definition structuredprovider.Definition `json:"definition"`
	Secrets    []ProviderSecret              `json:"secrets"`
}

type Document struct {
	Type                string              `json:"type"`
	SchemaVersion       SchemaVersion       `json:"schema_version"`
	ExportedAt          string              `json:"exported_at"`
	Providers           []Provider          `json:"providers"`
	ProviderOrder       []string            `json:"provider_order"`
	WebSettings         WebSettings         `json:"web_settings"`
	ApplicationSettings ApplicationSettings `json:"application_settings"`
	DeviceProfiles      []DeviceProfile     `json:"device_profiles"`
}

func Encrypt(ctx context.Context, document *Document, passphrase []byte) ([]byte, error) {
	if ctx == nil || document == nil || !validPassphrase(passphrase) {
		return nil, ErrInvalidPassphrase
	}
	if err := validateDocument(*document); err != nil {
		return nil, err
	}
	plaintext, err := json.Marshal(document)
	if err != nil {
		overwrite(plaintext)
		return nil, ErrArchiveSchema
	}
	if len(plaintext) > MaxPlaintextBytes {
		overwrite(plaintext)
		return nil, ErrArchiveTooLarge
	}
	defer overwrite(plaintext)
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = acquireScrypt(ctx); err != nil {
		return nil, err
	}
	defer releaseScrypt()
	recipient, err := age.NewScryptRecipient(string(passphrase))
	if err != nil {
		return nil, ErrInvalidPassphrase
	}
	recipient.SetWorkFactor(scryptWorkFactor)
	var encrypted bytes.Buffer
	writer, err := age.Encrypt(&encrypted, recipient)
	if err == nil {
		_, err = writer.Write(plaintext)
	}
	if err == nil {
		err = writer.Close()
	}
	if err != nil || encrypted.Len() > MaxEncryptedBytes {
		overwrite(encrypted.Bytes())
		if encrypted.Len() > MaxEncryptedBytes {
			return nil, ErrArchiveTooLarge
		}
		return nil, ErrDecrypt
	}
	if err = ctx.Err(); err != nil {
		overwrite(encrypted.Bytes())
		return nil, err
	}
	return append([]byte(nil), encrypted.Bytes()...), nil
}

func Decrypt(ctx context.Context, encrypted []byte, passphrase []byte) (*Document, error) {
	if ctx == nil || !validPassphrase(passphrase) {
		return nil, ErrInvalidPassphrase
	}
	if len(encrypted) == 0 {
		return nil, ErrDecrypt
	}
	if len(encrypted) > MaxEncryptedBytes {
		return nil, ErrArchiveTooLarge
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := acquireScrypt(ctx); err != nil {
		return nil, err
	}
	defer releaseScrypt()
	identity, err := age.NewScryptIdentity(string(passphrase))
	if err != nil {
		return nil, ErrInvalidPassphrase
	}
	identity.SetMaxWorkFactor(scryptWorkFactor)
	reader, err := age.Decrypt(bytes.NewReader(encrypted), identity)
	if err != nil {
		return nil, ErrDecrypt
	}
	plaintext, err := io.ReadAll(io.LimitReader(reader, MaxPlaintextBytes+1))
	if err != nil {
		overwrite(plaintext)
		return nil, ErrDecrypt
	}
	defer overwrite(plaintext)
	if len(plaintext) > MaxPlaintextBytes {
		return nil, ErrArchiveTooLarge
	}
	var document Document
	if err = protocol.DecodeStrictDocumentLimit(plaintext, MaxPlaintextBytes, &document); err != nil {
		return nil, ErrArchiveSchema
	}
	if err = validateDocument(document); err != nil {
		document.Destroy()
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		document.Destroy()
		return nil, err
	}
	return &document, nil
}

// Destroy overwrites every credential owned by the decrypted document. The
// caller must invoke it as soon as preview or import staging finishes.
func (document *Document) Destroy() {
	if document == nil {
		return
	}
	for providerIndex := range document.Providers {
		for secretIndex := range document.Providers[providerIndex].Secrets {
			secret := &document.Providers[providerIndex].Secrets[secretIndex]
			overwrite(secret.Value)
			secret.Value = nil
		}
		document.Providers[providerIndex].Secrets = nil
	}
}

func validateDocument(document Document) error {
	if document.Type != ArchiveType || document.SchemaVersion.Major != SchemaMajor ||
		document.SchemaVersion.Minor != SchemaMinor || document.Providers == nil ||
		document.ProviderOrder == nil || document.DeviceProfiles == nil ||
		len(document.Providers) > 6 ||
		len(document.DeviceProfiles) > configmodel.MaximumDeviceProfiles ||
		!configmodel.CanonicalUTC(document.ExportedAt) ||
		!configmodel.ValidateWebSettings(document.WebSettings) {
		return ErrArchiveSchema
	}
	providerIDs := make(map[string]struct{}, len(document.Providers))
	for index := range document.Providers {
		provider := &document.Providers[index]
		normalized, err := structuredprovider.NormalizeBackupDefinition(provider.Definition)
		if err != nil || !definitionsEqual(normalized, provider.Definition) ||
			len(provider.Secrets) != len(provider.Definition.Request.Headers) {
			return ErrArchiveSchema
		}
		if _, duplicate := providerIDs[provider.Definition.ID]; duplicate {
			return ErrArchiveSchema
		}
		providerIDs[provider.Definition.ID] = struct{}{}
		for secretIndex, secret := range provider.Secrets {
			if secret.HeaderIndex != secretIndex || len(secret.Value) == 0 ||
				len(secret.Value) > secretstore.MaximumSecretBytes {
				return ErrArchiveSchema
			}
		}
	}
	if len(document.ProviderOrder) != len(providerIDs) {
		return ErrArchiveSchema
	}
	ordered := make(map[string]struct{}, len(document.ProviderOrder))
	for _, providerID := range document.ProviderOrder {
		if _, exists := providerIDs[providerID]; !exists {
			return ErrArchiveSchema
		}
		if _, duplicate := ordered[providerID]; duplicate {
			return ErrArchiveSchema
		}
		ordered[providerID] = struct{}{}
	}
	deviceIDs := make(map[string]struct{}, len(document.DeviceProfiles))
	for index := range document.DeviceProfiles {
		profile := document.DeviceProfiles[index]
		if !configmodel.ValidateDeviceProfile(profile) {
			return ErrArchiveSchema
		}
		if _, duplicate := deviceIDs[profile.DeviceID]; duplicate {
			return ErrArchiveSchema
		}
		deviceIDs[profile.DeviceID] = struct{}{}
	}
	return nil
}

func validPassphrase(value []byte) bool {
	return len(value) >= minimumPassphraseBytes && len(value) <= maximumPassphraseBytes &&
		utf8.Valid(value)
}

func acquireScrypt(ctx context.Context) error {
	select {
	case scryptOperations <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseScrypt() {
	<-scryptOperations
}

func definitionsEqual(left, right structuredprovider.Definition) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func overwrite(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
