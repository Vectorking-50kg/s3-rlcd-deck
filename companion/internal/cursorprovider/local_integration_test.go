package cursorprovider

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
)

// TestInstalledCursorPersonalUsage is an opt-in, privacy-safe real-account
// smoke. It reports only pass/fail and never prints the token, account data, or
// raw private response. CI fixtures and cross-builds cannot substitute for it.
func TestInstalledCursorPersonalUsage(t *testing.T) {
	if os.Getenv("S3DECK_TEST_CURSOR_PERSONAL") != "1" {
		t.Skip("set S3DECK_TEST_CURSOR_PERSONAL=1 with Cursor logged in")
	}
	collector, err := New(Config{
		AdapterVersion:        AdapterVersion,
		ResponseSchemaVersion: ResponseSchemaVersion,
		RequestTimeout:        5 * time.Second,
	})
	if err != nil {
		t.Fatal("cannot configure the privacy-safe Cursor adapter")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	provider, err := collectProvider(ctx, collector.config)
	if err != nil {
		t.Fatal("installed Cursor personal-usage smoke failed")
	}
	if provider.ID != providerID || provider.Status != aisnapshot.ProviderOK ||
		!provider.Experimental || len(provider.Windows) == 0 {
		t.Fatal("installed Cursor returned no normalized quota window")
	}
}
