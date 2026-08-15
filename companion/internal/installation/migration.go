package installation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/deviceidentity"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/history"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairing"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/structuredprovider"
)

const maximumMigrationFileBytes = 64 << 20

var migrationFiles = []string{
	"pairing.json",
	"device-hub-identity.json",
	"structured-providers.json",
	"provider-history.sqlite3",
	"provider-history.sqlite3-wal",
	"provider-history.sqlite3-shm",
}

type migrationEntry struct {
	Path    string `json:"path"`
	Existed bool   `json:"existed"`
	Size    int    `json:"size"`
	SHA256  string `json:"sha256,omitempty"`
}

type migrationManifest struct {
	SchemaVersion uint32           `json:"schema_version"`
	CreatedUTC    string           `json:"created_utc"`
	Entries       []migrationEntry `json:"entries"`
}

func createMigrationSnapshot(
	ctx context.Context,
	dataDirectory string,
	backupRoot string,
	now time.Time,
	commit string,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := protectedfile.EnsurePrivateDirectory(backupRoot); err != nil {
		return "", err
	}
	base := now.Format("20060102T150405.000000000Z") + "-" + commit
	var backupPath string
	for sequence := 0; sequence < 1000; sequence++ {
		backupPath = filepath.Join(backupRoot, fmt.Sprintf("%s-%03d", base, sequence))
		err := os.Mkdir(backupPath, 0o700)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
		backupPath = ""
	}
	if backupPath == "" {
		return "", errors.New("migration snapshot namespace is exhausted")
	}
	removeBackup := true
	defer func() {
		if removeBackup {
			_ = os.RemoveAll(backupPath)
		}
	}()
	manifest := migrationManifest{
		SchemaVersion: StateSchemaVersion,
		CreatedUTC:    now.Format(time.RFC3339Nano),
		Entries:       make([]migrationEntry, 0, len(migrationFiles)),
	}
	for _, relative := range migrationFiles {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		source := filepath.Join(dataDirectory, relative)
		entry := migrationEntry{Path: relative}
		info, err := os.Lstat(source)
		if errors.Is(err, os.ErrNotExist) {
			manifest.Entries = append(manifest.Entries, entry)
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
			info.Size() < 0 || info.Size() > maximumMigrationFileBytes ||
			protectedfile.VerifyPrivate(source) != nil {
			return "", errors.New("migration source is not a bounded private regular file")
		}
		contents, err := protectedfile.Read(source, maximumMigrationFileBytes)
		if err != nil {
			return "", err
		}
		digest := sha256.Sum256(contents)
		entry.Existed = true
		entry.Size = len(contents)
		entry.SHA256 = hex.EncodeToString(digest[:])
		if _, err = protectedfile.Replace(filepath.Join(backupPath, relative), contents); err != nil {
			clear(contents)
			return "", err
		}
		clear(contents)
		manifest.Entries = append(manifest.Entries, entry)
	}
	if err := writePrivateJSON(filepath.Join(backupPath, "manifest.json"), manifest); err != nil {
		return "", err
	}
	removeBackup = false
	return backupPath, nil
}

func restoreMigrationSnapshot(ctx context.Context, dataDirectory, backupPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	document, err := protectedfile.Read(filepath.Join(backupPath, "manifest.json"), 64<<10)
	if err != nil {
		return err
	}
	defer clear(document)
	var manifest migrationManifest
	if decodeStrict(document, &manifest) != nil || !validMigrationManifest(manifest) {
		return errors.New("invalid migration snapshot manifest")
	}
	for _, entry := range manifest.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		target := filepath.Join(dataDirectory, entry.Path)
		if !entry.Existed {
			if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		contents, err := protectedfile.Read(filepath.Join(backupPath, entry.Path), maximumMigrationFileBytes)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(contents)
		if len(contents) != entry.Size || hex.EncodeToString(digest[:]) != entry.SHA256 {
			clear(contents)
			return errors.New("migration snapshot hash mismatch")
		}
		if _, err = protectedfile.Replace(target, contents); err != nil {
			clear(contents)
			return err
		}
		clear(contents)
	}
	return nil
}

func validMigrationManifest(manifest migrationManifest) bool {
	if manifest.SchemaVersion != StateSchemaVersion || len(manifest.Entries) != len(migrationFiles) {
		return false
	}
	if parsed, err := time.Parse(time.RFC3339Nano, manifest.CreatedUTC); err != nil ||
		parsed.Location() != time.UTC || parsed.Format(time.RFC3339Nano) != manifest.CreatedUTC {
		return false
	}
	for index, entry := range manifest.Entries {
		if entry.Path != migrationFiles[index] || entry.Size < 0 || entry.Size > maximumMigrationFileBytes {
			return false
		}
		if entry.Existed {
			decoded, err := hex.DecodeString(entry.SHA256)
			if err != nil || len(decoded) != sha256.Size {
				return false
			}
		} else if entry.Size != 0 || entry.SHA256 != "" {
			return false
		}
	}
	return true
}

func migrateData(ctx context.Context, dataDirectory string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	pairingExists, err := regularPathExists(filepath.Join(dataDirectory, "pairing.json"))
	if err != nil {
		return err
	}
	if pairingExists {
		store, err := pairing.OpenFileStore(filepath.Join(dataDirectory, "pairing.json"))
		if err != nil {
			return err
		}
		if err = store.Close(); err != nil {
			return err
		}
	}
	identityExists, err := regularPathExists(filepath.Join(dataDirectory, "device-hub-identity.json"))
	if err != nil {
		return err
	}
	if identityExists {
		if _, err := deviceidentity.LoadOrCreate(filepath.Join(dataDirectory, "device-hub-identity.json")); err != nil {
			return err
		}
	}
	providerExists, err := regularPathExists(filepath.Join(dataDirectory, "structured-providers.json"))
	if err != nil {
		return err
	}
	if providerExists {
		store, err := structuredprovider.OpenConfigurationStore(
			filepath.Join(dataDirectory, "structured-providers.json"),
		)
		if err != nil {
			return err
		}
		if err = store.Close(); err != nil {
			return err
		}
	}
	historyExists, err := regularPathExists(filepath.Join(dataDirectory, "provider-history.sqlite3"))
	if err != nil {
		return err
	}
	if historyExists {
		store, err := history.Open(ctx, history.Config{
			Path: filepath.Join(dataDirectory, "provider-history.sqlite3"),
		})
		if err != nil {
			return err
		}
		closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = store.Close(closeContext)
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

func regularPathExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, errors.New("migration input must be a regular non-symlink file")
	}
	return true, nil
}
