// Package configmodel defines the non-secret Companion settings that may cross
// the encrypted backup boundary. It intentionally has no storage or runtime
// dependencies so configuration, backup, and Device Link can share one
// validation contract.
package configmodel

import (
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"
)

const (
	MaximumDeviceProfiles    = 32
	maximumCapabilities      = 8
	DefaultManagementAddress = "127.0.0.1:7777"
)

var (
	deviceIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{7,63}$`)
	firmwarePattern   = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,31}$`)
	boardPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,47}$`)
	capabilityPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
)

type WebSettings struct {
	ManagementAddress string `json:"management_address"`
	AllowLAN          bool   `json:"allow_lan"`
	AllowedOrigin     string `json:"allowed_origin"`
}

type ApplicationSettings struct {
	HistoryEnabled bool `json:"history_enabled"`
}

// DeviceProfile is a non-secret cache projection. It intentionally excludes
// pairing verifiers, Tokens, certificates, addresses, and connection state.
type DeviceProfile struct {
	DeviceID        string   `json:"device_id"`
	FirmwareVersion string   `json:"firmware_version"`
	Board           string   `json:"board"`
	Capabilities    []string `json:"capabilities"`
	LastSeenUTC     string   `json:"last_seen_utc"`
}

func ValidateWebSettings(settings WebSettings) bool {
	host, portText, err := net.SplitHostPort(settings.ManagementAddress)
	port, portErr := strconv.Atoi(portText)
	if err != nil || portErr != nil || port < 1 || port > 65535 ||
		(host != "localhost" && net.ParseIP(host) == nil) {
		return false
	}
	listenerIP := net.ParseIP(host)
	if host == "localhost" {
		listenerIP = net.ParseIP("127.0.0.1")
	}
	if !listenerIP.IsLoopback() && !settings.AllowLAN {
		return false
	}
	if settings.AllowedOrigin == "" {
		return !settings.AllowLAN
	}
	if !utf8.ValidString(settings.AllowedOrigin) || len(settings.AllowedOrigin) > 2048 {
		return false
	}
	origin, err := url.Parse(settings.AllowedOrigin)
	return err == nil && (origin.Scheme == "http" || origin.Scheme == "https") &&
		origin.Host != "" && origin.User == nil && origin.Path == "" &&
		origin.RawQuery == "" && origin.Fragment == ""
}

func ValidateDeviceProfile(profile DeviceProfile) bool {
	if !deviceIDPattern.MatchString(profile.DeviceID) ||
		!firmwarePattern.MatchString(profile.FirmwareVersion) ||
		!boardPattern.MatchString(profile.Board) || profile.Capabilities == nil ||
		len(profile.Capabilities) == 0 || len(profile.Capabilities) > maximumCapabilities ||
		!CanonicalUTC(profile.LastSeenUTC) {
		return false
	}
	if !slices.IsSorted(profile.Capabilities) {
		return false
	}
	for index, capability := range profile.Capabilities {
		if !capabilityPattern.MatchString(capability) ||
			(index > 0 && capability == profile.Capabilities[index-1]) {
			return false
		}
	}
	return true
}

func CanonicalUTC(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Location() == time.UTC && parsed.Year() >= 1970 &&
		parsed.Format(time.RFC3339Nano) == value
}

func CloneDeviceProfiles(source []DeviceProfile) []DeviceProfile {
	if source == nil {
		return nil
	}
	result := make([]DeviceProfile, len(source))
	for index := range source {
		result[index] = source[index]
		result[index].Capabilities = append([]string(nil), source[index].Capabilities...)
	}
	return result
}
