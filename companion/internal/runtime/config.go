package runtime

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"
)

const (
	defaultManagementAddress = "127.0.0.1:7777"
	defaultDeviceHubAddress  = "127.0.0.1:7780"
	minimumTokenBytes        = 24
)

var (
	ErrManagementAddressNotLoopback = errors.New("management address must be loopback unless LAN management is explicitly enabled")
	ErrTokensMustDiffer             = errors.New("management and Device Hub tokens must differ")
)

type Config struct {
	Version    string
	Management ManagementConfig
	DeviceHub  DeviceHubConfig
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
	Address        string
	BootstrapToken string
	Limits         DeviceHubLimits
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
}

func normalizeConfig(config Config) (Config, error) {
	if config.Version == "" {
		return Config{}, errors.New("companion version is required")
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
	if len(config.DeviceHub.BootstrapToken) < minimumTokenBytes {
		return Config{}, fmt.Errorf("Device Hub bootstrap token must contain at least %d bytes", minimumTokenBytes)
	}
	if constantTimeTokenEqual(config.Management.AdminToken, config.DeviceHub.BootstrapToken) {
		return Config{}, ErrTokensMustDiffer
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
	if _, err = addressIP(config.DeviceHub.Address); err != nil {
		return Config{}, fmt.Errorf("invalid Device Hub address: %w", err)
	}
	config.DeviceHub.Limits = normalizeDeviceHubLimits(config.DeviceHub.Limits)
	config.Management.Limits = normalizeManagementLimits(config.Management.Limits)
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
