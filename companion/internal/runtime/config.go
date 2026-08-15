package runtime

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/backup"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/codexappserver"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/codexobserver"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/configmodel"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/cursorprovider"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/diagnostics"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/history"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairing"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/structuredprovider"
)

const (
	defaultManagementAddress = "127.0.0.1:7777"
	defaultDeviceHubAddress  = "127.0.0.1:7780"
	minimumTokenBytes        = 24
)

var (
	ErrManagementAddressNotLoopback = errors.New("management address must be loopback unless LAN management is explicitly enabled")
	ErrDeviceHubTLSRequired         = errors.New("Device Hub must remain loopback-only until pinned TLS transport is configured")
)

type Config struct {
	Version              string
	Commit               string
	Management           ManagementConfig
	DeviceHub            DeviceHubConfig
	Pairing              *pairing.Service
	CodexCollector       CodexCollector
	CodexObserver        CodexObserver
	CursorCollector      CursorCollector
	StructuredCollectors []StructuredCollector
	StructuredProviders  *structuredprovider.Service
	History              *history.Store
	Backup               BackupService
	Configuration        ConfigurationOwner
	Diagnostics          *diagnostics.Service
}

type BackupService interface {
	Export(context.Context, []byte) ([]byte, error)
	Preview(context.Context, []byte, []byte, backup.ImportMode) (backup.Preview, error)
	Import(
		context.Context,
		[]byte,
		[]byte,
		backup.ImportMode,
		map[string]backup.ConflictDecision,
		string,
	) (backup.ImportResult, error)
}

type ConfigurationOwner interface {
	UpdateApplicationSettings(context.Context, configmodel.ApplicationSettings) error
	UpdateHistoryEnabled(context.Context, bool) error
	SerialPresets(context.Context) ([]configmodel.SerialPreset, error)
	UpdateSerialPresets(context.Context, []configmodel.SerialPreset) error
	UpdateSerialPreset(context.Context, configmodel.SerialPreset) (bool, error)
	DeleteSerialPreset(context.Context, string) (bool, error)
	UpdateDeviceProfile(context.Context, configmodel.DeviceProfile) error
}

// CodexCollector is intentionally narrow: the runtime can supervise normalized
// updates and explicitly load an owned thread, but it cannot reach raw App
// Server responses or process details.
type CodexCollector interface {
	Run(context.Context, codexappserver.Publisher) error
	LoadThread(context.Context, string) error
}

// CodexObserver is deliberately publish-only. It cannot load, resume, or
// otherwise take ownership of a user session.
type CodexObserver interface {
	Run(context.Context, codexobserver.Publisher) error
}

// CursorCollector is publish-only. The runtime cannot read raw Cursor state,
// access credentials, or invoke the private endpoint itself.
type CursorCollector interface {
	Run(context.Context, cursorprovider.Publisher) error
}

// StructuredCollector is publish-only. Runtime never receives the request
// definition, upstream response, URL, headers, or Provider credentials.
type StructuredCollector interface {
	ProviderID() string
	Run(context.Context, structuredprovider.Publisher) error
}

type ManagementConfig struct {
	Address       string
	AllowLAN      bool
	AllowedOrigin string
	AdminToken    string
	Limits        ManagementLimits
}

type ManagementLimits struct {
	MaxHeaderBytes      int
	ReadHeaderTimeout   time.Duration
	ReadTimeout         time.Duration
	WriteTimeout        time.Duration
	IdleTimeout         time.Duration
	MaxConcurrent       int
	MaxConcurrentPerIP  int
	SensitiveRequests   int
	SensitiveRateWindow time.Duration
}

type DeviceHubConfig struct {
	Address               string
	AdvertisedAddress     string
	TLSCertificate        *tls.Certificate
	HeartbeatInterval     time.Duration
	HeartbeatTimeout      time.Duration
	ServerProtocolVersion int
	Limits                DeviceHubLimits
}

type DeviceHubLimits struct {
	MaxHeaderBytes     int
	MaxBodyBytes       int64
	ReadHeaderTimeout  time.Duration
	ReadTimeout        time.Duration
	WriteTimeout       time.Duration
	IdleTimeout        time.Duration
	MaxConcurrent      int
	MaxConcurrentPerIP int
	RateLimitRequests  int
	RateLimitWindow    time.Duration
	PairingAttempts    int
	PairingRateWindow  time.Duration
}

