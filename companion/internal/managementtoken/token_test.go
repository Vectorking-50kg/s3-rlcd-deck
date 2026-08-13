package managementtoken_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/managementtoken"
)

func TestLoadOrCreatePersistsProtectedRandomToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "management-token")
	first, err := managementtoken.LoadOrCreate(path)
	if err != nil {
		t.Fatalf("LoadOrCreate(first) error = %v", err)
	}
	second, err := managementtoken.LoadOrCreate(path)
	if err != nil {
		t.Fatalf("LoadOrCreate(second) error = %v", err)
	}
	if first == "" || first != second {
		t.Fatalf("tokens = %q and %q, want stable non-empty token", first, second)
	}
	if info, statErr := os.Stat(path); statErr != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token mode = %v, error = %v, want 0600", info.Mode(), statErr)
	}
}
