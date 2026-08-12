package protocol_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
)

type fixtureManifest struct {
	SchemaVersion int `json:"schema_version"`
	Cases         []struct {
		Name      string             `json:"name"`
		File      string             `json:"file"`
		Encoding  string             `json:"encoding,omitempty"`
		Accepted  bool               `json:"accepted"`
		ErrorCode protocol.ErrorCode `json:"error_code,omitempty"`
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

func TestParseEnvelopeRejectsInvalidUTF8(t *testing.T) {
	message := append([]byte(`{"type":"device.hello","protocol_version":1,"future":"`), 0xff)
	message = append(message, '"', '}')

	_, err := protocol.ParseEnvelope(message)
	if !errors.Is(err, protocol.ErrMalformedEnvelope) {
		t.Fatalf("error = %v, want ErrMalformedEnvelope", err)
	}
}

func TestParseEnvelopeClassifiesMissingAndNullVersionAsMalformed(t *testing.T) {
	for _, message := range [][]byte{
		[]byte(`{"type":"device.hello"}`),
		[]byte(`{"type":"device.hello","protocol_version":null}`),
	} {
		_, err := protocol.ParseEnvelope(message)
		if got := protocol.Code(err); got != protocol.MalformedEnvelopeCode {
			t.Fatalf("Code(ParseEnvelope(%s)) = %q, want %q", message, got, protocol.MalformedEnvelopeCode)
		}
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
			if testCase.Encoding == "hex" {
				message, readErr = hex.DecodeString(string(bytes.TrimSpace(message)))
				if readErr != nil {
					t.Fatalf("decode hex fixture: %v", readErr)
				}
			} else if testCase.Encoding != "" && testCase.Encoding != "json" {
				t.Fatalf("unsupported fixture encoding %q", testCase.Encoding)
			}
			_, parseErr := protocol.ParseEnvelope(message)
			if (parseErr == nil) != testCase.Accepted {
				t.Fatalf("ParseEnvelope() error = %v, accepted = %t", parseErr, testCase.Accepted)
			}
			if !testCase.Accepted && protocol.Code(parseErr) != testCase.ErrorCode {
				t.Fatalf("Code(ParseEnvelope()) = %q, want %q", protocol.Code(parseErr), testCase.ErrorCode)
			}
		})
	}
}

func TestGoConstantsMatchSharedProtocolCatalog(t *testing.T) {
	root := repositoryRoot(t)
	var envelopeSchema struct {
		Properties struct {
			ProtocolVersion struct {
				Constant uint32 `json:"const"`
			} `json:"protocol_version"`
		} `json:"properties"`
		MaximumBytes int `json:"x-maximum-encoded-bytes"`
	}
	readJSONFile(t, filepath.Join(root, "protocol", "schema", "envelope-v1.schema.json"), &envelopeSchema)
	if envelopeSchema.Properties.ProtocolVersion.Constant != protocol.CurrentVersion {
		t.Fatalf("shared protocol version = %d, Go = %d", envelopeSchema.Properties.ProtocolVersion.Constant, protocol.CurrentVersion)
	}
	if envelopeSchema.MaximumBytes != protocol.MaxControlMessageBytes {
		t.Fatalf("shared message limit = %d, Go = %d", envelopeSchema.MaximumBytes, protocol.MaxControlMessageBytes)
	}

	var errorCatalog struct {
		SchemaVersion int                  `json:"schema_version"`
		Codes         []protocol.ErrorCode `json:"codes"`
	}
	readJSONFile(t, filepath.Join(root, "protocol", "schema", "error-codes-v1.json"), &errorCatalog)
	wantCodes := []protocol.ErrorCode{
		protocol.MalformedEnvelopeCode,
		protocol.MessageTooLargeCode,
		protocol.UnsupportedVersionCode,
		protocol.InternalErrorCode,
	}
	if errorCatalog.SchemaVersion != 1 || !slices.Equal(errorCatalog.Codes, wantCodes) {
		t.Fatalf("shared error catalog = %#v, want %#v", errorCatalog, wantCodes)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() could not locate test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", ".."))
}

func readJSONFile(t *testing.T, path string, target any) {
	t.Helper()
	document, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err = json.Unmarshal(document, target); err != nil {
		t.Fatalf("parse %s: %v", path, err)
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
