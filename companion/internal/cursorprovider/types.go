// Package cursorprovider implements the experimental, read-only Cursor quota
// adapter. Raw Cursor credentials and private endpoint responses never cross
// this package boundary.
package cursorprovider

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
)

const (
	AdapterVersion        = 1
	ResponseSchemaVersion = 1

	providerID   = "cursor"
	providerName = "Cursor"
)

var (
	ErrAlreadyRunning = errors.New("Cursor collector is already running")
	ErrNotLoggedIn    = errors.New("Cursor is not logged in")
	ErrPermission     = errors.New("Cursor state permission denied")
	ErrDatabaseLocked = errors.New("Cursor state database is locked")
	ErrSchemaChanged  = errors.New("Cursor private response schema changed")
	ErrUnavailable    = errors.New("Cursor usage is unavailable")
)

// AccessTokenSource returns a newly read access token owned by the caller.
// Implementations must not read or refresh Cursor's refresh token.
type AccessTokenSource interface {
	AccessToken(context.Context) ([]byte, error)
}

// Config keeps the unstable Cursor endpoint and response contract behind a
// separately versioned adapter boundary.
type Config struct {
	TokenSource           AccessTokenSource
	HTTPClient            *http.Client
	AdapterVersion        int
	ResponseSchemaVersion int
	RequestTimeout        time.Duration
	RefreshInterval       time.Duration
	RetryInterval         time.Duration
	Now                   func() time.Time

	// The endpoint and response limit are private test seams. Production always
	// uses the pinned HTTPS endpoint and repository-owned maximum.
	endpointURL     string
	maximumResponse int64
}

// Publisher receives only an independently owned AI Snapshot Provider DTO.
type Publisher func(context.Context, aisnapshot.Provider) error

// CloneProvider creates an independently owned copy for publisher boundaries.
func CloneProvider(source aisnapshot.Provider) aisnapshot.Provider {
	cloned := source
	cloned.UpdatedAt = clonePointer(source.UpdatedAt)
	cloned.UpdatedAtUnixMS = clonePointer(source.UpdatedAtUnixMS)
	cloned.StaleAfterSeconds = clonePointer(source.StaleAfterSeconds)
	if source.Balance != nil {
		balance := *source.Balance
		cloned.Balance = &balance
	}
	cloned.Windows = append([]aisnapshot.QuotaWindow(nil), source.Windows...)
	for index := range cloned.Windows {
		window := &cloned.Windows[index]
		original := source.Windows[index]
		window.UsedBasisPoints = clonePointer(original.UsedBasisPoints)
		window.RemainingBasisPoints = clonePointer(original.RemainingBasisPoints)
		window.WindowMinutes = clonePointer(original.WindowMinutes)
		window.ResetsAt = clonePointer(original.ResetsAt)
		window.ResetsAtUnixMS = clonePointer(original.ResetsAtUnixMS)
	}
	if source.Tokens != nil {
		tokens := *source.Tokens
		tokens.Input = clonePointer(source.Tokens.Input)
		tokens.CachedInput = clonePointer(source.Tokens.CachedInput)
		tokens.Output = clonePointer(source.Tokens.Output)
		tokens.Reasoning = clonePointer(source.Tokens.Reasoning)
		tokens.Total = clonePointer(source.Tokens.Total)
		cloned.Tokens = &tokens
	}
	if source.Error != nil {
		problem := *source.Error
		cloned.Error = &problem
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
