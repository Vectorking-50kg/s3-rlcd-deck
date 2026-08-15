// Package diagnostics owns the Companion's bounded, redacted diagnostic
// event stream. Its public event schema deliberately has no arbitrary text,
// path, request, response, credential, prompt, tool-argument, or serial-body
// field; callers must choose fixed semantic codes and bounded numeric facts.
package diagnostics

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"time"
)

type Level string

const (
	LevelInfo    Level = "info"
	LevelWarning Level = "warning"
	LevelError   Level = "error"
)

type Module string

const (
	ModuleRuntime     Module = "runtime"
	ModuleProvider    Module = "provider"
	ModuleDeviceLink  Module = "device_link"
	ModuleHistory     Module = "history"
	ModuleOTA         Module = "ota"
	ModuleSerial      Module = "serial"
	ModuleDiagnostics Module = "diagnostics"
)

type Code string

const (
	CodeRuntimeReady       Code = "runtime_ready"
	CodeRuntimeStopped     Code = "runtime_stopped"
	CodeProviderRequest    Code = "provider_request"
	CodeDeviceConnected    Code = "device_connected"
	CodeDeviceDisconnected Code = "device_disconnected"
	CodeOperationFailed    Code = "operation_failed"
	CodeQueueOverflow      Code = "queue_overflow"
	CodePanicRecovered     Code = "panic_recovered"
	CodeBundleExported     Code = "bundle_exported"
)

type ErrorCode string

const (
	ErrorTimeout          ErrorCode = "timeout"
	ErrorAuthStale        ErrorCode = "auth_stale"
	ErrorPermissionDenied ErrorCode = "permission_denied"
	ErrorSchemaChanged    ErrorCode = "schema_changed"
	ErrorNetworkPolicy    ErrorCode = "network_policy"
	ErrorUnavailable      ErrorCode = "unavailable"
	ErrorStorage          ErrorCode = "storage"
	ErrorProtocol         ErrorCode = "protocol"
)

type Event struct {
	Level          Level     `json:"level"`
	Module         Module    `json:"module"`
	Code           Code      `json:"code"`
	HTTPStatus     uint16    `json:"http_status,omitempty"`
	LatencyMS      uint32    `json:"latency_ms,omitempty"`
	SchemaVersion  string    `json:"schema_version,omitempty"`
	ErrorCode      ErrorCode `json:"error_code,omitempty"`
	IdentifierHash string    `json:"identifier_hash,omitempty"`
	Count          uint32    `json:"count,omitempty"`
}

type ProviderDiagnostic struct {
	ProviderID    string
	HTTPStatus    int
	LatencyMS     int64
	SchemaVersion string
	ErrorCode     string
}

func (service *Service) RecordProvider(diagnostic ProviderDiagnostic) bool {
	if service == nil || diagnostic.ProviderID == "" || diagnostic.HTTPStatus < 0 ||
		diagnostic.HTTPStatus > 599 ||
		(diagnostic.HTTPStatus != 0 && diagnostic.HTTPStatus < 100) || diagnostic.LatencyMS < 0 ||
		diagnostic.LatencyMS > int64((10*time.Minute)/time.Millisecond) {
		return false
	}
	errorCode := ErrorCode(diagnostic.ErrorCode)
	level := LevelInfo
	if errorCode != "" {
		level = LevelWarning
	}
	return service.Record(Event{
		Level: level, Module: ModuleProvider, Code: CodeProviderRequest,
		HTTPStatus: uint16(diagnostic.HTTPStatus), LatencyMS: uint32(diagnostic.LatencyMS),
		SchemaVersion: diagnostic.SchemaVersion, ErrorCode: errorCode,
		IdentifierHash: HashIdentifier(diagnostic.ProviderID),
	})
}

type storedEvent struct {
	TimestampUTC string `json:"timestamp_utc"`
	Event
}

var safeSchemaVersion = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,31}$`)
var safeBuildVersion = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`)
var safeCommit = regexp.MustCompile(`^(unknown|[0-9a-f]{7,40})$`)
var safeSchemaKey = regexp.MustCompile(`^[a-z][a-z0-9_.\[\]-]{0,95}$`)

