package pairingv2

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type pairingV2FixtureManifest struct {
	SchemaVersion   int `json:"schema_version"`
	ProtocolVersion int `json:"protocol_version"`
	Cases           []struct {
		File     string `json:"file"`
		Accepted bool   `json:"accepted"`
		Type     string `json:"type"`
	} `json:"cases"`
}

func validCredentialsDocument(t *testing.T) []byte {
	t.Helper()
	certificate := []byte("pairing-v2-test-certificate")
	digest := sha256.Sum256(certificate)
	return []byte(fmt.Sprintf(`{"type":"pairing.credentials","protocol_version":2,"session_id":"00112233445566778899aabbccddeeff","transaction_id":"ffeeddccbbaa99887766554433221100","sequence":1,"window_nonce":"0123456789abcdef0123456789abcdef","companion_nonce":"abcdef0123456789abcdef0123456789","hub_service":"s3deck-companion-a1b2._s3rlcd-hub._tcp.local.","hub_address":"192.168.31.3:7780","token":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","certificate_fingerprint":"sha256:%x","certificate_der":"%s","device_link_protocol":1}`,
		digest, base64.StdEncoding.EncodeToString(certificate)))
}

func TestDecodeContractCredentialsValidatesCertificateAndNetworkBoundary(t *testing.T) {
	message, err := DecodeContractMessage(validCredentialsDocument(t))
	if err != nil {
		t.Fatalf("DecodeContractMessage() error = %v", err)
	}
	credentials, ok := message.(*Credentials)
	if !ok || credentials.Sequence != 1 || credentials.HubAddress != "192.168.31.3:7780" {
		t.Fatalf("credentials = %#v", message)
	}

	for name, replacement := range map[string]string{
		"public address": `"hub_address":"203.0.113.5:7780"`,
		"wrong digest":   `"certificate_fingerprint":"sha256:` + strings.Repeat("0", 64) + `"`,
		"bad token":      `"token":"short"`,
		"wrong sequence": `"sequence":9`,
	} {
		t.Run(name, func(t *testing.T) {
			document := string(validCredentialsDocument(t))
			switch name {
			case "public address":
				document = strings.Replace(document, `"hub_address":"192.168.31.3:7780"`, replacement, 1)
			case "wrong digest":
				start := strings.Index(document, `"certificate_fingerprint":`)
				end := strings.Index(document[start+27:], `,"certificate_der"`)
				document = document[:start] + replacement + document[start+27+end:]
			case "bad token":
				document = strings.Replace(document, `"token":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"`, replacement, 1)
			case "wrong sequence":
				document = strings.Replace(document, `"sequence":1`, replacement, 1)
			}
			if _, err := DecodeContractMessage([]byte(document)); err == nil {
				t.Fatal("malformed credentials accepted")
			}
		})
	}
}

func TestDecodeContractRejectsUnknownDuplicateAndUnsupportedDocuments(t *testing.T) {
	valid := string(validCredentialsDocument(t))
	tests := [][]byte{
		[]byte(strings.Replace(valid, `"sequence":1`, `"sequence":1,"sequence":1`, 1)),
		[]byte(strings.Replace(valid, `"sequence":1`, `"sequence":1,"secret_extra":"x"`, 1)),
		[]byte(strings.Replace(valid, `"protocol_version":2`, `"protocol_version":3`, 1)),
		[]byte(strings.Replace(valid, `"type":"pairing.credentials"`, `"type":"pairing.unknown"`, 1)),
	}
	for index, document := range tests {
		if _, err := DecodeContractMessage(document); err == nil {
			t.Fatalf("case %d accepted", index)
		}
	}
}

func TestDecodeContractAcceptsEveryTransactionMessageAndEnforcesSequence(t *testing.T) {
	documents := []string{
		`{"type":"pairing.commit_ready","protocol_version":2,"session_id":"00112233445566778899aabbccddeeff","transaction_id":"ffeeddccbbaa99887766554433221100","sequence":2,"window_nonce":"0123456789abcdef0123456789abcdef","companion_nonce":"abcdef0123456789abcdef0123456789","deck_nonce":"11111111111111111111111111111111","device_id":"deck_12345678","device_identity":"ZGV2aWNlLWlkZW50aXR5LTE","profile_id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","transcript_sha256":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`,
		`{"type":"pairing.commit","protocol_version":2,"session_id":"00112233445566778899aabbccddeeff","transaction_id":"ffeeddccbbaa99887766554433221100","sequence":3,"deck_nonce":"11111111111111111111111111111111","transcript_sha256":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`,
		`{"type":"pairing.commit_receipt","protocol_version":2,"session_id":"00112233445566778899aabbccddeeff","transaction_id":"ffeeddccbbaa99887766554433221100","sequence":4,"profile_id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","profile_generation":7,"transcript_sha256":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`,
		`{"type":"pairing.status_request","protocol_version":2,"session_id":"00112233445566778899aabbccddeeff","transaction_id":"ffeeddccbbaa99887766554433221100","sequence":5}`,
		`{"type":"pairing.status","protocol_version":2,"session_id":"00112233445566778899aabbccddeeff","transaction_id":"ffeeddccbbaa99887766554433221100","sequence":6,"state":"connecting","error_code":"none","transcript_sha256":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`,
		`{"type":"pairing.cancel","protocol_version":2,"session_id":"00112233445566778899aabbccddeeff","transaction_id":"ffeeddccbbaa99887766554433221100","sequence":7}`,
		`{"type":"pairing.error","protocol_version":2,"session_id":"00112233445566778899aabbccddeeff","transaction_id":"ffeeddccbbaa99887766554433221100","sequence":8,"code":"storage_failure"}`,
	}
	for index, document := range documents {
		if _, err := DecodeContractMessage([]byte(document)); err != nil {
			t.Fatalf("document %d rejected: %v", index, err)
		}
	}
	if _, err := DecodeContractMessage([]byte(strings.Replace(documents[1], `"sequence":3`, `"sequence":2`, 1))); err == nil {
		t.Fatal("out-of-sequence commit accepted")
	}
}

