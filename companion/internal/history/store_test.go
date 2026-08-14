package history_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/history"
	_ "modernc.org/sqlite"
)

func TestCaptureKeepsLatestNormalizedSamplePerUTCHour(t *testing.T) {
	store, err := history.Open(context.Background(), history.Config{
		Path: filepath.Join(t.TempDir(), "provider-history.sqlite3"),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close(context.Background())

	firstObserved := time.Date(2026, 8, 15, 10, 5, 0, 0, time.UTC)
	secondObserved := firstObserved.Add(40 * time.Minute)
	first := providerAt("codex", 2500, 7_000_000)
	second := providerAt("codex", 4300, 5_500_000)
	if err = store.Capture(context.Background(), first, firstObserved); err != nil {
		t.Fatalf("Capture(first) error = %v", err)
	}
	if err = store.Capture(context.Background(), second, secondObserved); err != nil {
		t.Fatalf("Capture(second) error = %v", err)
	}
	if err = store.Flush(context.Background()); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	records, err := store.Query(context.Background(), history.Query{
		From:  firstObserved.Truncate(time.Hour),
		Until: firstObserved.Truncate(time.Hour).Add(time.Hour),
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("len(records) = %d, want 1", len(records))
	}
	record := records[0]
	if record.ProviderID != "codex" || !record.ObservedAt.Equal(secondObserved) {
		t.Fatalf("record identity = %+v", record)
	}
	if len(record.Windows) != 1 || record.Windows[0].UsedBasisPoints == nil ||
		*record.Windows[0].UsedBasisPoints != 4300 {
		t.Fatalf("record windows = %+v", record.Windows)
	}
	if record.Balance == nil || record.Balance.AmountMicros != 5_500_000 {
		t.Fatalf("record balance = %+v", record.Balance)
	}
}

func TestHistoryCanBeDisabledReenabledAndCleared(t *testing.T) {
	store, err := history.Open(context.Background(), history.Config{
		Path: filepath.Join(t.TempDir(), "provider-history.sqlite3"),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close(context.Background())

	observedAt := time.Date(2026, 8, 15, 11, 0, 0, 0, time.UTC)
	if err = store.SetEnabled(context.Background(), false); err != nil {
		t.Fatalf("SetEnabled(false) error = %v", err)
	}
	if err = store.Capture(context.Background(), providerAt("codex", 1000, 2_000_000), observedAt); err != nil {
		t.Fatalf("Capture(disabled) error = %v", err)
	}
	if err = store.Flush(context.Background()); err != nil {
		t.Fatalf("Flush(disabled) error = %v", err)
	}
	if records := queryDay(t, store, observedAt); len(records) != 0 {
		t.Fatalf("disabled history records = %+v", records)
	}

	if err = store.SetEnabled(context.Background(), true); err != nil {
		t.Fatalf("SetEnabled(true) error = %v", err)
	}
	if err = store.Capture(context.Background(), providerAt("codex", 1000, 2_000_000), observedAt); err != nil {
		t.Fatalf("Capture(enabled) error = %v", err)
	}
	if err = store.Flush(context.Background()); err != nil {
		t.Fatalf("Flush(enabled) error = %v", err)
	}
	if records := queryDay(t, store, observedAt); len(records) != 1 {
		t.Fatalf("enabled history len = %d, want 1", len(records))
	}

	if err = store.Clear(context.Background()); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	if records := queryDay(t, store, observedAt); len(records) != 0 {
		t.Fatalf("cleared history records = %+v", records)
	}
}

func TestDisabledSettingSurvivesRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "provider-history.sqlite3")
	store, err := history.Open(context.Background(), history.Config{Path: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetEnabled(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	store, err = history.Open(context.Background(), history.Config{Path: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	if store.Enabled() || store.Settings().RetentionDays != 90 {
		t.Fatalf("reopened settings = %+v", store.Settings())
	}
}

func TestExportCSVNeutralizesSpreadsheetFormulas(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "provider-history.sqlite3")
	store, err := history.Open(context.Background(), history.Config{
		Path: databasePath,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	observedAt := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	provider := providerAt("codex", 1000, 2_000_000)
	if err = store.Capture(context.Background(), provider, observedAt); err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	if err = store.Flush(context.Background()); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	if err = store.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	// Simulate a legacy/tampered row. Export is a trust boundary even if current
	// normalized DTO validation already rejects formula-looking identifiers.
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec("UPDATE quota_hours SET name = '=1+1'"); err != nil {
		t.Fatalf("seed hostile history row: %v", err)
	}
	if err = database.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = history.Open(context.Background(), history.Config{Path: databasePath})
	if err != nil {
		t.Fatalf("reopen history: %v", err)
	}
	defer store.Close(context.Background())

	var exported bytes.Buffer
	err = store.ExportCSV(context.Background(), &exported, history.Query{
		From:  observedAt.Add(-time.Hour),
		Until: observedAt.Add(time.Hour),
		Limit: 100,
	})
	if err != nil {
		t.Fatalf("ExportCSV() error = %v", err)
	}
	if got := exported.String(); !bytes.Contains(exported.Bytes(), []byte("'=1+1")) ||
		bytes.Contains(exported.Bytes(), []byte(",=1+1,")) {
		t.Fatalf("CSV formula was not neutralized:\n%s", got)
	}
}

func TestOpenBacksUpAndMigratesVersionOneHistory(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "provider-history.sqlite3")
	hour := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	seedVersionOneHistory(t, databasePath, hour)

	store, err := history.Open(context.Background(), history.Config{Path: databasePath})
	if err != nil {
		t.Fatalf("Open(v1) error = %v", err)
	}
	defer store.Close(context.Background())
	records, err := store.Query(context.Background(), history.Query{
		From: hour.Add(-time.Hour), Until: hour.Add(time.Hour), Limit: 10,
	})
	if err != nil {
		t.Fatalf("Query(migrated) error = %v", err)
	}
	if len(records) != 1 || records[0].ProviderID != "codex" || records[0].ErrorCode != nil {
		t.Fatalf("migrated records = %+v", records)
	}

	backupPath := databasePath + ".schema-v1.bak"
	if info, statErr := os.Stat(backupPath); statErr != nil || !info.Mode().IsRegular() {
		t.Fatalf("migration backup = %#v, error = %v", info, statErr)
	}
	backup, err := sql.Open("sqlite", backupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var version int
	if err = backup.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatalf("backup schema version = %d, error = %v", version, err)
	}
}

func TestMigrationFailureRollsBackAndKeepsVersionOneBackup(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "provider-history.sqlite3")
	hour := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	seedVersionOneHistory(t, databasePath, hour)
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec("CREATE TABLE provider_hours_by_time(value INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if err = database.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := history.Open(context.Background(), history.Config{Path: databasePath})
	if store != nil || !errors.Is(err, history.ErrMigration) {
		t.Fatalf("Open(conflicting v1) = (%v, %v), want ErrMigration", store, err)
	}
	if _, err = os.Stat(databasePath + ".schema-v1.bak"); err != nil {
		t.Fatalf("migration backup missing: %v", err)
	}

	database, err = sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var version int
	if err = database.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatalf("rolled-back version = %d, error = %v", version, err)
	}
	rows, err := database.Query("PRAGMA table_info(provider_hours)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var index, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err = rows.Scan(&index, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "error_code" {
			t.Fatal("failed migration left error_code column behind")
		}
	}
}

func TestOpenRejectsCorruptDatabaseWithoutReplacingIt(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "provider-history.sqlite3")
	corrupt := []byte("not a sqlite database; private evidence must remain untouched")
	if err := os.WriteFile(databasePath, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := history.Open(context.Background(), history.Config{Path: databasePath})
	if store != nil || !errors.Is(err, history.ErrCorrupt) {
		t.Fatalf("Open(corrupt) = (%v, %v), want ErrCorrupt", store, err)
	}
	after, readErr := os.ReadFile(databasePath)
	if readErr != nil || !bytes.Equal(after, corrupt) {
		t.Fatalf("corrupt database was replaced: bytes=%q error=%v", after, readErr)
	}
}

func TestRetentionKeepsExactlyNinetyDaysAndPrunesOlderHours(t *testing.T) {
	store, err := history.Open(context.Background(), history.Config{
		Path: filepath.Join(t.TempDir(), "provider-history.sqlite3"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())

	latest := time.Date(2026, 8, 15, 15, 0, 0, 0, time.UTC)
	exactBoundary := latest.Add(-90 * 24 * time.Hour)
	tooOld := exactBoundary.Add(-time.Hour)
	for _, observed := range []time.Time{tooOld, exactBoundary, latest} {
		if err = store.Capture(context.Background(), providerAt("codex", 1000, 1_000_000), observed); err != nil {
			t.Fatalf("Capture(%s) error = %v", observed, err)
		}
	}
	if err = store.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	records, err := store.Query(context.Background(), history.Query{
		From: tooOld, Until: latest.Add(time.Hour), Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || !records[0].HourUTC.Equal(exactBoundary) || !records[1].HourUTC.Equal(latest) {
		t.Fatalf("retained records = %+v", records)
	}
}

func TestCommittedCaptureSurvivesProcessCrash(t *testing.T) {
	if os.Getenv("S3DECK_HISTORY_CRASH_HELPER") == "1" {
		path := os.Getenv("S3DECK_HISTORY_CRASH_PATH")
		store, err := history.Open(context.Background(), history.Config{Path: path})
		if err != nil {
			os.Exit(21)
		}
		observed := time.Date(2026, 8, 15, 13, 0, 0, 0, time.UTC)
		if store.Capture(context.Background(), providerAt("codex", 2200, 3_000_000), observed) != nil ||
			store.Flush(context.Background()) != nil {
			os.Exit(22)
		}
		// Deliberately skip Close to model process termination after COMMIT.
		os.Exit(0)
	}

	databasePath := filepath.Join(t.TempDir(), "provider-history.sqlite3")
	command := exec.Command(os.Args[0], "-test.run=^TestCommittedCaptureSurvivesProcessCrash$")
	command.Env = append(os.Environ(),
		"S3DECK_HISTORY_CRASH_HELPER=1",
		"S3DECK_HISTORY_CRASH_PATH="+databasePath,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("crash helper: %v\n%s", err, output)
	}
	store, err := history.Open(context.Background(), history.Config{Path: databasePath})
	if err != nil {
		t.Fatalf("Open(after crash) error = %v", err)
	}
	defer store.Close(context.Background())
	records, err := store.Query(context.Background(), history.Query{
		From:  time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 8, 15, 14, 0, 0, 0, time.UTC),
		Limit: 10,
	})
	if err != nil || len(records) != 1 || records[0].ProviderID != "codex" {
		t.Fatalf("records after crash = %+v, error = %v", records, err)
	}
}

func TestSlowCSVConsumerDoesNotBlockCapture(t *testing.T) {
	store, err := history.Open(context.Background(), history.Config{
		Path: filepath.Join(t.TempDir(), "provider-history.sqlite3"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	first := time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC)
	if err = store.Capture(context.Background(), providerAt("codex", 1000, 1_000_000), first); err != nil {
		t.Fatal(err)
	}
	if err = store.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	destination := newBlockingWriter()
	exportDone := make(chan error, 1)
	go func() {
		exportDone <- store.ExportCSV(context.Background(), destination, history.Query{
			From: first.Add(-time.Hour), Until: first.Add(3 * time.Hour), Limit: 10,
		})
	}()
	<-destination.started

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	second := first.Add(time.Hour)
	if err = store.Capture(ctx, providerAt("codex", 2000, 2_000_000), second); err != nil {
		t.Fatalf("Capture(while export blocked) error = %v", err)
	}
	if err = store.Flush(ctx); err != nil {
		t.Fatalf("Flush(while export blocked) error = %v", err)
	}
	close(destination.release)
	if err = <-exportDone; err != nil {
		t.Fatalf("ExportCSV() error = %v", err)
	}
}

func TestFlushReportsBoundedWriterFailureAndLaterRecovery(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "provider-history.sqlite3")
	store, err := history.Open(context.Background(), history.Config{Path: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())

	blocker, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	if _, err = blocker.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	observed := time.Date(2026, 8, 15, 16, 0, 0, 0, time.UTC)
	if err = store.Capture(context.Background(), providerAt("codex", 1000, 1_000_000), observed); err != nil {
		t.Fatal(err)
	}
	flushContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	err = store.Flush(flushContext)
	cancel()
	if !errors.Is(err, history.ErrUnavailable) {
		t.Fatalf("Flush(locked writer) error = %v, want ErrUnavailable", err)
	}
	if _, err = blocker.Exec("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if err = store.Capture(context.Background(), providerAt("codex", 2000, 2_000_000), observed); err != nil {
		t.Fatal(err)
	}
	if err = store.Flush(context.Background()); err != nil {
		t.Fatalf("Flush(after recovery) error = %v", err)
	}
}

func TestConcurrentCaptureAndBoundedQueries(t *testing.T) {
	store, err := history.Open(context.Background(), history.Config{
		Path:      filepath.Join(t.TempDir(), "provider-history.sqlite3"),
		QueueSize: 512,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	base := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)

	var wait sync.WaitGroup
	errorsSeen := make(chan error, 128)
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for offset := 0; offset < 8; offset++ {
				observed := base.Add(time.Duration(worker*8+offset) * time.Minute)
				if captureErr := store.Capture(
					context.Background(),
					providerAt("codex", uint16(1000+worker), uint64(1_000_000+offset)),
					observed,
				); captureErr != nil {
					errorsSeen <- captureErr
				}
			}
		}(worker)
	}
	for reader := 0; reader < 4; reader++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for attempt := 0; attempt < 20; attempt++ {
				if _, queryErr := store.Query(context.Background(), history.Query{
					From: base.Add(-time.Hour), Until: base.Add(2 * time.Hour), Limit: 10,
				}); queryErr != nil {
					errorsSeen <- queryErr
				}
			}
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for observedErr := range errorsSeen {
		t.Fatalf("concurrent operation error = %v", observedErr)
	}
	if err = store.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Query(context.Background(), history.Query{
		From: base, Until: base.Add(time.Hour), Limit: 20_001,
	}); !errors.Is(err, history.ErrInvalid) {
		t.Fatalf("unbounded Query() error = %v, want ErrInvalid", err)
	}
}

func TestDatabaseNeverReceivesDisplayOrNonHistoryProviderFields(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "provider-history.sqlite3")
	store, err := history.Open(context.Background(), history.Config{Path: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	observed := time.Date(2026, 8, 15, 17, 0, 0, 0, time.UTC)
	updated := observed.Add(-time.Minute).Format(time.RFC3339)
	staleAfter := uint32(123)
	provider := providerAt("codex", 1000, 1_000_000)
	provider.DisplayName = "PRIVATE_DISPLAY_CANARY"
	provider.UpdatedAt = &updated
	provider.StaleAfterSeconds = &staleAfter
	if err = store.Capture(context.Background(), provider, observed); err != nil {
		t.Fatal(err)
	}
	if err = store.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm"} {
		contents, readErr := os.ReadFile(path)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		if bytes.Contains(contents, []byte("PRIVATE_DISPLAY_CANARY")) {
			t.Fatalf("non-history Provider field persisted in %s", filepath.Base(path))
		}
	}
}

type blockingWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{started: make(chan struct{}), release: make(chan struct{})}
}

func (writer *blockingWriter) Write(contents []byte) (int, error) {
	writer.once.Do(func() { close(writer.started) })
	<-writer.release
	return len(contents), nil
}

func queryDay(t *testing.T, store *history.Store, at time.Time) []history.Record {
	t.Helper()
	records, err := store.Query(context.Background(), history.Query{
		From:  at.Add(-12 * time.Hour),
		Until: at.Add(12 * time.Hour),
		Limit: 100,
	})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	return records
}

func providerAt(id string, used uint16, amount uint64) aisnapshot.Provider {
	remaining := uint16(10_000 - used)
	return aisnapshot.Provider{
		SchemaVersion: aisnapshot.SchemaVersion{Major: 1, Minor: 0},
		ID:            id,
		DisplayName:   "Codex",
		Status:        aisnapshot.ProviderOK,
		Source:        aisnapshot.ProviderSourceCodexAppServer,
		Confidence:    aisnapshot.ConfidenceVerified,
		Balance:       &aisnapshot.Money{AmountMicros: amount, Currency: "USD"},
		Windows: []aisnapshot.QuotaWindow{{
			Name:                 "primary",
			UsedBasisPoints:      &used,
			RemainingBasisPoints: &remaining,
		}},
	}
}

func seedVersionOneHistory(t *testing.T, path string, hour time.Time) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	_, err = database.Exec(`
CREATE TABLE provider_hours (
    provider_id TEXT NOT NULL,
    hour_utc_ms INTEGER NOT NULL,
    observed_at_utc_ms INTEGER NOT NULL,
    status TEXT NOT NULL,
    balance_amount_micros INTEGER,
    balance_currency TEXT,
    token_input INTEGER,
    token_cached_input INTEGER,
    token_output INTEGER,
    token_reasoning INTEGER,
    token_total INTEGER,
    PRIMARY KEY (provider_id, hour_utc_ms)
) WITHOUT ROWID;
CREATE TABLE quota_hours (
    provider_id TEXT NOT NULL,
    hour_utc_ms INTEGER NOT NULL,
    ordinal INTEGER NOT NULL,
    name TEXT NOT NULL,
    used_basis_points INTEGER,
    remaining_basis_points INTEGER,
    window_minutes INTEGER,
    resets_at_utc_ms INTEGER,
    PRIMARY KEY (provider_id, hour_utc_ms, ordinal),
    FOREIGN KEY (provider_id, hour_utc_ms)
        REFERENCES provider_hours(provider_id, hour_utc_ms) ON DELETE CASCADE
) WITHOUT ROWID;
CREATE TABLE history_settings (
    singleton INTEGER PRIMARY KEY,
    enabled INTEGER NOT NULL
);
INSERT INTO history_settings(singleton, enabled) VALUES (1, 1);
INSERT INTO provider_hours(
    provider_id, hour_utc_ms, observed_at_utc_ms, status,
    balance_amount_micros, balance_currency
) VALUES ('codex', ?, ?, 'ok', 7000000, 'USD');
INSERT INTO quota_hours(
    provider_id, hour_utc_ms, ordinal, name, used_basis_points, remaining_basis_points
) VALUES ('codex', ?, 0, 'primary', 2500, 7500);
PRAGMA user_version = 1;
`, hour.UnixMilli(), hour.Add(5*time.Minute).UnixMilli(), hour.UnixMilli())
	if err != nil {
		t.Fatalf("seed v1 history: %v", err)
	}
}
