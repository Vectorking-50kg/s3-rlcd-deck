package runtime

import (
	"testing"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairing"
)

func testPairingService(t *testing.T) *pairing.Service {
	t.Helper()
	service, err := pairing.New(pairing.Config{
		Store:                  pairing.NewMemoryStore(),
		CertificateFingerprint: "sha256:69be57455b3b4f84c7c23140e875002791c5a5509ca9d0c644a63d5eaf836cce",
		CertificateDER:         []byte("test-certificate-der"),
	})
	if err != nil {
		t.Fatalf("pairing.New() error = %v", err)
	}
	return service
}
