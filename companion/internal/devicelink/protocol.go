package devicelink

import (
	"errors"
	"regexp"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
)

const (
	ProtocolVersion        = int(protocol.CurrentVersion)
	MaxControlMessageBytes = protocol.MaxControlMessageBytes
	Subprotocol            = "s3-rlcd-deck.v1"
	BoardESP32S3RLCD42     = "esp32-s3-rlcd-4.2"
	MessageDeviceHello     = "device.hello"
	MessageHeartbeat       = "device.heartbeat"
)

var safeVersion = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,31}$`)

type DeviceHello struct {
	Type            string   `json:"type"`
	ProtocolVersion int      `json:"protocol_version"`
	DeviceID        string   `json:"device_id"`
	FirmwareVersion string   `json:"firmware_version"`
	Board           string   `json:"board"`
	Capabilities    []string `json:"capabilities"`
	SerialState     string   `json:"serial_state"`
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
		hello.SerialState != "disarmed" || len(hello.Capabilities) == 0 ||
		len(hello.Capabilities) > 8 {
		return DeviceHello{}, errors.New("invalid device.hello")
	}
	seen := make(map[string]struct{}, len(hello.Capabilities))
	for _, capability := range hello.Capabilities {
		switch capability {
		case "display", "serial", "ota":
		default:
			return DeviceHello{}, errors.New("unsupported device capability")
		}
		if _, duplicate := seen[capability]; duplicate {
			return DeviceHello{}, errors.New("duplicate device capability")
		}
		seen[capability] = struct{}{}
	}
	return hello, nil
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
