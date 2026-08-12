package pairing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
)

func TestSharedPairingFixturesDefineTheGoRequestContract(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() could not locate test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", ".."))
	fixtureDirectory := filepath.Join(root, "protocol", "fixtures", "pairing")
	manifestBytes, err := os.ReadFile(filepath.Join(fixtureDirectory, "manifest.json"))
	if err != nil {
		t.Fatalf("read fixture manifest: %v", err)
	}
	var manifest struct {
		SchemaVersion int `json:"schema_version"`
		Cases         []struct {
			Name     string `json:"name"`
			File     string `json:"file"`
			Accepted bool   `json:"accepted"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("parse fixture manifest: %v", err)
	}
	if manifest.SchemaVersion != 1 || len(manifest.Cases) < 2 {
		t.Fatalf("pairing manifest = %#v", manifest)
	}
	for _, testCase := range manifest.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			message, readErr := os.ReadFile(filepath.Join(fixtureDirectory, testCase.File))
			if readErr != nil {
				t.Fatalf("read fixture: %v", readErr)
			}
			var request RedeemRequest
			decodeErr := protocol.DecodeStrictDocument(message, &request)
			if decodeErr == nil {
				_, decodeErr = validateRedeemRequest(request)
			}
			if (decodeErr == nil) != testCase.Accepted {
				t.Fatalf("pairing fixture error = %v, accepted = %t", decodeErr, testCase.Accepted)
			}
		})
	}

	var schema struct {
		Definitions struct {
			Redeem struct {
				Properties struct {
					ProtocolVersion struct {
						Constant int `json:"const"`
					} `json:"protocol_version"`
				} `json:"properties"`
			} `json:"redeem_request"`
		} `json:"$defs"`
	}
	schemaBytes, err := os.ReadFile(filepath.Join(root, "protocol", "schema", "pairing-v1.schema.json"))
	if err != nil {
		t.Fatalf("read pairing schema: %v", err)
	}
	if err = json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatalf("parse pairing schema: %v", err)
	}
	if schema.Definitions.Redeem.Properties.ProtocolVersion.Constant != ProtocolVersion {
		t.Fatalf("pairing schema version != Go protocol version")
	}
}
