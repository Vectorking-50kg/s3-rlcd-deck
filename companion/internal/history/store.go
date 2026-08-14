package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
	sqlitedriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type commandKind uint8

const (
	writerOperationTimeout = 5 * time.Second
	queryOperationTimeout  = 5 * time.Second
	maintenanceTimeout     = 2 * time.Second
	retentionSweepInterval = time.Hour
)

const (
	commandCapture commandKind = iota
	commandFlush
	commandSetEnabled
	commandClear
	commandClose
)

type storeCommand struct {
	kind       commandKind
	provider   aisnapshot.Provider
	observedAt time.Time
	enabled    bool
	generation uint64
	result     chan error
}

// Store is a single-writer deep module. Capture only validates and transfers
// an owned DTO into a bounded queue; all SQLite writes belong to its worker.
type Store struct {
	path      string
	retention time.Duration
	writer    *sql.DB
	reader    *sql.DB
	lock      *protectedfile.Lock
	commands  chan storeCommand
	done      chan struct{}
	now       func() time.Time

	stateMu    sync.RWMutex
	closed     bool
	enabled    atomic.Bool
	available  atomic.Bool
	generation atomic.Uint64
}

func Open(ctx context.Context, config Config) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if config.Path == "" {
		return nil, fmt.Errorf("%w: database path is required", ErrInvalid)
	}
	path, err := filepath.Abs(filepath.Clean(config.Path))
	if err != nil || path == "" {
		return nil, fmt.Errorf("%w: database path is invalid", ErrInvalid)
	}
	if config.Retention == 0 {
		config.Retention = DefaultRetention
	}
	if config.Retention < time.Hour || config.Retention > 366*24*time.Hour || config.Retention%time.Hour != 0 {
		return nil, fmt.Errorf("%w: retention must be whole UTC hours", ErrInvalid)
	}
	if config.QueueSize == 0 {
		config.QueueSize = defaultQueueSize
	}
	if config.QueueSize < 1 || config.QueueSize > 4096 {
		return nil, fmt.Errorf("%w: queue size is out of range", ErrInvalid)
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	currentUTC := config.Now().UTC()
	if currentUTC.IsZero() {
		return nil, fmt.Errorf("%w: current UTC time is unavailable", ErrInvalid)
	}

	lock, err := protectedfile.AcquireDirectoryLock(filepath.Dir(path), ".provider-history.lock")
	if err != nil {
		return nil, fmt.Errorf("open Provider history: %w", err)
	}
	closeLock := true
	defer func() {
		if closeLock {
			_ = lock.Close()
		}
	}()
	if info, inspectErr := os.Lstat(path); inspectErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: database must be a regular non-symlink file", ErrUnavailable)
		}
		if err = protectedfile.EnsurePrivateFile(path); err != nil {
			return nil, fmt.Errorf("open Provider history: %w", err)
		}
	} else if !errors.Is(inspectErr, os.ErrNotExist) {
		return nil, fmt.Errorf("open Provider history: %w", inspectErr)
	}

	writer, err := sql.Open("sqlite", writableDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open Provider history: %w", err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	closeWriter := true
	defer func() {
		if closeWriter {
			_ = writer.Close()
		}
	}()
	if err = writer.PingContext(ctx); err != nil {
		return nil, classifyDatabaseError(err)
	}
	if err = protectedfile.EnsurePrivateFile(path); err != nil {
		return nil, fmt.Errorf("protect Provider history: %w", err)
	}
	if err = initializeSchema(ctx, writer, path); err != nil {
		if isCorruptError(err) || errors.Is(err, ErrMigration) {
			return nil, err
		}
		return nil, classifyDatabaseError(err)
	}
	if err = ensureSQLiteFilesPrivate(path); err != nil {
		return nil, fmt.Errorf("protect Provider history: %w", err)
	}
	var enabled int
	if err = writer.QueryRowContext(ctx, "SELECT enabled FROM history_settings WHERE singleton = 1").Scan(&enabled); err != nil {
		return nil, classifyDatabaseError(err)
	}
	pruneContext, cancelPrune := context.WithTimeout(ctx, writerOperationTimeout)
	err = pruneExpired(pruneContext, writer, currentUTC, config.Retention)
	cancelPrune()
	if err != nil {
		return nil, err
	}
	if err = ensureSQLiteFilesPrivate(path); err != nil {
		return nil, fmt.Errorf("protect Provider history: %w", err)
	}

	reader, err := sql.Open("sqlite", readOnlyDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open Provider history reader: %w", err)
	}
	reader.SetMaxOpenConns(4)
	reader.SetMaxIdleConns(1)
	if err = reader.PingContext(ctx); err != nil {
		_ = reader.Close()
		return nil, classifyDatabaseError(err)
	}

	store := &Store{
		path:      path,
		retention: config.Retention,
		writer:    writer,
		reader:    reader,
		lock:      lock,
		commands:  make(chan storeCommand, config.QueueSize),
		done:      make(chan struct{}),
		now:       config.Now,
	}
	store.enabled.Store(enabled == 1)
	store.available.Store(true)
	closeLock = false
	closeWriter = false
	go store.runWriter()
	return store, nil
}

