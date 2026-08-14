package aisnapshot

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"
)

func repositoryFile(t *testing.T, parts ...string) []byte {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate AI Snapshot contract test")
	}
	pathParts := append([]string{filepath.Dir(source), "..", "..", ".."}, parts...)
	path := filepath.Join(pathParts...)
	document, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read repository file %s: %v", path, err)
	}
	return document
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	return repositoryFile(t, "protocol", "fixtures", "ai-snapshot-v1", name)
}

func TestDecodeAcceptsMinimalAISnapshot(t *testing.T) {
	snapshot, err := Decode(fixture(t, "valid-minimal.json"))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if snapshot.SchemaVersion.Major != 1 || snapshot.GeneratedAtUnixMS != 1786624496000 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestDecodeReturnsNormalizedProviderAndSessionDTOs(t *testing.T) {
	snapshot, err := Decode(fixture(t, "valid-full.json"))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(snapshot.Providers) != 1 || snapshot.Providers[0].ID != "codex" {
		t.Fatalf("Providers = %#v", snapshot.Providers)
	}
	window := snapshot.Providers[0].Windows[0]
	if window.Name != "primary" || *window.UsedBasisPoints != 3800 ||
		*window.RemainingBasisPoints != 6200 {
		t.Fatalf("Window = %#v", window)
	}
	if len(snapshot.Sessions) != 1 || snapshot.Sessions[0].Confidence != ConfidenceVerified ||
		snapshot.Sessions[0].State != SessionRunning {
		t.Fatalf("Sessions = %#v", snapshot.Sessions)
	}
}

func TestDecodeRejectsUnknownSessionState(t *testing.T) {
	if _, err := Decode(fixture(t, "invalid-session-state.json")); err == nil {
		t.Fatal("Decode() accepted an unknown Session state")
	}
}

func TestDecodeRejectsPrivateContentCanary(t *testing.T) {
	_, err := Decode(fixture(t, "invalid-privacy-prompt.json"))
	if !errors.Is(err, ErrPrivateData) {
		t.Fatalf("Decode() error = %v, want ErrPrivateData", err)
	}
}

func TestCanonicalFixtureManifest(t *testing.T) {
	type fixtureCase struct {
		File   string `json:"file"`
		Result string `json:"result"`
	}
	var manifest struct {
		Cases []fixtureCase `json:"cases"`
	}
	if err := json.Unmarshal(fixture(t, "manifest.json"), &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if len(manifest.Cases) < 12 {
		t.Fatalf("fixture count = %d, want comprehensive contract", len(manifest.Cases))
	}
	for _, testCase := range manifest.Cases {
		t.Run(testCase.File, func(t *testing.T) {
			_, err := Decode(fixture(t, testCase.File))
			switch testCase.Result {
			case "accepted":
				if err != nil {
					t.Fatalf("Decode() error = %v", err)
				}
			case "malformed":
				if !errors.Is(err, ErrMalformedSnapshot) {
					t.Fatalf("Decode() error = %v, want ErrMalformedSnapshot", err)
				}
			case "unsupported_version":
				if !errors.Is(err, ErrUnsupportedVersion) {
					t.Fatalf("Decode() error = %v, want ErrUnsupportedVersion", err)
				}
			case "private_data":
				if !errors.Is(err, ErrPrivateData) {
					t.Fatalf("Decode() error = %v, want ErrPrivateData", err)
				}
			default:
				t.Fatalf("unknown fixture result %q", testCase.Result)
			}
		})
	}
}

func TestEncodeUsesTheSameContractAsDecode(t *testing.T) {
	snapshot, err := Decode(fixture(t, "valid-full.json"))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	document, err := Encode(snapshot)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if _, err = Decode(document); err != nil {
		t.Fatalf("encoded snapshot does not round trip: %v", err)
	}
	snapshot.NextRefresh = 0
	if _, err = Encode(snapshot); !errors.Is(err, ErrMalformedSnapshot) {
		t.Fatalf("Encode(invalid) error = %v, want ErrMalformedSnapshot", err)
	}
}

func TestRetainedSnapshotSurvivesAnUnsupportedMajor(t *testing.T) {
	var retained Retained
	accepted, err := retained.Apply(fixture(t, "valid-full.json"))
	if err != nil || accepted.ProviderOrder[0] != "codex" {
		t.Fatalf("Apply(valid) = %#v, %v", accepted, err)
	}
	if _, err = retained.Apply(fixture(t, "invalid-major-version.json")); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("Apply(unknown major) error = %v", err)
	}
	current, present := retained.Current()
	if !present || current.GeneratedAtUnixMS != accepted.GeneratedAtUnixMS ||
		current.ProviderOrder[0] != "codex" {
		t.Fatalf("Current() = %#v, %t; previous valid snapshot was overwritten", current, present)
	}
}

func TestGoEnumsMatchTheSharedSchemaAndErrorCatalog(t *testing.T) {
	var schema struct {
		Defs map[string]struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(repositoryFile(
		t, "protocol", "schema", "ai-snapshot-v1.schema.json",
	), &schema); err != nil {
		t.Fatalf("decode shared schema: %v", err)
	}
	assertEnum := func(definition, property string, expected []string) {
		t.Helper()
		actual := append([]string(nil), schema.Defs[definition].Properties[property].Enum...)
		sort.Strings(actual)
		sort.Strings(expected)
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("%s.%s enum = %v, want %v", definition, property, actual, expected)
		}
	}
	assertEnum("provider", "status", []string{
		string(ProviderOK), string(ProviderDegraded), string(ProviderUnavailable),
	})
	assertEnum("provider", "source", []string{
		string(ProviderSourceCodexAppServer), string(ProviderSourceCursorLocal),
		string(ProviderSourceStructuredHTTP), string(ProviderSourceNone),
	})
	assertEnum("provider", "confidence", []string{
		string(ConfidenceVerified), string(ConfidenceInferred), string(ConfidenceUnavailable),
	})
	assertEnum("session", "state", []string{
		string(SessionRunning), string(SessionWaitingApproval), string(SessionWaitingInput),
		string(SessionCompleted), string(SessionFailed), string(SessionRecent),
		string(SessionEnded), string(SessionUnknown), string(SessionUnavailable),
	})
	assertEnum("session", "source", []string{
		string(SessionSourceCodexAppServerOwned), string(SessionSourceProcessJSONL),
		string(SessionSourceNone),
	})

	var catalog struct {
		ProviderErrorCodes []string `json:"provider_error_codes"`
	}
	if err := json.Unmarshal(repositoryFile(
		t, "protocol", "schema", "ai-snapshot-error-codes-v1.json",
	), &catalog); err != nil {
		t.Fatalf("decode error catalog: %v", err)
	}
	expectedErrors := []string{
		string(ProviderErrorAuthStale), string(ProviderErrorPermissionDenied),
		string(ProviderErrorTimeout), string(ProviderErrorProcessExited),
		string(ProviderErrorSchemaChanged), string(ProviderErrorUnavailable),
	}
	assertEnum("provider_error", "code", append([]string(nil), expectedErrors...))
	sort.Strings(catalog.ProviderErrorCodes)
	sort.Strings(expectedErrors)
	if !reflect.DeepEqual(catalog.ProviderErrorCodes, expectedErrors) {
		t.Fatalf("provider error catalog = %v, want %v", catalog.ProviderErrorCodes, expectedErrors)
	}
}
