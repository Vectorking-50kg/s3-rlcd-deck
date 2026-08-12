package runtime

import (
	"testing"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairing"
)

func testPairingService(t *testing.T) *pairing.Service {
	t.Helper()
	service, err := pairing.New(pairing.Config{
		Store: pairing.NewMemoryStore(),
		CertificateFingerprint: "sha256:" +
			"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("pairing.New() error = %v", err)
	}
	return service
}
