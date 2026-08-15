// Package installation owns the Companion's per-user installation, login
// startup registration, migration snapshot, and interrupted-upgrade recovery.
// Callers choose an operation; platform command ordering and compensation stay
// behind Manager's small interface.
package installation

import (
	"context"
	"errors"
	"time"
)

const StateSchemaVersion = 1

var (
	ErrInvalid     = errors.New("invalid Companion installation request")
	ErrUnavailable = errors.New("Companion installation is unavailable")
	ErrMigration   = errors.New("Companion data migration failed")
	ErrPlatform    = errors.New("login startup registration failed")
)

type Request struct {
	SourceExecutable string
	Version          string
	Commit           string
	DeviceHubAddress string
}

type Status struct {
	Installed        bool   `json:"installed"`
	Enabled          bool   `json:"enabled"`
	Version          string `json:"version,omitempty"`
	Commit           string `json:"commit,omitempty"`
	ActiveExecutable string `json:"active_executable,omitempty"`
	PreviousVersion  string `json:"previous_version,omitempty"`
	Platform         string `json:"platform"`
}

type Config struct {
	RootDirectory  string
	DataDirectory  string
	Now            func() time.Time
	AvailableBytes func(string) (uint64, error)
}

type launchSpec struct {
	Executable       string
	DataDirectory    string
	DeviceHubAddress string
}

type platformStatus struct {
	Installed bool
	Enabled   bool
}

type platformAdapter interface {
	Name() string
	Configure(context.Context, launchSpec) error
	SetEnabled(context.Context, bool) error
	Remove(context.Context) error
	Status(context.Context) (platformStatus, error)
}

type installationState struct {
	SchemaVersion      uint32 `json:"schema_version"`
	Version            string `json:"version"`
	Commit             string `json:"commit"`
	ActiveExecutable   string `json:"active_executable"`
	PreviousVersion    string `json:"previous_version,omitempty"`
	PreviousExecutable string `json:"previous_executable,omitempty"`
	DeviceHubAddress   string `json:"device_hub_address"`
	Enabled            bool   `json:"enabled"`
}

type transactionJournal struct {
	SchemaVersion uint32            `json:"schema_version"`
	RestoreData   bool              `json:"restore_data"`
	Reconfigure   bool              `json:"reconfigure"`
	BackupPath    string            `json:"backup_path,omitempty"`
	HadPrior      bool              `json:"had_prior"`
	Prior         installationState `json:"prior"`
	Next          installationState `json:"next"`
}
