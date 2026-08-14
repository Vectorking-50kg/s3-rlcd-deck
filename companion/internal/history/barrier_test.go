package history

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
)

func TestWriterRejectsCapturesFromBeforeDisableAndClearBarriers(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	store, err := Open(context.Background(), Config{
		Path: filepath.Join(t.TempDir(), "provider-history.sqlite3"),
		Now:  func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	provider := barrierProvider()

	beforeDisable := store.generation.Load()
	if err = store.SetEnabled(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	store.commands <- storeCommand{
		kind: commandCapture, provider: provider, observedAt: now, generation: beforeDisable,
	}
	if err = store.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertBarrierHistoryEmpty(t, store, now)

	if err = store.SetEnabled(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	beforeClear := store.generation.Load()
	if err = store.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.commands <- storeCommand{
		kind: commandCapture, provider: provider, observedAt: now, generation: beforeClear,
	}
	if err = store.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertBarrierHistoryEmpty(t, store, now)
}

func barrierProvider() aisnapshot.Provider {
	used := uint16(1000)
	return aisnapshot.Provider{
		SchemaVersion: aisnapshot.SchemaVersion{Major: 1, Minor: 0},
		ID:            "codex",
		DisplayName:   "Codex",
		Status:        aisnapshot.ProviderOK,
		Source:        aisnapshot.ProviderSourceCodexAppServer,
		Confidence:    aisnapshot.ConfidenceVerified,
		Windows:       []aisnapshot.QuotaWindow{{Name: "primary", UsedBasisPoints: &used}},
	}
}

func assertBarrierHistoryEmpty(t *testing.T, store *Store, now time.Time) {
	t.Helper()
	records, err := store.Query(context.Background(), Query{
		From: now.Add(-time.Hour), Until: now.Add(time.Hour), Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("stale capture crossed writer barrier: %+v", records)
	}
}
