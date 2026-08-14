// Package history owns privacy-safe, UTC-hour Provider history. Only fields
// from the normalized Provider DTO can cross this package boundary; sessions,
// prompts, raw responses, credentials, and serial data have no representation.
package history

import (
	"errors"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
)

const (
	DefaultRetention = 90 * 24 * time.Hour
	defaultQueueSize = 128
	maximumQueryRows = 20_000
)

var (
	ErrBusy        = errors.New("Provider history queue is full")
	ErrClosed      = errors.New("Provider history is closed")
	ErrInvalid     = errors.New("invalid Provider history request")
	ErrCorrupt     = errors.New("Provider history database is corrupt")
	ErrMigration   = errors.New("Provider history migration failed")
	ErrUnavailable = errors.New("Provider history is unavailable")
)

type Config struct {
	Path      string
	Retention time.Duration
	QueueSize int
}

type Query struct {
	ProviderID string
	From       time.Time
	Until      time.Time
	Limit      int
}

type Record struct {
	ProviderID string                        `json:"provider_id"`
	HourUTC    time.Time                     `json:"hour_utc"`
	ObservedAt time.Time                     `json:"observed_at_utc"`
	Status     aisnapshot.ProviderStatus     `json:"status"`
	ErrorCode  *aisnapshot.ProviderErrorCode `json:"error_code"`
	Balance    *aisnapshot.Money             `json:"balance"`
	Tokens     *aisnapshot.TokenUsage        `json:"tokens"`
	Windows    []QuotaWindow                 `json:"windows"`
}

type QuotaWindow struct {
	Name                 string     `json:"name"`
	UsedBasisPoints      *uint16    `json:"used_basis_points"`
	RemainingBasisPoints *uint16    `json:"remaining_basis_points"`
	WindowMinutes        *uint32    `json:"window_minutes"`
	ResetsAt             *time.Time `json:"resets_at_utc"`
}

type Settings struct {
	Enabled       bool `json:"enabled"`
	RetentionDays int  `json:"retention_days"`
}