var validLevels = map[Level]struct{}{
	LevelInfo: {}, LevelWarning: {}, LevelError: {},
}

var validModules = map[Module]struct{}{
	ModuleRuntime: {}, ModuleProvider: {}, ModuleDeviceLink: {}, ModuleHistory: {},
	ModuleOTA: {}, ModuleSerial: {}, ModuleDiagnostics: {},
}

var validCodes = map[Code]struct{}{
	CodeRuntimeReady: {}, CodeRuntimeStopped: {}, CodeProviderRequest: {},
	CodeDeviceConnected: {}, CodeDeviceDisconnected: {}, CodeOperationFailed: {},
	CodeQueueOverflow: {}, CodePanicRecovered: {}, CodeBundleExported: {},
}

var validModuleCodes = map[Module]map[Code]struct{}{
	ModuleRuntime: {
		CodeRuntimeReady: {}, CodeRuntimeStopped: {}, CodeOperationFailed: {},
		CodePanicRecovered: {},
	},
	ModuleProvider: {CodeProviderRequest: {}},
	ModuleDeviceLink: {
		CodeDeviceConnected: {}, CodeDeviceDisconnected: {}, CodeOperationFailed: {},
	},
	ModuleHistory:     {CodeOperationFailed: {}},
	ModuleOTA:         {CodeOperationFailed: {}},
	ModuleSerial:      {CodeOperationFailed: {}},
	ModuleDiagnostics: {CodeQueueOverflow: {}, CodeBundleExported: {}, CodeOperationFailed: {}},
}

var validErrors = map[ErrorCode]struct{}{
	ErrorTimeout: {}, ErrorAuthStale: {}, ErrorPermissionDenied: {},
	ErrorSchemaChanged: {}, ErrorNetworkPolicy: {}, ErrorUnavailable: {},
	ErrorStorage: {}, ErrorProtocol: {},
}

func validEvent(event Event) bool {
	if _, ok := validLevels[event.Level]; !ok {
		return false
	}
	if _, ok := validModules[event.Module]; !ok {
		return false
	}
	if _, ok := validCodes[event.Code]; !ok {
		return false
	}
	if _, ok := validModuleCodes[event.Module][event.Code]; !ok {
		return false
	}
	if event.ErrorCode != "" {
		if _, ok := validErrors[event.ErrorCode]; !ok {
			return false
		}
	}
	if event.SchemaVersion != "" && !safeSchemaVersion.MatchString(event.SchemaVersion) {
		return false
	}
	if event.IdentifierHash != "" && !validIdentifierHash(event.IdentifierHash) {
		return false
	}
	if event.HTTPStatus > 599 || (event.HTTPStatus != 0 && event.HTTPStatus < 100) ||
		event.LatencyMS > uint32((10*time.Minute)/time.Millisecond) {
		return false
	}
	if event.Module == ModuleProvider {
		return event.Count == 0 &&
			((event.ErrorCode == "" && event.Level == LevelInfo) ||
				(event.ErrorCode != "" && event.Level != LevelInfo))
	}
	if event.Code == CodeQueueOverflow {
		return event.Level == LevelWarning && event.Count != 0 && event.ErrorCode == "" &&
			event.IdentifierHash == ""
	}
	if event.ErrorCode != "" && event.Code != CodeOperationFailed {
		return false
	}
	if event.Code == CodeOperationFailed &&
		(event.ErrorCode == "" || event.Level == LevelInfo) {
		return false
	}
	if event.IdentifierHash != "" && event.Code != CodeDeviceConnected &&
		event.Code != CodeDeviceDisconnected {
		return false
	}
	if (event.Code == CodeRuntimeReady || event.Code == CodeRuntimeStopped ||
		event.Code == CodeDeviceConnected || event.Code == CodeBundleExported) &&
		event.Level != LevelInfo {
		return false
	}
	if event.Code == CodeDeviceDisconnected && event.Level != LevelWarning {
		return false
	}
	if event.Code == CodePanicRecovered && event.Level != LevelError {
		return false
	}
	return event.HTTPStatus == 0 && event.LatencyMS == 0 && event.SchemaVersion == "" &&
		event.Count == 0
}

