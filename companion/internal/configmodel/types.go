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
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MaximumDeviceProfiles    = 32
	MaximumSerialPresets     = 32
	maximumCapabilities      = 8
	DefaultManagementAddress = "127.0.0.1:7777"
)

var (
	deviceIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{7,63}$`)
	firmwarePattern   = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,31}$`)
	boardPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,47}$`)
	capabilityPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	serialPresetID    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
)

type WebSettings struct {
	ManagementAddress string `json:"management_address"`
	AllowLAN          bool   `json:"allow_lan"`
	AllowedOrigin     string `json:"allowed_origin"`
}

type ApplicationSettings struct {
	HistoryEnabled bool           `json:"history_enabled"`
	SerialPresets  []SerialPreset `json:"serial_presets"`
}

type SerialPresetMode string

const (
	SerialPresetText SerialPresetMode = "text"
	SerialPresetHex  SerialPresetMode = "hex"
)

type SerialLineEnding string

const (
	SerialLineEndingCurrent SerialLineEnding = "current"
	SerialLineEndingCRLF    SerialLineEnding = "crlf"
	SerialLineEndingLF      SerialLineEnding = "lf"
	SerialLineEndingCR      SerialLineEnding = "cr"
	SerialLineEndingNone    SerialLineEnding = "none"
)

type SerialPreset struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Mode       SerialPresetMode `json:"mode"`
	Payload    []byte           `json:"payload"`
	LineEnding SerialLineEnding `json:"line_ending"`
}

func ValidateSerialPresets(presets []SerialPreset) bool {
	if len(presets) > MaximumSerialPresets {
		return false
	}
	identifiers := make(map[string]struct{}, len(presets))
	for _, preset := range presets {
		if !serialPresetID.MatchString(preset.ID) || !validPresetName(preset.Name) {
			return false
		}
		if _, exists := identifiers[preset.ID]; exists {
			return false
		}
		identifiers[preset.ID] = struct{}{}
		endingBytes := serialLineEndingBytes(preset.LineEnding)
		if endingBytes < 0 {
			return false
		}
		switch preset.Mode {
		case SerialPresetText:
			if !utf8.Valid(preset.Payload) || len(preset.Payload) == 0 ||
				len(preset.Payload)+endingBytes > 256 {
				return false
			}
		case SerialPresetHex:
			if preset.LineEnding != SerialLineEndingNone || len(preset.Payload) == 0 ||
				len(preset.Payload) > 256 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func validPresetName(name string) bool {
	if !utf8.ValidString(name) || strings.TrimSpace(name) == "" ||
		len(name) > 192 || utf8.RuneCountInString(name) > 48 {
		return false
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func serialLineEndingBytes(ending SerialLineEnding) int {
	switch ending {
	case SerialLineEndingCurrent, SerialLineEndingCRLF:
		return 2
	case SerialLineEndingLF, SerialLineEndingCR:
		return 1
	case SerialLineEndingNone:
		return 0
	default:
		return -1
	}
}

func CloneSerialPresets(source []SerialPreset) []SerialPreset {
	if source == nil {
		return nil
	}
	result := make([]SerialPreset, len(source))
	for index := range source {
		result[index] = source[index]
		result[index].Payload = append([]byte(nil), source[index].Payload...)
	}
	return result
}

func DestroySerialPresets(presets []SerialPreset) {
	for index := range presets {
		clear(presets[index].Payload)
		presets[index].Payload = nil
	}
}

func CloneApplicationSettings(source ApplicationSettings) ApplicationSettings {
	source.SerialPresets = CloneSerialPresets(source.SerialPresets)
	return source
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
