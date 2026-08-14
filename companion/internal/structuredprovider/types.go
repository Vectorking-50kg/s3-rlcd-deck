// Package structuredprovider collects quota and balance data from explicitly
// configured HTTP endpoints without executing user-supplied code or retaining
// raw responses.
package structuredprovider

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
)

const (
	AdapterVersion         = 1
	DefaultRequestTimeout  = 10 * time.Second
	DefaultMaximumResponse = 256 << 10
)

var (
	ErrAlreadyRunning   = errors.New("structured Provider collector is already running")
	ErrInvalidConfig    = errors.New("invalid structured Provider configuration")
	ErrInvalidCurl      = errors.New("curl import is not a supported safe request")
	ErrAuthStale        = errors.New("structured Provider credential was rejected")
	ErrPermission       = errors.New("structured Provider permission denied")
	ErrSchemaChanged    = errors.New("structured Provider response schema changed")
	ErrNetworkPolicy    = errors.New("structured Provider target violates network policy")
	ErrResponseTooLarge = errors.New("structured Provider response is too large")
	ErrUnavailable      = errors.New("structured Provider is unavailable")
)

type Method string

const (
	MethodGET  Method = "GET"
	MethodPOST Method = "POST"
)

type ResetFormat string

const (
	ResetRFC3339          ResetFormat = "rfc3339"
	ResetUnixSeconds      ResetFormat = "unix_seconds"
	ResetUnixMilliseconds ResetFormat = "unix_milliseconds"
)

// Header is a structured request header. Its value is always resolved from the
// platform secret store for one request and is never persisted in Definition.
type Header struct {
	Name            string `json:"name"`
	SecretReference string `json:"secret_reference,omitempty"`
	Prefix          string `json:"prefix,omitempty"`
}

type Request struct {
	Method  Method          `json:"method"`
	URL     string          `json:"url"`
	Headers []Header        `json:"headers"`
	Body    json.RawMessage `json:"body,omitempty"`
}

// Mapping is the deliberately small JSONPath projection into the shared
// Provider DTO. BalanceDivisor defaults to one and supports upstream fixed
// point units such as AIHubMix quota/500000.
type Mapping struct {
	BalancePath    string      `json:"balance_path,omitempty"`
	UsedPath       string      `json:"used_path,omitempty"`
	TotalPath      string      `json:"total_path,omitempty"`
	ResetPath      string      `json:"reset_path,omitempty"`
	CurrencyPath   string      `json:"currency_path,omitempty"`
	FixedCurrency  string      `json:"fixed_currency,omitempty"`
	BalanceDivisor uint64      `json:"balance_divisor,omitempty"`
	WindowName     string      `json:"window_name,omitempty"`
	ResetFormat    ResetFormat `json:"reset_format,omitempty"`
}

type Definition struct {
	ID                    string  `json:"id"`
	DisplayName           string  `json:"display_name"`
	Experimental          bool    `json:"experimental"`
	Request               Request `json:"request"`
	Mapping               Mapping `json:"mapping"`
	RefreshMinutes        uint16  `json:"refresh_minutes"`
	RequestTimeoutSeconds uint16  `json:"request_timeout_seconds,omitempty"`
	MaximumResponseBytes  int64   `json:"maximum_response_bytes,omitempty"`
}

// SecretResolver returns caller-owned bytes. The collector overwrites them
// after constructing the one request for which they were resolved.
type SecretResolver interface {
	Resolve(context.Context, string) ([]byte, error)
}

type Publisher func(context.Context, aisnapshot.Provider) error

type Diagnostic struct {
	ProviderID       string
	HTTPStatus       int
	LatencyMillis    int64
	AdapterVersion   int
	ResponseAccepted bool
	ErrorCode        string
}

type DiagnosticSink func(Diagnostic)

// Preview contains normalized, display-safe state only. It never contains raw
// headers, credentials, URLs, response bodies, or upstream account fields.
type Preview struct {
	Provider   aisnapshot.Provider
	Diagnostic Diagnostic
	Warning    string
}

type Config struct {
	Definition Definition
	Secrets    SecretResolver
	Now        func() time.Time
	Diagnostic DiagnosticSink

	resolver       ipResolver
	dialer         dialContextFunc
	client         httpClient
	requestTimeout time.Duration
}

type Template struct {
	ID          string
	DisplayName string
	Definition  Definition
	SecretSlots []string
}

type ImportedSecret struct {
	Reference string
	Value     []byte
}

// CurlImport separates sensitive header values from the persisted request
// definition. The caller must transfer Secrets into a platform secret store
// and overwrite the returned byte slices.
type CurlImport struct {
	Request Request
	Secrets []ImportedSecret
}
