package cursorprovider

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	sqlitedriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	cursorAccessTokenKey = "cursorAuth/accessToken"
	maximumTokenBytes    = 16 << 10
)

// SQLiteTokenSource reads Cursor's application-owned state database in
// read-only/query-only mode. A database is opened for each call so an atomic
// Cursor state replacement cannot leave the adapter pinned to an old token.
type SQLiteTokenSource struct {
	databasePath string
}

func NewSQLiteTokenSource(databasePath string) (*SQLiteTokenSource, error) {
	if databasePath == "" {
		configurationDirectory, err := os.UserConfigDir()
		if err != nil {
			return nil, ErrUnavailable
		}
		databasePath = filepath.Join(
			configurationDirectory,
			"Cursor",
			"User",
			"globalStorage",
			"state.vscdb",
		)
	}
	absolute, err := filepath.Abs(filepath.Clean(databasePath))
	if err != nil || absolute == "" {
		return nil, ErrUnavailable
	}
	return &SQLiteTokenSource{databasePath: absolute}, nil
}

func (source *SQLiteTokenSource) AccessToken(ctx context.Context) ([]byte, error) {
	if source == nil || source.databasePath == "" {
		return nil, ErrUnavailable
	}
	info, err := os.Lstat(source.databasePath)
	if err != nil {
		return nil, classifyStateError(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, ErrPermission
	}
	probe, err := os.Open(source.databasePath)
	if err != nil {
		return nil, classifyStateError(err)
	}
	if err = probe.Close(); err != nil {
		return nil, ErrUnavailable
	}

	database, err := sql.Open("sqlite", sqliteReadOnlyDSN(source.databasePath))
	if err != nil {
		return nil, classifySQLiteError(err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(0)
	defer database.Close()

	var token []byte
	err = database.QueryRowContext(
		ctx,
		"SELECT value FROM ItemTable WHERE key = ? LIMIT 1",
		cursorAccessTokenKey,
	).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotLoggedIn
	}
	if err != nil {
		return nil, classifySQLiteError(err)
	}
	if !validAccessToken(token) {
		overwrite(token)
		return nil, ErrNotLoggedIn
	}
	return token, nil
}

func sqliteReadOnlyDSN(databasePath string) string {
	path := filepath.ToSlash(databasePath)
	if filepath.VolumeName(databasePath) != "" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	location := &url.URL{Scheme: "file", Path: path}
	query := location.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	query.Add("_pragma", "busy_timeout(250)")
	location.RawQuery = query.Encode()
	return location.String()
}

func validAccessToken(token []byte) bool {
	if len(token) == 0 || len(token) > maximumTokenBytes {
		return false
	}
	for _, character := range token {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func classifyStateError(err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return ErrPermission
	}
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotLoggedIn
	}
	return ErrUnavailable
}

func classifySQLiteError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	var sqliteError *sqlitedriver.Error
	if !errors.As(err, &sqliteError) {
		return classifyStateError(err)
	}
	switch sqliteError.Code() & 0xff {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
		return ErrDatabaseLocked
	case sqlite3.SQLITE_PERM, sqlite3.SQLITE_AUTH, sqlite3.SQLITE_READONLY:
		return ErrPermission
	case sqlite3.SQLITE_ERROR, sqlite3.SQLITE_SCHEMA, sqlite3.SQLITE_CORRUPT,
		sqlite3.SQLITE_NOTADB:
		return ErrSchemaChanged
	default:
		return ErrUnavailable
	}
}

func overwrite(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
