package devicelink

import (
	"errors"
	"regexp"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
)

const (
	ProtocolVersion             = int(protocol.CurrentVersion)
	MaxControlMessageBytes      = protocol.MaxControlMessageBytes
	Subprotocol                 = "s3-rlcd-deck.v1"
	BoardESP32S3RLCD42          = "esp32-s3-rlcd-4.2"
	MessageDeviceHello          = "device.hello"
	MessageHeartbeat            = "device.heartbeat"
	MessageSerialState          = "serial.state"
	MessageSerialOwnerRequest   = "serial.owner.request"
	MessageSerialOwnerResult    = "serial.owner.result"
	MessageSerialOwnerActivity  = "serial.owner.activity"
	MessageSerialHistoryRequest = "serial.history.request"
	MessageOTAOffer             = "ota.offer"
	MessageOTAChunk             = "ota.chunk"
	MessageOTAResult            = "ota.result"
	MessageDiagnosticsRequest   = "diagnostics.request"
	MessageDiagnosticsSnapshot  = "diagnostics.snapshot"
)

var safeVersion = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,31}$`)
var otaTransactionID = regexp.MustCompile(`^[0-9a-f]{32}$`)

type DeviceHello struct {
	Type            string   `json:"type"`
	ProtocolVersion int      `json:"protocol_version"`
	DeviceID        string   `json:"device_id"`
	FirmwareVersion string   `json:"firmware_version"`
	Board           string   `json:"board"`
	Capabilities    []string `json:"capabilities"`
	SerialState     string   `json:"serial_state"`
	SerialSessionID uint64   `json:"serial_session_id"`
}

type Heartbeat struct {
	Type            string `json:"type"`
	ProtocolVersion int    `json:"protocol_version"`
	UTC             string `json:"utc"`
	MonotonicMS     uint64 `json:"monotonic_ms"`
	TXQueueDepth    uint32 `json:"tx_queue_depth"`
	TXQueueCapacity uint32 `json:"tx_queue_capacity"`
	RXQueueDepth    uint32 `json:"rx_queue_depth"`
	RXQueueCapacity uint32 `json:"rx_queue_capacity"`
}

type SerialState struct {
	Type            string `json:"type"`
	ProtocolVersion int    `json:"protocol_version"`
	SerialState     string `json:"serial_state"`
	SerialSessionID uint64 `json:"serial_session_id"`
	OwnerGeneration uint64 `json:"owner_generation"`
	LeaseID         uint64 `json:"lease_id"`
}

type SerialOwnerRequest struct {
	Type            string `json:"type"`
	ProtocolVersion int    `json:"protocol_version"`
	SerialSessionID uint64 `json:"serial_session_id"`
	RequestID       uint64 `json:"request_id"`
	Enable          bool   `json:"enable"`
}

type SerialOwnerActivity struct {
	Type            string `json:"type"`
	ProtocolVersion int    `json:"protocol_version"`
	SerialSessionID uint64 `json:"serial_session_id"`
	LeaseID         uint64 `json:"lease_id"`
}

type SerialOwnerResult struct {
	Type            string `json:"type"`
	ProtocolVersion int    `json:"protocol_version"`
	SerialSessionID uint64 `json:"serial_session_id"`
	RequestID       uint64 `json:"request_id"`
	Code            string `json:"code"`
	SerialState     string `json:"serial_state"`
	OwnerGeneration uint64 `json:"owner_generation"`
	LeaseID         uint64 `json:"lease_id"`
}

type SerialHistoryRequest struct {
	Type            string `json:"type"`
	ProtocolVersion int    `json:"protocol_version"`
	SerialSessionID uint64 `json:"serial_session_id"`
	AfterSequence   uint64 `json:"after_sequence"`
}

type OTAOffer struct {
	Type                   string `json:"type"`
	ProtocolVersion        int    `json:"protocol_version"`
	TransactionID          string `json:"transaction_id"`
	Version                string `json:"version"`
	Board                  string `json:"board"`
	ImageLength            uint32 `json:"image_length"`
	ImageSHA256            string `json:"image_sha256"`
	Signature              string `json:"signature"`
	SigningKeyID           uint32 `json:"signing_key_id"`
	MinimumProtocolVersion uint32 `json:"minimum_protocol_version"`
}

type OTAChunk struct {
	Type            string `json:"type"`
	ProtocolVersion int    `json:"protocol_version"`
	TransactionID   string `json:"transaction_id"`
	Offset          uint32 `json:"offset"`
	Data            string `json:"data"`
	Final           bool   `json:"final"`
}

type OTAResult struct {
	Type            string `json:"type"`
	ProtocolVersion int    `json:"protocol_version"`
	TransactionID   string `json:"transaction_id"`
	State           string `json:"state"`
	Code            string `json:"code"`
	ReceivedBytes   uint32 `json:"received_bytes"`
	ImageLength     uint32 `json:"image_length"`
}

type DiagnosticsRequest struct {
	Type            string `json:"type"`
	ProtocolVersion int    `json:"protocol_version"`
	RequestID       uint64 `json:"request_id"`
}

type DiagnosticEvent struct {
	MonotonicMS uint64 `json:"monotonic_ms"`
	Level       string `json:"level"`
	Component   string `json:"component"`
	Code        string `json:"code"`
	Value       uint32 `json:"value"`
}

type DiagnosticsSnapshot struct {
	Type            string            `json:"type"`
	ProtocolVersion int               `json:"protocol_version"`
	RequestID       uint64            `json:"request_id"`
	Dropped         uint32            `json:"dropped"`
	Events          []DiagnosticEvent `json:"events"`
}

func parseDeviceHello(message []byte, authenticatedDeviceID string) (DeviceHello, error) {
	envelope, err := protocol.ParseEnvelope(message)
	if err != nil || envelope.Type != MessageDeviceHello {
		return DeviceHello{}, errors.New("first message must be a compatible device.hello")
	}
	var hello DeviceHello
	if err = protocol.DecodeStrictDocument(message, &hello); err != nil {
		return DeviceHello{}, err
	}
	if hello.ProtocolVersion != ProtocolVersion || hello.DeviceID != authenticatedDeviceID ||
		hello.Board != BoardESP32S3RLCD42 || !safeVersion.MatchString(hello.FirmwareVersion) ||
		!validHelloSerialState(hello.SerialState, hello.SerialSessionID) || len(hello.Capabilities) == 0 ||
		len(hello.Capabilities) > 8 {
		return DeviceHello{}, errors.New("invalid device.hello")
	}
	seen := make(map[string]struct{}, len(hello.Capabilities))
	for _, capability := range hello.Capabilities {
		switch capability {
		case "display", "serial", "ota", "diagnostics":
		default:
			return DeviceHello{}, errors.New("unsupported device capability")
		}
		if _, duplicate := seen[capability]; duplicate {
			return DeviceHello{}, errors.New("duplicate device capability")
		}
		seen[capability] = struct{}{}
	}
	if hello.SerialState != "disarmed" {
		if _, serialCapable := seen["serial"]; !serialCapable {
			return DeviceHello{}, errors.New("active Serial Session without serial capability")
		}
	}
	return hello, nil
}

func validHelloSerialState(state string, sessionID uint64) bool {
	if state == "disarmed" {
		return sessionID == 0
	}
	return sessionID != 0 && (state == "usb_tx" || state == "web_tx")
}

func parseHeartbeat(message []byte, previousMonotonic uint64, hasPrevious bool) (Heartbeat, error) {
	envelope, err := protocol.ParseEnvelope(message)
	if err != nil || envelope.Type != MessageHeartbeat {
		return Heartbeat{}, errors.New("only device.heartbeat is valid after device.hello")
	}
	var heartbeat Heartbeat
	if err = protocol.DecodeStrictDocument(message, &heartbeat); err != nil {
		return Heartbeat{}, err
	}
	parsedUTC, err := time.Parse(time.RFC3339Nano, heartbeat.UTC)
	canonicalUTC := err == nil && parsedUTC.Location() == time.UTC &&
		parsedUTC.Format(time.RFC3339Nano) == heartbeat.UTC
	if heartbeat.ProtocolVersion != ProtocolVersion || !canonicalUTC ||
		heartbeat.TXQueueCapacity == 0 || heartbeat.RXQueueCapacity == 0 ||
		heartbeat.TXQueueDepth > heartbeat.TXQueueCapacity ||
		heartbeat.RXQueueDepth > heartbeat.RXQueueCapacity ||
		(hasPrevious && heartbeat.MonotonicMS < previousMonotonic) {
		return Heartbeat{}, errors.New("invalid device.heartbeat")
	}
	return heartbeat, nil
}

func parseSerialState(message []byte) (SerialState, error) {
	envelope, err := protocol.ParseEnvelope(message)
	if err != nil || envelope.Type != MessageSerialState {
		return SerialState{}, errors.New("message is not serial.state")
	}
	var state SerialState
	if err = protocol.DecodeStrictDocument(message, &state); err != nil {
		return SerialState{}, err
	}
	if state.ProtocolVersion != ProtocolVersion ||
		!validHelloSerialState(state.SerialState, state.SerialSessionID) ||
		(state.SerialState != "web_tx" && state.LeaseID != 0) ||
		(state.SerialState == "web_tx" && state.LeaseID == 0) {
		return SerialState{}, errors.New("invalid serial.state")
	}
	return state, nil
}

func parseSerialOwnerResult(message []byte) (SerialOwnerResult, error) {
	envelope, err := protocol.ParseEnvelope(message)
	if err != nil || envelope.Type != MessageSerialOwnerResult {
		return SerialOwnerResult{}, errors.New("message is not serial.owner.result")
	}
	var result SerialOwnerResult
	if err = protocol.DecodeStrictDocument(message, &result); err != nil {
		return SerialOwnerResult{}, err
	}
	validCode := result.Code == "applied" || result.Code == "no_change" ||
		result.Code == "stale_session" || result.Code == "stale_request" ||
		result.Code == "uart_install_failed" || result.Code == "uart_uninstall_failed" ||
		result.Code == "invalid"
	if result.ProtocolVersion != ProtocolVersion ||
		result.RequestID == 0 || !validCode ||
		!validHelloSerialState(result.SerialState, result.SerialSessionID) ||
		(result.SerialState != "web_tx" && result.LeaseID != 0) ||
		(result.SerialState == "web_tx" && result.LeaseID == 0) {
		return SerialOwnerResult{}, errors.New("invalid serial.owner.result")
	}
	return result, nil
}

func parseOTAResult(message []byte) (OTAResult, error) {
	envelope, err := protocol.ParseEnvelope(message)
	if err != nil || envelope.Type != MessageOTAResult {
		return OTAResult{}, errors.New("message is not ota.result")
	}
	var result OTAResult
	if err = protocol.DecodeStrictDocument(message, &result); err != nil {
		return OTAResult{}, err
	}
	validState := result.State == "receiving" ||
		result.State == "ready_to_reboot" || result.State == "failed"
	validCode := result.Code == "ok" || result.Code == "invalid_manifest" ||
		result.Code == "wrong_board" || result.Code == "incompatible_protocol" ||
		result.Code == "downgrade_rejected" || result.Code == "image_too_large" ||
		result.Code == "signature_rejected" || result.Code == "busy" ||
		result.Code == "stale_offset" || result.Code == "flash_failure" ||
		result.Code == "hash_mismatch" || result.Code == "image_invalid" ||
		result.Code == "timed_out"
	if result.ProtocolVersion != ProtocolVersion ||
		!otaTransactionID.MatchString(result.TransactionID) ||
		!validState || !validCode || result.ReceivedBytes > result.ImageLength ||
		result.ImageLength > 1_740_800 ||
		(result.Code == "ok" && result.State == "failed") ||
		(result.Code != "ok" && result.State != "failed") ||
		(result.Code == "ok" && result.ImageLength == 0) ||
		(result.State == "ready_to_reboot" &&
			(result.Code != "ok" || result.ReceivedBytes != result.ImageLength)) {
		return OTAResult{}, errors.New("invalid ota.result")
	}
	return result, nil
}

func parseDiagnosticsSnapshot(message []byte) (DiagnosticsSnapshot, error) {
	envelope, err := protocol.ParseEnvelope(message)
	if err != nil || envelope.Type != MessageDiagnosticsSnapshot {
		return DiagnosticsSnapshot{}, errors.New("message is not diagnostics.snapshot")
	}
	var snapshot DiagnosticsSnapshot
	if err = protocol.DecodeStrictDocument(message, &snapshot); err != nil {
		return DiagnosticsSnapshot{}, err
	}
	if snapshot.ProtocolVersion != ProtocolVersion || snapshot.RequestID == 0 ||
		len(snapshot.Events) > 64 {
		return DiagnosticsSnapshot{}, errors.New("invalid diagnostics.snapshot")
	}
	var previous uint64
	for index, event := range snapshot.Events {
		if !validDiagnosticLevel(event.Level) || !validDiagnosticComponent(event.Component) ||
			!validDiagnosticCode(event.Code) || (index != 0 && event.MonotonicMS < previous) {
			return DiagnosticsSnapshot{}, errors.New("invalid diagnostics.snapshot event")
		}
		previous = event.MonotonicMS
	}
	return snapshot, nil
}

func validDiagnosticLevel(value string) bool {
	return value == "info" || value == "warning" || value == "error"
}

func validDiagnosticComponent(value string) bool {
	switch value {
	case "system", "display", "wifi", "setup", "sensor", "device_link", "serial", "ota":
		return true
	default:
		return false
	}
}

func validDiagnosticCode(value string) bool {
	switch value {
	case "boot", "ready", "unavailable", "connected", "disconnected", "storage_error",
		"protocol_error", "timeout", "owner_changed", "update_started", "update_failed",
		"rollback", "queue_overflow":
		return true
	default:
		return false
	}
}