func TestSharedPairingV2FixturesMatchGoContract(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate pairing v2 contract test")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	fixtureDirectory := filepath.Join(root, "protocol", "fixtures", "pairing-v2")
	manifestBytes, err := os.ReadFile(filepath.Join(fixtureDirectory, "manifest.json"))
	if err != nil {
		t.Fatalf("read fixture manifest: %v", err)
	}
	var manifest pairingV2FixtureManifest
	if err = json.Unmarshal(manifestBytes, &manifest); err != nil || manifest.SchemaVersion != 1 ||
		manifest.ProtocolVersion != int(ContractVersion) || len(manifest.Cases) < 10 {
		t.Fatalf("fixture manifest = %#v, error = %v", manifest, err)
	}
	for _, testCase := range manifest.Cases {
		t.Run(testCase.File, func(t *testing.T) {
			document, readErr := os.ReadFile(filepath.Join(fixtureDirectory, testCase.File))
			if readErr != nil {
				t.Fatalf("read fixture: %v", readErr)
			}
			message, decodeErr := DecodeContractMessage(document)
			if testCase.Accepted != (decodeErr == nil) {
				t.Fatalf("accepted = %t, error = %v", testCase.Accepted, decodeErr)
			}
			if testCase.Accepted {
				encoded, marshalErr := json.Marshal(message)
				if marshalErr != nil || !strings.Contains(string(encoded), `"type":"`+testCase.Type+`"`) {
					t.Fatalf("fixture type mismatch: %s, %v", encoded, marshalErr)
				}
			}
		})
	}
	schemaBytes, err := os.ReadFile(filepath.Join(root, "protocol", "schema", "pairing-v2.schema.json"))
	if err != nil {
		t.Fatalf("read Pairing v2 schema: %v", err)
	}
	var schema struct {
		Definitions map[string]json.RawMessage `json:"$defs"`
	}
	if json.Unmarshal(schemaBytes, &schema) != nil || schema.Definitions["credentials"] == nil ||
		schema.Definitions["commit_receipt"] == nil {
		t.Fatal("Pairing v2 schema is missing authoritative message definitions")
	}
}

func TestPairingV2TranscriptMatchesCrossEndKAT(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate pairing v2 contract test")
	}
	fixtureDirectory := filepath.Join(
		filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "..")),
		"protocol", "fixtures", "pairing-v2",
	)
	credentialsDocument, err := os.ReadFile(filepath.Join(fixtureDirectory, "valid-credentials.json"))
	if err != nil {
		t.Fatalf("read credentials fixture: %v", err)
	}
	readyDocument, err := os.ReadFile(filepath.Join(fixtureDirectory, "valid-commit-ready.json"))
	if err != nil {
		t.Fatalf("read commit-ready fixture: %v", err)
	}
	credentialsMessage, err := DecodeContractMessage(credentialsDocument)
	if err != nil {
		t.Fatalf("decode credentials: %v", err)
	}
	readyMessage, err := DecodeContractMessage(readyDocument)
	if err != nil {
		t.Fatalf("decode commit-ready: %v", err)
	}
	credentials := *credentialsMessage.(*Credentials)
	ready := *readyMessage.(*CommitReady)
	digest, err := TranscriptSHA256(credentials, ready)
	if err != nil {
		t.Fatalf("TranscriptSHA256() error = %v", err)
	}
	const expected = "sha256:ed73b99fc50c3d32bcb6404f42c11890585d2362af77b75578140dd1619d0493"
	if digest != expected || digest != ready.TranscriptSHA256 {
		t.Fatalf("transcript digest = %q, ready = %q", digest, ready.TranscriptSHA256)
	}

	ready.ProfileID = "sha256:" + strings.Repeat("0", 64)
	if _, err = TranscriptSHA256(credentials, ready); !errors.Is(err, ErrMalformedContract) {
		t.Fatalf("mismatched profile digest error = %v", err)
	}
}
