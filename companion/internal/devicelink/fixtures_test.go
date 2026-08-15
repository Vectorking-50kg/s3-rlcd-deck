package devicelink

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
)

type fixtureManifest struct {
	SchemaVersion int `json:"schema_version"`
	Fixtures      []struct {
		File                string `json:"file"`
		Kind                string `json:"kind"`
		Accepted            bool   `json:"accepted"`
		PreviousMonotonicMS uint64 `json:"previous_monotonic_ms"`
		HasPrevious         bool   `json:"has_previous"`
	} `json:"fixtures"`
}

func TestSharedDeviceLinkFixtures(t *testing.T) {
	t.Parallel()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate Device Link test source")
	}
	directory := filepath.Clean(filepath.Join(
		filepath.Dir(source),
		"..", "..", "..", "protocol", "fixtures", "device-link-v1",
	))
	manifestBytes, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest fixtureManifest
	if err = json.Unmarshal(manifestBytes, &manifest); err != nil || manifest.SchemaVersion != 1 {
		t.Fatalf("decode fixture manifest: %v", err)
	}
	for _, fixture := range manifest.Fixtures {
		fixture := fixture
		t.Run(fixture.File, func(t *testing.T) {
			message, readErr := os.ReadFile(filepath.Join(directory, fixture.File))
			if readErr != nil {
				t.Fatal(readErr)
			}
			var parseErr error
			switch fixture.Kind {
			case "hello":
				_, parseErr = parseDeviceHello(message, "deck-001122334455")
			case "heartbeat":
				_, parseErr = parseHeartbeat(
					message,
					fixture.PreviousMonotonicMS,
					fixture.HasPrevious,
				)
			case "serial_state":
				_, parseErr = parseSerialState(message)
			case "serial_owner_result":
				_, parseErr = parseSerialOwnerResult(message)
			case "serial_control":
				parseErr = parseSerialControlFixture(message)
			default:
				t.Fatalf("unsupported fixture kind %q", fixture.Kind)
			}
			if accepted := parseErr == nil; accepted != fixture.Accepted {
				t.Fatalf("accepted=%t, want %t: %v", accepted, fixture.Accepted, parseErr)
			}
		})
	}
}

func parseSerialControlFixture(message []byte) error {
	envelope, err := protocol.ParseEnvelope(message)
	if err != nil {
		return err
	}
	switch envelope.Type {
	case MessageSerialOwnerRequest:
		var request SerialOwnerRequest
		if err = protocol.DecodeStrictDocument(message, &request); err != nil {
			return err
		}
		if request.ProtocolVersion != ProtocolVersion || request.SerialSessionID == 0 || request.RequestID == 0 {
			return os.ErrInvalid
		}
	case MessageSerialOwnerActivity:
		var activity SerialOwnerActivity
		if err = protocol.DecodeStrictDocument(message, &activity); err != nil {
			return err
		}
		if activity.ProtocolVersion != ProtocolVersion || activity.SerialSessionID == 0 || activity.LeaseID == 0 {
			return os.ErrInvalid
		}
	case MessageSerialHistoryRequest:
		var request SerialHistoryRequest
		if err = protocol.DecodeStrictDocument(message, &request); err != nil {
			return err
		}
		if request.ProtocolVersion != ProtocolVersion || request.SerialSessionID == 0 {
			return os.ErrInvalid
		}
	default:
		return os.ErrInvalid
	}
	return nil
}