func (store *Store) Capture(ctx context.Context, provider aisnapshot.Provider, observedAt time.Time) error {
	if store == nil {
		return ErrClosed
	}
	// This is the capture admission linearization point. It intentionally
	// precedes caller-context checks, DTO validation, and cloning so a clear or
	// enable-state barrier that completes while any of those steps run makes
	// the eventual command stale.
	admissionGeneration := store.generation.Load()
	if err := ctx.Err(); err != nil {
		return err
	}
	store.stateMu.RLock()
	defer store.stateMu.RUnlock()
	if store.closed {
		store.available.Store(false)
		return ErrClosed
	}
	if !store.enabled.Load() {
		return nil
	}
	observedAt = observedAt.UTC()
	if observedAt.IsZero() || aisnapshot.ValidateProvider(provider, observedAt) != nil {
		return ErrInvalid
	}
	command := storeCommand{
		kind:       commandCapture,
		provider:   provider.Clone(),
		observedAt: observedAt,
		generation: admissionGeneration,
	}
	select {
	case store.commands <- command:
		return nil
	default:
		store.available.Store(false)
		return ErrBusy
	}
}

func (store *Store) Flush(ctx context.Context) error {
	return store.synchronize(ctx, commandFlush)
}

func (store *Store) Enabled() bool {
	return store != nil && store.enabled.Load()
}

// Available reports whether the bounded history subsystem can currently
// accept and persist normalized Provider updates. It never exposes a database
// or Provider error string across the Runtime boundary.
func (store *Store) Available() bool {
	return store != nil && store.available.Load()
}

func (store *Store) Settings() Settings {
	if store == nil {
		return Settings{}
	}
	return Settings{
		Enabled:       store.enabled.Load(),
		Available:     store.available.Load(),
		RetentionDays: int(store.retention / (24 * time.Hour)),
	}
}

func (store *Store) SetEnabled(ctx context.Context, enabled bool) error {
	return store.synchronizeCommand(ctx, storeCommand{kind: commandSetEnabled, enabled: enabled})
}

func (store *Store) Clear(ctx context.Context) error {
	return store.synchronize(ctx, commandClear)
}

