package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
)

const currentSchemaVersion = 2

const createSchemaV2 = `
CREATE TABLE IF NOT EXISTS provider_hours (
    provider_id TEXT NOT NULL,
    hour_utc_ms INTEGER NOT NULL,
    observed_at_utc_ms INTEGER NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('ok', 'degraded', 'unavailable')),
    error_code TEXT,
    balance_amount_micros INTEGER,
    balance_currency TEXT,
    token_input INTEGER,
    token_cached_input INTEGER,
    token_output INTEGER,
    token_reasoning INTEGER,
    token_total INTEGER,
    PRIMARY KEY (provider_id, hour_utc_ms),
    CHECK ((balance_amount_micros IS NULL) = (balance_currency IS NULL))
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS quota_hours (
    provider_id TEXT NOT NULL,
    hour_utc_ms INTEGER NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0 AND ordinal < 4),
    name TEXT NOT NULL,
    used_basis_points INTEGER,
    remaining_basis_points INTEGER,
    window_minutes INTEGER,
    resets_at_utc_ms INTEGER,
    PRIMARY KEY (provider_id, hour_utc_ms, ordinal),
    FOREIGN KEY (provider_id, hour_utc_ms)
        REFERENCES provider_hours(provider_id, hour_utc_ms) ON DELETE CASCADE
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS provider_hours_by_time
    ON provider_hours(hour_utc_ms, provider_id);
CREATE TABLE IF NOT EXISTS history_settings (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1))
);
INSERT OR IGNORE INTO history_settings(singleton, enabled) VALUES (1, 1);
PRAGMA user_version = 2;
`

func initializeSchema(ctx context.Context, database *sql.DB, path string) error {
	if err := quickCheck(ctx, database); err != nil {
		return err
	}
	var version int
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read Provider history schema: %w", err)
	}
	if version < 0 || version > currentSchemaVersion {
		return fmt.Errorf("%w: unsupported Provider history schema version %d", ErrMigration, version)
	}
	switch version {
	case 0:
		var tables int
		if err := database.QueryRowContext(ctx, `
SELECT COUNT(*) FROM sqlite_schema
WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&tables); err != nil {
			return fmt.Errorf("inspect Provider history schema: %w", err)
		}
		if tables != 0 {
			return fmt.Errorf("%w: unversioned database is not empty", ErrMigration)
		}
		if _, err := database.ExecContext(ctx, createSchemaV2); err != nil {
			return fmt.Errorf("create Provider history schema: %w", err)
		}
	case 1:
		if err := createMigrationBackup(ctx, database, path, version); err != nil {
			return fmt.Errorf("%w: create schema backup", ErrMigration)
		}
		if err := migrateVersionOne(ctx, database); err != nil {
			return fmt.Errorf("%w: upgrade schema version 1", ErrMigration)
		}
	}
	return quickCheck(ctx, database)
}

func quickCheck(ctx context.Context, database *sql.DB) error {
	var result string
	if err := database.QueryRowContext(ctx, "PRAGMA quick_check(1)").Scan(&result); err != nil {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if result != "ok" {
		return fmt.Errorf("%w: integrity check failed", ErrCorrupt)
	}
	return nil
}

func migrateVersionOne(ctx context.Context, database *sql.DB) error {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	if _, err = transaction.ExecContext(ctx, "ALTER TABLE provider_hours ADD COLUMN error_code TEXT"); err != nil {
		return err
	}
	if _, err = transaction.ExecContext(ctx, `
CREATE INDEX provider_hours_by_time ON provider_hours(hour_utc_ms, provider_id)`); err != nil {
		return err
	}
	if _, err = transaction.ExecContext(ctx, "PRAGMA user_version = 2"); err != nil {
		return err
	}
	return transaction.Commit()
}

func createMigrationBackup(ctx context.Context, database *sql.DB, path string, version int) error {
	backupPath := fmt.Sprintf("%s.schema-v%d.bak", path, version)
	temporaryPath := backupPath + ".tmp"
	if err := os.Remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := database.ExecContext(ctx, "VACUUM INTO ?", temporaryPath); err != nil {
		return err
	}
	if err := protectedfile.EnsurePrivateFile(temporaryPath); err != nil {
		return err
	}
	file, err := os.OpenFile(temporaryPath, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if err = errors.Join(syncErr, closeErr); err != nil {
		return err
	}
	if err = commitBackupFile(temporaryPath, backupPath); err != nil {
		return err
	}
	removeTemporary = false
	if err = protectedfile.EnsurePrivateFile(backupPath); err != nil {
		return err
	}
	return verifyMigrationBackup(ctx, backupPath, version)
}

func verifyMigrationBackup(ctx context.Context, path string, version int) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("migration backup is not a regular file")
	}
	if err = protectedfile.VerifyPrivate(path); err != nil {
		return err
	}
	database, err := sql.Open("sqlite", readOnlyDSN(filepath.Clean(path)))
	if err != nil {
		return err
	}
	defer database.Close()
	if err = quickCheck(ctx, database); err != nil {
		return err
	}
	var actual int
	if err = database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&actual); err != nil {
		return err
	}
	if actual != version {
		return fmt.Errorf("migration backup schema version is %d", actual)
	}
	return nil
}

func isCorruptError(err error) bool {
	return errors.Is(err, ErrCorrupt)
}
