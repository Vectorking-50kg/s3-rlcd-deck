package cursorprovider

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func createCursorStateDatabase(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.vscdb")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	return path, database
}

func TestSQLiteTokenSourceReadsOnlyCurrentAccessToken(t *testing.T) {
	path, database := createCursorStateDatabase(t)
	defer database.Close()
	if _, err := database.Exec(
		`INSERT INTO ItemTable(key, value) VALUES (?, ?), (?, ?)`,
		cursorAccessTokenKey,
		"fresh-access-token",
		"cursorAuth/refreshToken",
		"refresh-token-must-never-be-read",
	); err != nil {
		t.Fatal(err)
	}
	source, err := NewSQLiteTokenSource(path)
	if err != nil {
		t.Fatal(err)
	}
	token, err := source.AccessToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer overwrite(token)
	if string(token) != "fresh-access-token" {
		t.Fatalf("token = %q", token)
	}
	if _, err = database.Exec(
		`UPDATE ItemTable SET value = ? WHERE key = ?`,
		"replacement-access-token",
		cursorAccessTokenKey,
	); err != nil {
		t.Fatal(err)
	}
	replacement, err := source.AccessToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer overwrite(replacement)
	if string(replacement) != "replacement-access-token" {
		t.Fatalf("replacement = %q", replacement)
	}
}

func TestSQLiteTokenSourceFailsClosedForMissingOrChangedState(t *testing.T) {
	t.Run("missing database", func(t *testing.T) {
		source, err := NewSQLiteTokenSource(filepath.Join(t.TempDir(), "missing.vscdb"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = source.AccessToken(context.Background()); !errors.Is(err, ErrNotLoggedIn) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("missing access token", func(t *testing.T) {
		path, database := createCursorStateDatabase(t)
		database.Close()
		source, _ := NewSQLiteTokenSource(path)
		if _, err := source.AccessToken(context.Background()); !errors.Is(err, ErrNotLoggedIn) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("schema changed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.vscdb")
		database, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = database.Exec(`CREATE TABLE OtherTable (name TEXT)`); err != nil {
			database.Close()
			t.Fatal(err)
		}
		database.Close()
		source, _ := NewSQLiteTokenSource(path)
		if _, err = source.AccessToken(context.Background()); !errors.Is(err, ErrSchemaChanged) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("malformed token", func(t *testing.T) {
		path, database := createCursorStateDatabase(t)
		if _, err := database.Exec(
			`INSERT INTO ItemTable(key, value) VALUES (?, ?)`,
			cursorAccessTokenKey,
			"token with whitespace",
		); err != nil {
			database.Close()
			t.Fatal(err)
		}
		database.Close()
		source, _ := NewSQLiteTokenSource(path)
		if _, err := source.AccessToken(context.Background()); !errors.Is(err, ErrNotLoggedIn) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestSQLiteTokenSourceRejectsSymlinkAndPermissionFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows reparse-point behavior is covered by the native smoke")
	}
	path, database := createCursorStateDatabase(t)
	if _, err := database.Exec(
		`INSERT INTO ItemTable(key, value) VALUES (?, ?)`,
		cursorAccessTokenKey,
		"access-token",
	); err != nil {
		database.Close()
		t.Fatal(err)
	}
	database.Close()
	symlink := filepath.Join(t.TempDir(), "linked.vscdb")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	source, _ := NewSQLiteTokenSource(symlink)
	if _, err := source.AccessToken(context.Background()); !errors.Is(err, ErrPermission) {
		t.Fatalf("symlink error = %v", err)
	}

	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	source, _ = NewSQLiteTokenSource(path)
	if _, err := source.AccessToken(context.Background()); !errors.Is(err, ErrPermission) {
		t.Fatalf("permission error = %v", err)
	}
}

func TestSQLiteTokenSourceBoundsLockedDatabaseWait(t *testing.T) {
	path, database := createCursorStateDatabase(t)
	defer database.Close()
	if _, err := database.Exec(
		`INSERT INTO ItemTable(key, value) VALUES (?, ?)`,
		cursorAccessTokenKey,
		"access-token",
	); err != nil {
		t.Fatal(err)
	}
	connection, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err = connection.ExecContext(context.Background(), "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	defer connection.ExecContext(context.Background(), "ROLLBACK")
	source, _ := NewSQLiteTokenSource(path)
	started := time.Now()
	_, err = source.AccessToken(context.Background())
	if !errors.Is(err, ErrDatabaseLocked) {
		t.Fatalf("error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("locked read took %v", elapsed)
	}
}

func TestDefaultCursorStatePathUsesPlatformConfigurationDirectory(t *testing.T) {
	source, err := NewSQLiteTokenSource("")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(source.databasePath) != "state.vscdb" ||
		filepath.Base(filepath.Dir(filepath.Dir(source.databasePath))) != "User" {
		t.Fatalf("database path = %q", source.databasePath)
	}
}