func normalizeConfig(config Config) (Config, error) {
	if config.Version == "" {
		return Config{}, errors.New("companion version is required")
	}
	if config.Commit == "" {
		config.Commit = "unknown"
	}
	if config.Management.Address == "" {
		config.Management.Address = defaultManagementAddress
	}
	if config.DeviceHub.Address == "" {
		config.DeviceHub.Address = defaultDeviceHubAddress
	}
	if len(config.Management.AdminToken) < minimumTokenBytes {
		return Config{}, fmt.Errorf("management admin token must contain at least %d bytes", minimumTokenBytes)
	}
	if config.Pairing == nil {
		return Config{}, errors.New("pairing service is required")
	}
	managementIP, err := addressIP(config.Management.Address)
	if err != nil {
		return Config{}, fmt.Errorf("invalid management address: %w", err)
	}
	if !managementIP.IsLoopback() && !config.Management.AllowLAN {
		return Config{}, ErrManagementAddressNotLoopback
	}
	if config.Management.AllowLAN && config.Management.AllowedOrigin == "" {
		return Config{}, errors.New("LAN management requires an explicit allowed origin")
	}
	if config.Management.AllowedOrigin != "" {
		if err = validateBrowserOrigin(config.Management.AllowedOrigin); err != nil {
			return Config{}, fmt.Errorf("invalid management allowed origin: %w", err)
		}
	}
	deviceHubIP, err := addressIP(config.DeviceHub.Address)
	if err != nil {
		return Config{}, fmt.Errorf("invalid Device Hub address: %w", err)
	}
	hasTLSCertificate := config.DeviceHub.TLSCertificate != nil &&
		len(config.DeviceHub.TLSCertificate.Certificate) != 0 &&
		config.DeviceHub.TLSCertificate.PrivateKey != nil
	if config.DeviceHub.TLSCertificate != nil && !hasTLSCertificate {
		return Config{}, errors.New("Device Hub TLS certificate is incomplete")
	}
	if !deviceHubIP.IsLoopback() && !hasTLSCertificate {
		return Config{}, ErrDeviceHubTLSRequired
	}
	if config.DeviceHub.AdvertisedAddress != "" &&
		!validAdvertisedDeviceHubAddress(config.DeviceHub.AdvertisedAddress) {
		return Config{}, errors.New("Device Hub advertised address must be a routable IP address and non-zero port")
	}
	if config.DeviceHub.HeartbeatInterval < 0 || config.DeviceHub.HeartbeatTimeout < 0 ||
		(config.DeviceHub.HeartbeatTimeout != 0 &&
			config.DeviceHub.HeartbeatInterval >= config.DeviceHub.HeartbeatTimeout) {
		return Config{}, errors.New("Device Hub heartbeat timing is invalid")
	}
	if config.DeviceHub.ServerProtocolVersion < 0 {
		return Config{}, errors.New("Device Hub server protocol version is invalid")
	}
	config.DeviceHub.Limits = normalizeDeviceHubLimits(config.DeviceHub.Limits)
	config.Management.Limits = normalizeManagementLimits(config.Management.Limits)
	if len(config.StructuredCollectors) > 6 {
		return Config{}, errors.New("at most six structured Provider collectors are supported")
	}
	if config.StructuredProviders != nil && len(config.StructuredCollectors) != 0 {
		return Config{}, errors.New("dynamic and fixed structured Provider collectors are mutually exclusive")
	}
	structuredIDs := make(map[string]struct{}, len(config.StructuredCollectors))
	for _, collector := range config.StructuredCollectors {
		if collector == nil || collector.ProviderID() == "" {
			return Config{}, errors.New("structured Provider collector is invalid")
		}
		if _, duplicate := structuredIDs[collector.ProviderID()]; duplicate {
			return Config{}, errors.New("structured Provider IDs must be unique")
		}
		structuredIDs[collector.ProviderID()] = struct{}{}
	}
	config.StructuredCollectors = append([]StructuredCollector(nil), config.StructuredCollectors...)
	return config, nil
}

func normalizeManagementLimits(limits ManagementLimits) ManagementLimits {
	if limits.MaxHeaderBytes <= 0 {
		limits.MaxHeaderBytes = 16 << 10
	}
	if limits.ReadHeaderTimeout <= 0 {
		limits.ReadHeaderTimeout = 2 * time.Second
	}
	if limits.ReadTimeout <= 0 {
		limits.ReadTimeout = 10 * time.Second
	}
	if limits.WriteTimeout <= 0 {
		limits.WriteTimeout = 10 * time.Second
	}
	if limits.IdleTimeout <= 0 {
		limits.IdleTimeout = 30 * time.Second
	}
	if limits.MaxConcurrent <= 0 {
		limits.MaxConcurrent = 64
	}
	if limits.MaxConcurrentPerIP <= 0 {
		limits.MaxConcurrentPerIP = 16
	}
	if limits.SensitiveRequests <= 0 {
		limits.SensitiveRequests = 30
	}
	if limits.SensitiveRateWindow <= 0 {
		limits.SensitiveRateWindow = time.Minute
	}
	return limits
}

func validateBrowserOrigin(origin string) error {
	parsed, err := url.Parse(origin)
	if err != nil {
		return err
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("origin must use http or https and include a host")
	}
	if parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("origin must not include credentials, a path, a query, or a fragment")
	}
	return nil
}

func normalizeDeviceHubLimits(limits DeviceHubLimits) DeviceHubLimits {
	if limits.MaxHeaderBytes <= 0 {
		limits.MaxHeaderBytes = 16 << 10
	}
	if limits.MaxBodyBytes <= 0 {
		limits.MaxBodyBytes = 16 << 10
	}
	if limits.ReadHeaderTimeout <= 0 {
		limits.ReadHeaderTimeout = 2 * time.Second
	}
	if limits.ReadTimeout <= 0 {
		limits.ReadTimeout = 10 * time.Second
	}
	if limits.WriteTimeout <= 0 {
		limits.WriteTimeout = 10 * time.Second
	}
	if limits.IdleTimeout <= 0 {
		limits.IdleTimeout = 30 * time.Second
	}
	if limits.MaxConcurrent <= 0 {
		limits.MaxConcurrent = 32
	}
	if limits.MaxConcurrentPerIP <= 0 {
		limits.MaxConcurrentPerIP = 8
	}
	if limits.RateLimitRequests <= 0 {
		limits.RateLimitRequests = 120
	}
	if limits.RateLimitWindow <= 0 {
		limits.RateLimitWindow = time.Minute
	}
	if limits.PairingAttempts <= 0 {
		limits.PairingAttempts = 10
	}
	if limits.PairingRateWindow <= 0 {
		limits.PairingRateWindow = time.Minute
	}
	return limits
}

func addressIP(address string) (net.IP, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if host == "localhost" {
		return net.ParseIP("127.0.0.1"), nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil, errors.New("listener host must be an IP address")
	}
	return ip, nil
}
