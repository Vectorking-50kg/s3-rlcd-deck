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
