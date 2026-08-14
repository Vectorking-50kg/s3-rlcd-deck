// Package codexappserver adapts the versioned Codex App Server protocol into
// the repository's privacy-safe AI Snapshot DTOs. Raw App Server responses do
// not cross this package boundary.
package codexappserver

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
)

const (
	AdapterVersion = 1
	providerID     = "codex"
)

var (
	ErrUnavailable   = errors.New("Codex App Server is unavailable")
	ErrProcessExited = errors.New("Codex App Server exited")
	ErrSchemaChanged = errors.New("Codex App Server schema changed")
	ErrNotLoggedIn   = errors.New("Codex is not logged in")
	ErrPermission    = errors.New("Codex permission denied")
)

// Connection is a private, message-oriented JSONL session. Implementations
// must unblock Read when Close is called.
type Connection interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, []byte) error
	Close() error
}

type Connector interface {
	Connect(context.Context) (Connection, error)
}

type Config struct {
	Connector       Connector
	AdapterVersion  int
	ClientVersion   string
	RequestTimeout  time.Duration
	ReconnectDelay  time.Duration
	Now             func() time.Time
	MaximumDocument int
}

// Update contains only normalized DTOs accepted by the shared AI Snapshot
// contract. It intentionally has no field for raw responses or account data.
type Update struct {
	Provider aisnapshot.Provider
	Sessions []aisnapshot.Session
}

// Clone returns an independently owned copy suitable for crossing an in-memory
// publisher boundary. The DTO remains normalized; no upstream response is
// retained or reconstructed.
func (update Update) Clone() Update {
	cloned := Update{
		Provider: update.Provider,
		Sessions: cloneSlice(update.Sessions),
	}
	cloned.Provider.UpdatedAt = clonePointer(update.Provider.UpdatedAt)
	cloned.Provider.UpdatedAtUnixMS = clonePointer(update.Provider.UpdatedAtUnixMS)
	cloned.Provider.StaleAfterSeconds = clonePointer(update.Provider.StaleAfterSeconds)
	if update.Provider.Balance != nil {
		balance := *update.Provider.Balance
		cloned.Provider.Balance = &balance
	}
	cloned.Provider.Windows = cloneSlice(update.Provider.Windows)
	for index := range cloned.Provider.Windows {
		window := &cloned.Provider.Windows[index]
		source := update.Provider.Windows[index]
		window.UsedBasisPoints = clonePointer(source.UsedBasisPoints)
		window.RemainingBasisPoints = clonePointer(source.RemainingBasisPoints)
		window.WindowMinutes = clonePointer(source.WindowMinutes)
		window.ResetsAt = clonePointer(source.ResetsAt)
		window.ResetsAtUnixMS = clonePointer(source.ResetsAtUnixMS)
	}
	if update.Provider.Tokens != nil {
		tokens := *update.Provider.Tokens
		tokens.Input = clonePointer(update.Provider.Tokens.Input)
		tokens.CachedInput = clonePointer(update.Provider.Tokens.CachedInput)
		tokens.Output = clonePointer(update.Provider.Tokens.Output)
		tokens.Reasoning = clonePointer(update.Provider.Tokens.Reasoning)
		tokens.Total = clonePointer(update.Provider.Tokens.Total)
		cloned.Provider.Tokens = &tokens
	}
	if update.Provider.Error != nil {
		problem := *update.Provider.Error
		cloned.Provider.Error = &problem
	}
	for index := range cloned.Sessions {
		session := &cloned.Sessions[index]
		source := update.Sessions[index]
		session.DisplayName = clonePointer(source.DisplayName)
		session.StartedAt = clonePointer(source.StartedAt)
		session.StartedAtUnixMS = clonePointer(source.StartedAtUnixMS)
		session.LastActivityAt = clonePointer(source.LastActivityAt)
		session.LastActivityAtUnixMS = clonePointer(source.LastActivityAtUnixMS)
		session.DurationSeconds = clonePointer(source.DurationSeconds)
		session.TurnTokens = clonePointer(source.TurnTokens)
		session.ContextUsedBasisPoints = clonePointer(source.ContextUsedBasisPoints)
	}
	return cloned
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneSlice[T any](values []T) []T {
	if values == nil {
		return nil
	}
	cloned := make([]T, len(values))
	copy(cloned, values)
	return cloned
}

type Publisher func(context.Context, Update) error

func discardCloser(closer io.Closer) {
	if closer != nil {
		_ = closer.Close()
	}
}
