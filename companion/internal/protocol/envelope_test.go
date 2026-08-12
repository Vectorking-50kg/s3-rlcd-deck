package protocol_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
)

type fixtureManifest struct {
	SchemaVersion int `json:"schema_version"`
	Cases         []struct {
		Name     string `json:"name"`
		File     string `json:"file"`
		Accepted bool   `json:"accepted"`
	} `json:"cases"`
}

func TestParseEnvelopeAcceptsProtocolV1Message(t *testing.T) {
	message := []byte(
		`{"type":"device.heartbeat","protocol_version":1,"queue_watermark":7}`,
	)

	envelope, err := protocol.ParseEnvelope(message)
	if err != nil {
		t.Fatalf("ParseEnvelope() error = %v", err)
	}
	if envelope.Type != "device.heartbeat" {
		t.Fatalf("Type = %q, want device.heartbeat", envelope.Type)
	}
	if envelope.ProtocolVersion != 1 {
		t.Fatalf("ProtocolVersion = %d, want 1", envelope.ProtocolVersion)
	}
}

func TestParseEnvelopeRejectsOversizedControlMessage(t *testing.T) {
	message := bytes.Repeat([]byte{' '}, protocol.MaxControlMessageBytes+1)

	_, err := protocol.ParseEnvelope(message)
	if !errors.Is(err, protocol.ErrMessageTooLarge) {
		t.Fatalf("error = %v, want ErrMessageTooLarge", err)
	}
}

func TestParseEnvelopeRejectsUnsupportedMajorVersion(t *testing.T) {
	message := []byte(`{"type":"device.heartbeat","protocol_version":2}`)

	_, err := protocol.ParseEnvelope(message)
	if !errors.Is(err, protocol.ErrUnsupportedVersion) {
		t.Fatalf("error = %v, want ErrUnsupportedVersion", err)
	}
}

func TestParseEnvelopeRejectsNonCanonicalMessageType(t *testing.T) {
	message := []byte(`{"type":"Device Hello","protocol_version":1}`)

	_, err := protocol.ParseEnvelope(message)
	if !errors.Is(err, protocol.ErrMalformedEnvelope) {
		t.Fatalf("error = %v, want ErrMalformedEnvelope", err)
	}
}

func TestParseEnvelopeRejectsDuplicateEnvelopeFields(t *testing.T) {
	message := []byte(
		`{"type":"device.hello","type":"device.heartbeat","protocol_version":1}`,
	)

	_, err := protocol.ParseEnvelope(message)
	if !errors.Is(err, protocol.ErrMalformedEnvelope) {
		t.Fatalf("error = %v, want ErrMalformedEnvelope", err)
	}
}

func TestParseEnvelopeRejectsDuplicateNestedFields(t *testing.T) {
	message := []byte(
		`{"type":"device.hello","protocol_version":1,"capability":{"name":"display","name":"serial"}}`,
	)

	_, err := protocol.ParseEnvelope(message)
	if !errors.Is(err, protocol.ErrMalformedEnvelope) {
		t.Fatalf("error = %v, want ErrMalformedEnvelope", err)
	}
}

func TestEnvelopeFixturesDefineTheCrossEndContract(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() could not locate test source")
	}
	fixtureDirectory := filepath.Join(
		filepath.Dir(sourceFile), "..", "..", "..", "protocol", "fixtures", "envelope",
	)
	manifestBytes, err := os.ReadFile(filepath.Join(fixtureDirectory, "manifest.json"))
	if err != nil {
		t.Fatalf("read fixture manifest: %v", err)
	}
	var manifest fixtureManifest
	if err = json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("parse fixture manifest: %v", err)
	}
	if manifest.SchemaVersion != 1 || len(manifest.Cases) < 2 {
		t.Fatalf("fixture manifest = %#v, want schema 1 with accepted and rejected cases", manifest)
	}
	for _, testCase := range manifest.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			message, readErr := os.ReadFile(filepath.Join(fixtureDirectory, testCase.File))
			if readErr != nil {
				t.Fatalf("read fixture: %v", readErr)
			}
			_, parseErr := protocol.ParseEnvelope(message)
			if (parseErr == nil) != testCase.Accepted {
				t.Fatalf("ParseEnvelope() error = %v, accepted = %t", parseErr, testCase.Accepted)
			}
		})
	}
}

func TestErrorCodeUsesStableWireNames(t *testing.T) {
	testCases := []struct {
		err  error
		want protocol.ErrorCode
	}{
		{protocol.ErrMalformedEnvelope, "malformed_envelope"},
		{protocol.ErrMessageTooLarge, "message_too_large"},
		{protocol.ErrUnsupportedVersion, "unsupported_protocol_version"},
	}
	for _, testCase := range testCases {
		if got := protocol.Code(testCase.err); got != testCase.want {
			t.Fatalf("Code(%v) = %q, want %q", testCase.err, got, testCase.want)
		}
	}
}
