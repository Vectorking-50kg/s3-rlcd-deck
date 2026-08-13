package deviceidentity_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/deviceidentity"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
)

func TestLoadOrCreatePersistsAProtectedCertificateIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity", "device-hub.json")
	created, err := deviceidentity.LoadOrCreate(path)
	if err != nil {
		t.Fatalf("LoadOrCreate() error = %v", err)
	}
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(created.Fingerprint()) {
		t.Fatalf("fingerprint = %q, want canonical SHA-256", created.Fingerprint())
	}
	certificate, err := created.TLSCertificate()
	if err != nil || len(certificate.Certificate) != 1 {
		t.Fatalf("TLSCertificate() = %#v, %v", certificate, err)
	}
	if err = protectedfile.VerifyPrivate(path); err != nil {
		t.Fatalf("identity protection = %v", err)
	}

	reopened, err := deviceidentity.LoadOrCreate(path)
	if err != nil {
		t.Fatalf("reopen identity error = %v", err)
	}
	if reopened.Fingerprint() != created.Fingerprint() {
		t.Fatalf("fingerprint changed across restart: %q != %q", reopened.Fingerprint(), created.Fingerprint())
	}
}

func TestLoadOrCreateFailsClosedForMalformedIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device-hub.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := deviceidentity.LoadOrCreate(path); err == nil {
		t.Fatal("LoadOrCreate() error = nil, want malformed identity failure")
	}
}