func HashIdentifier(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:8])
}

func validIdentifierHash(value string) bool {
	if len(value) != 16 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

type DeckLevel string

const (
	DeckLevelInfo    DeckLevel = "info"
	DeckLevelWarning DeckLevel = "warning"
	DeckLevelError   DeckLevel = "error"
)

type DeckComponent string

const (
	DeckComponentSystem     DeckComponent = "system"
	DeckComponentDisplay    DeckComponent = "display"
	DeckComponentWiFi       DeckComponent = "wifi"
	DeckComponentSetup      DeckComponent = "setup"
	DeckComponentSensor     DeckComponent = "sensor"
	DeckComponentDeviceLink DeckComponent = "device_link"
	DeckComponentSerial     DeckComponent = "serial"
	DeckComponentOTA        DeckComponent = "ota"
)

type DeckCode string

const (
	DeckCodeBoot          DeckCode = "boot"
	DeckCodeReady         DeckCode = "ready"
	DeckCodeUnavailable   DeckCode = "unavailable"
	DeckCodeConnected     DeckCode = "connected"
	DeckCodeDisconnected  DeckCode = "disconnected"
	DeckCodeStorageError  DeckCode = "storage_error"
	DeckCodeProtocolError DeckCode = "protocol_error"
	DeckCodeTimeout       DeckCode = "timeout"
	DeckCodeOwnerChanged  DeckCode = "owner_changed"
	DeckCodeUpdateStarted DeckCode = "update_started"
	DeckCodeUpdateFailed  DeckCode = "update_failed"
	DeckCodeRollback      DeckCode = "rollback"
	DeckCodeQueueOverflow DeckCode = "queue_overflow"
)

type DeckEvent struct {
	MonotonicMS uint64        `json:"monotonic_ms"`
	Level       DeckLevel     `json:"level"`
	Component   DeckComponent `json:"component"`
	Code        DeckCode      `json:"code"`
	Value       uint32        `json:"value,omitempty"`
}

type DeckRing struct {
	DeviceIDHash string      `json:"device_id_hash"`
	Dropped      uint32      `json:"dropped"`
	Events       []DeckEvent `json:"events"`
}

var validDeckLevels = map[DeckLevel]struct{}{
	DeckLevelInfo: {}, DeckLevelWarning: {}, DeckLevelError: {},
}
var validDeckComponents = map[DeckComponent]struct{}{
	DeckComponentSystem: {}, DeckComponentDisplay: {}, DeckComponentWiFi: {},
	DeckComponentSetup: {}, DeckComponentSensor: {}, DeckComponentDeviceLink: {},
	DeckComponentSerial: {}, DeckComponentOTA: {},
}
var validDeckCodes = map[DeckCode]struct{}{
	DeckCodeBoot: {}, DeckCodeReady: {}, DeckCodeUnavailable: {}, DeckCodeConnected: {},
	DeckCodeDisconnected: {}, DeckCodeStorageError: {}, DeckCodeProtocolError: {},
	DeckCodeTimeout: {}, DeckCodeOwnerChanged: {}, DeckCodeUpdateStarted: {},
	DeckCodeUpdateFailed: {}, DeckCodeRollback: {}, DeckCodeQueueOverflow: {},
}

func validDeckRing(ring DeckRing) bool {
	if !validIdentifierHash(ring.DeviceIDHash) || len(ring.Events) > 64 {
		return false
	}
	var previous uint64
	for index, event := range ring.Events {
		if _, ok := validDeckLevels[event.Level]; !ok {
			return false
		}
		if _, ok := validDeckComponents[event.Component]; !ok {
			return false
		}
		if _, ok := validDeckCodes[event.Code]; !ok {
			return false
		}
		if index != 0 && event.MonotonicMS < previous {
			return false
		}
		previous = event.MonotonicMS
	}
	return true
}