func (store *Store) Close(ctx context.Context) error {
	if store == nil {
		return nil
	}
	store.stateMu.Lock()
	if store.closed {
		store.stateMu.Unlock()
		select {
		case <-store.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	result := make(chan error, 1)
	select {
	case store.commands <- storeCommand{kind: commandClose, result: result}:
		store.closed = true
		store.available.Store(false)
		store.stateMu.Unlock()
	case <-ctx.Done():
		store.stateMu.Unlock()
		return ctx.Err()
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (store *Store) synchronize(ctx context.Context, kind commandKind) error {
	return store.synchronizeCommand(ctx, storeCommand{kind: kind})
}

func (store *Store) synchronizeCommand(ctx context.Context, command storeCommand) error {
	if store == nil {
		return ErrClosed
	}
	result := make(chan error, 1)
	command.result = result
	store.stateMu.RLock()
	defer store.stateMu.RUnlock()
	if store.closed {
		store.available.Store(false)
		return ErrClosed
	}
	select {
	case store.commands <- command:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (store *Store) runWriter() {
	defer close(store.done)
	ticker := time.NewTicker(retentionSweepInterval)
	defer ticker.Stop()
	var persistenceErr error
	for {
		var command storeCommand
		select {
		case command = <-store.commands:
		default:
			select {
			case command = <-store.commands:
			case <-ticker.C:
				operationContext, cancel := context.WithTimeout(context.Background(), maintenanceTimeout)
				err := pruneExpired(operationContext, store.writer, store.now().UTC(), store.retention)
				if err == nil {
					err = ensureSQLiteFilesPrivate(store.path)
				}
				cancel()
				persistenceErr = err
				store.available.Store(err == nil)
				continue
			}
		}
		var err error
		switch command.kind {
		case commandCapture:
			if !store.enabled.Load() || command.generation != store.generation.Load() {
				break
			}
			operationContext, cancel := context.WithTimeout(context.Background(), writerOperationTimeout)
			err = store.capture(operationContext, command.provider, command.observedAt)
			cancel()
			persistenceErr = err
			store.available.Store(err == nil)
		case commandFlush:
			// FIFO ordering makes reaching this command the flush barrier.
			err = persistenceErr
		case commandSetEnabled:
			operationContext, cancel := context.WithTimeout(context.Background(), writerOperationTimeout)
			err = store.setEnabled(operationContext, command.enabled)
			cancel()
			if err == nil {
				store.generation.Add(1)
				store.enabled.Store(command.enabled)
			}
			persistenceErr = err
			store.available.Store(err == nil)
		case commandClear:
			operationContext, cancel := context.WithTimeout(context.Background(), writerOperationTimeout)
			_, err = store.writer.ExecContext(operationContext, "DELETE FROM provider_hours")
			cancel()
			if err != nil {
				err = classifyDatabaseError(err)
			} else {
				store.generation.Add(1)
				persistenceErr = nil
			}
			store.available.Store(err == nil)
		case commandClose:
			err = errors.Join(persistenceErr, store.reader.Close(), store.writer.Close(), store.lock.Close())
			if command.result != nil {
				command.result <- err
			}
			return
		}
		if command.result != nil {
			command.result <- err
		}
	}
}

func (store *Store) capture(ctx context.Context, provider aisnapshot.Provider, observedAt time.Time) error {
	transaction, err := store.writer.BeginTx(ctx, nil)
	if err != nil {
		return classifyDatabaseError(err)
	}
	defer transaction.Rollback()
	hour := observedAt.Truncate(time.Hour)
	result, err := transaction.ExecContext(ctx, `
INSERT INTO provider_hours (
    provider_id, hour_utc_ms, observed_at_utc_ms, status, error_code,
    balance_amount_micros, balance_currency,
    token_input, token_cached_input, token_output, token_reasoning, token_total
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(provider_id, hour_utc_ms) DO UPDATE SET
    observed_at_utc_ms = excluded.observed_at_utc_ms,
    status = excluded.status,
    error_code = excluded.error_code,
    balance_amount_micros = excluded.balance_amount_micros,
    balance_currency = excluded.balance_currency,
    token_input = excluded.token_input,
    token_cached_input = excluded.token_cached_input,
    token_output = excluded.token_output,
    token_reasoning = excluded.token_reasoning,
    token_total = excluded.token_total
WHERE excluded.observed_at_utc_ms >= provider_hours.observed_at_utc_ms`,
		provider.ID,
		hour.UnixMilli(),
		observedAt.UnixMilli(),
		provider.Status,
		errorCodeValue(provider.Error),
		moneyAmount(provider.Balance),
		moneyCurrency(provider.Balance),
		tokenValue(provider.Tokens, func(tokens *aisnapshot.TokenUsage) *uint64 { return tokens.Input }),
		tokenValue(provider.Tokens, func(tokens *aisnapshot.TokenUsage) *uint64 { return tokens.CachedInput }),
		tokenValue(provider.Tokens, func(tokens *aisnapshot.TokenUsage) *uint64 { return tokens.Output }),
		tokenValue(provider.Tokens, func(tokens *aisnapshot.TokenUsage) *uint64 { return tokens.Reasoning }),
		tokenValue(provider.Tokens, func(tokens *aisnapshot.TokenUsage) *uint64 { return tokens.Total }),
	)
	if err != nil {
		return classifyDatabaseError(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return classifyDatabaseError(err)
	}
	if changed != 0 {
		if _, err = transaction.ExecContext(
			ctx,
			"DELETE FROM quota_hours WHERE provider_id = ? AND hour_utc_ms = ?",
			provider.ID,
			hour.UnixMilli(),
		); err != nil {
			return classifyDatabaseError(err)
		}
		for ordinal, window := range provider.Windows {
			if _, err = transaction.ExecContext(ctx, `
INSERT INTO quota_hours (
    provider_id, hour_utc_ms, ordinal, name, used_basis_points,
    remaining_basis_points, window_minutes, resets_at_utc_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				provider.ID,
				hour.UnixMilli(),
				ordinal,
				window.Name,
				optionalUnsigned(window.UsedBasisPoints),
				optionalUnsigned(window.RemainingBasisPoints),
				optionalUnsigned(window.WindowMinutes),
				optionalSigned(window.ResetsAtUnixMS),
			); err != nil {
				return classifyDatabaseError(err)
			}
		}
	}
	if err = pruneExpired(ctx, transaction, store.now().UTC(), store.retention); err != nil {
		return err
	}
	if err = transaction.Commit(); err != nil {
		return classifyDatabaseError(err)
	}
	return ensureSQLiteFilesPrivate(store.path)
}

func (store *Store) Query(ctx context.Context, query Query) ([]Record, error) {
	if store == nil {
		return nil, ErrClosed
	}
	if err := validateQuery(query); err != nil {
		return nil, err
	}
	store.stateMu.RLock()
	closed := store.closed
	store.stateMu.RUnlock()
	if closed {
		store.available.Store(false)
		return nil, ErrClosed
	}
	queryContext, cancel := context.WithTimeout(ctx, queryOperationTimeout)
	defer cancel()
	arguments := []any{query.From.UTC().UnixMilli(), query.Until.UTC().UnixMilli()}
	providerClause := ""
	if query.ProviderID != "" {
		providerClause = " AND provider_id = ?"
		arguments = append(arguments, query.ProviderID)
	}
	arguments = append(arguments, query.Limit)
	rows, err := store.reader.QueryContext(queryContext, `
WITH selected AS (
    SELECT * FROM provider_hours
    WHERE hour_utc_ms >= ? AND hour_utc_ms < ?`+providerClause+`
    ORDER BY hour_utc_ms ASC, provider_id ASC
    LIMIT ?
)
SELECT
    selected.provider_id, selected.hour_utc_ms, selected.observed_at_utc_ms,
    selected.status, selected.error_code,
    selected.balance_amount_micros, selected.balance_currency,
    selected.token_input, selected.token_cached_input, selected.token_output,
    selected.token_reasoning, selected.token_total,
    quota_hours.ordinal, quota_hours.name, quota_hours.used_basis_points,
    quota_hours.remaining_basis_points, quota_hours.window_minutes,
    quota_hours.resets_at_utc_ms
FROM selected
LEFT JOIN quota_hours USING (provider_id, hour_utc_ms)
ORDER BY selected.hour_utc_ms ASC, selected.provider_id ASC, quota_hours.ordinal ASC`, arguments...)
	if err != nil {
		return nil, store.classifyQueryError(ctx, err)
	}
	defer rows.Close()
	records := make([]Record, 0, min(query.Limit, 256))
	var current *Record
	for rows.Next() {
		var providerID, status string
		var hourMS, observedMS int64
		var errorCode, currency sql.NullString
		var balance, tokenInput, tokenCached, tokenOutput, tokenReasoning, tokenTotal sql.NullInt64
		var ordinal, used, remaining, minutes, resetMS sql.NullInt64
		var name sql.NullString
		if err = rows.Scan(
			&providerID, &hourMS, &observedMS, &status, &errorCode,
			&balance, &currency, &tokenInput, &tokenCached, &tokenOutput,
			&tokenReasoning, &tokenTotal, &ordinal, &name, &used, &remaining,
			&minutes, &resetMS,
		); err != nil {
			return nil, store.classifyQueryError(ctx, err)
		}
		if current == nil || current.ProviderID != providerID || current.HourUTC.UnixMilli() != hourMS {
			records = append(records, recordFromRow(
				providerID, hourMS, observedMS, status, errorCode, balance, currency,
				tokenInput, tokenCached, tokenOutput, tokenReasoning, tokenTotal,
			))
			current = &records[len(records)-1]
		}
		if ordinal.Valid {
			current.Windows = append(current.Windows, quotaFromRow(name, used, remaining, minutes, resetMS))
		}
	}
	if err = rows.Err(); err != nil {
		return nil, store.classifyQueryError(ctx, err)
	}
	return records, nil
}

func (store *Store) classifyQueryError(callerContext context.Context, err error) error {
	classified := classifyDatabaseError(err)
	callerCanceled := callerContext.Err() != nil &&
		(errors.Is(classified, context.Canceled) || errors.Is(classified, context.DeadlineExceeded))
	if !callerCanceled {
		store.available.Store(false)
	}
	return classified
}

type databaseExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func pruneExpired(ctx context.Context, database databaseExecer, currentUTC time.Time, retention time.Duration) error {
	if currentUTC.IsZero() {
		return ErrUnavailable
	}
	cutoff := currentUTC.UTC().Truncate(time.Hour).Add(-retention)
	if _, err := database.ExecContext(ctx, "DELETE FROM provider_hours WHERE hour_utc_ms < ?", cutoff.UnixMilli()); err != nil {
		return classifyDatabaseError(err)
	}
	return nil
}

func (store *Store) setEnabled(ctx context.Context, enabled bool) error {
	if err := ensureSQLiteFilesPrivate(store.path); err != nil {
		return classifyDatabaseError(err)
	}
	transaction, err := store.writer.BeginTx(ctx, nil)
	if err != nil {
		return classifyDatabaseError(err)
	}
	defer transaction.Rollback()
	if _, err = transaction.ExecContext(
		ctx,
		"UPDATE history_settings SET enabled = ? WHERE singleton = 1",
		enabled,
	); err != nil {
		return classifyDatabaseError(err)
	}
	if err = pruneExpired(ctx, transaction, store.now().UTC(), store.retention); err != nil {
		return err
	}
	if err = transaction.Commit(); err != nil {
		return classifyDatabaseError(err)
	}
	return nil
}

func validateQuery(query Query) error {
	from := query.From.UTC()
	until := query.Until.UTC()
	if from.IsZero() || until.IsZero() || !from.Before(until) ||
		until.Sub(from) > 366*24*time.Hour || query.Limit < 1 || query.Limit > maximumQueryRows {
		return ErrInvalid
	}
	if query.ProviderID != "" && (len(query.ProviderID) > 32 || strings.TrimSpace(query.ProviderID) != query.ProviderID) {
		return ErrInvalid
	}
	return nil
}

func writableDSN(path string) string {
	return sqliteDSN(path, false)
}

func readOnlyDSN(path string) string {
	return sqliteDSN(path, true)
}

func sqliteDSN(path string, readOnly bool) string {
	slashed := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	location := &url.URL{Scheme: "file", Path: slashed}
	query := location.Query()
	if readOnly {
		query.Set("mode", "ro")
		query.Add("_pragma", "query_only(1)")
	} else {
		query.Add("_pragma", "journal_mode(WAL)")
		query.Add("_pragma", "synchronous(FULL)")
		query.Add("_pragma", "foreign_keys(1)")
		query.Add("_pragma", "trusted_schema(0)")
	}
	query.Add("_pragma", "busy_timeout(1000)")
	location.RawQuery = query.Encode()
	return location.String()
}

func classifyDatabaseError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var sqliteError *sqlitedriver.Error
	if errors.As(err, &sqliteError) {
		switch sqliteError.Code() & 0xff {
		case sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_NOTADB:
			return fmt.Errorf("%w", ErrCorrupt)
		}
	}
	return fmt.Errorf("%w", ErrUnavailable)
}

func ensureSQLiteFilesPrivate(path string) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := protectedfile.EnsurePrivateFile(candidate); err != nil {
			return err
		}
	}
	return nil
}

func errorCodeValue(problem *aisnapshot.ProviderError) any {
	if problem == nil {
		return nil
	}
	return string(problem.Code)
}

func moneyAmount(money *aisnapshot.Money) any {
	if money == nil {
		return nil
	}
	return int64(money.AmountMicros)
}

func moneyCurrency(money *aisnapshot.Money) any {
	if money == nil {
		return nil
	}
	return money.Currency
}

func tokenValue(tokens *aisnapshot.TokenUsage, selectValue func(*aisnapshot.TokenUsage) *uint64) any {
	if tokens == nil {
		return nil
	}
	return optionalUnsigned(selectValue(tokens))
}

func optionalUnsigned[T ~uint16 | ~uint32 | ~uint64](value *T) any {
	if value == nil {
		return nil
	}
	return int64(*value)
}

func optionalSigned(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func recordFromRow(
	providerID string,
	hourMS int64,
	observedMS int64,
	status string,
	errorCode sql.NullString,
	balance sql.NullInt64,
	currency sql.NullString,
	input sql.NullInt64,
	cached sql.NullInt64,
	output sql.NullInt64,
	reasoning sql.NullInt64,
	total sql.NullInt64,
) Record {
	record := Record{
		ProviderID: providerID,
		HourUTC:    time.UnixMilli(hourMS).UTC(),
		ObservedAt: time.UnixMilli(observedMS).UTC(),
		Status:     aisnapshot.ProviderStatus(status),
		Windows:    []QuotaWindow{},
	}
	if errorCode.Valid {
		value := aisnapshot.ProviderErrorCode(errorCode.String)
		record.ErrorCode = &value
	}
	if balance.Valid && currency.Valid {
		record.Balance = &aisnapshot.Money{AmountMicros: uint64(balance.Int64), Currency: currency.String}
	}
	if input.Valid || cached.Valid || output.Valid || reasoning.Valid || total.Valid {
		record.Tokens = &aisnapshot.TokenUsage{
			Input:       uint64Pointer(input),
			CachedInput: uint64Pointer(cached),
			Output:      uint64Pointer(output),
			Reasoning:   uint64Pointer(reasoning),
			Total:       uint64Pointer(total),
		}
	}
	return record
}

func quotaFromRow(
	name sql.NullString,
	used sql.NullInt64,
	remaining sql.NullInt64,
	minutes sql.NullInt64,
	resetMS sql.NullInt64,
) QuotaWindow {
	window := QuotaWindow{
		Name:                 name.String,
		UsedBasisPoints:      uint16Pointer(used),
		RemainingBasisPoints: uint16Pointer(remaining),
		WindowMinutes:        uint32Pointer(minutes),
	}
	if resetMS.Valid {
		value := time.UnixMilli(resetMS.Int64).UTC()
		window.ResetsAt = &value
	}
	return window
}

func uint64Pointer(value sql.NullInt64) *uint64 {
	if !value.Valid {
		return nil
	}
	converted := uint64(value.Int64)
	return &converted
}

func uint32Pointer(value sql.NullInt64) *uint32 {
	if !value.Valid {
		return nil
	}
	converted := uint32(value.Int64)
	return &converted
}

func uint16Pointer(value sql.NullInt64) *uint16 {
	if !value.Valid {
		return nil
	}
	converted := uint16(value.Int64)
	return &converted
}
