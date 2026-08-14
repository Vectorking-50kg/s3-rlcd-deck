package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/cursorprovider"
)

type fakeRuntimeCursorCollector struct {
	provider aisnapshot.Provider
	started  chan struct{}
	stop     chan struct{}
	terminal error
}

func (collector *fakeRuntimeCursorCollector) Run(
	ctx context.Context,
	publish cursorprovider.Publisher,
) error {
	if err := publish(ctx, collector.provider); err != nil {
		return err
	}
	close(collector.started)
	if collector.terminal != nil {
		return collector.terminal
	}
	select {
	case <-ctx.Done():
	case <-collector.stop:
	}
	return nil
}

func TestRuntimeOwnsCursorProviderAndIsolatesCollectorFailure(t *testing.T) {
	used := uint16(2500)
	collector := &fakeRuntimeCursorCollector{
		provider: aisnapshot.Provider{
			SchemaVersion: aisnapshot.SchemaVersion{Major: 1, Minor: 0},
			ID:            "cursor",
			DisplayName:   "Cursor",
			Status:        aisnapshot.ProviderOK,
			Source:        aisnapshot.ProviderSourceCursorLocal,
			Confidence:    aisnapshot.ConfidenceInferred,
			Experimental:  true,
			Windows: []aisnapshot.QuotaWindow{{
				Name:            "billing",
				UsedBasisPoints: &used,
			}},
		},
		started:  make(chan struct{}),
		terminal: errors.New("private Cursor adapter stopped"),
	}
	application, err := New(Config{
		Version: "1.2.3-test",
		Management: ManagementConfig{
			Address:    "127.0.0.1:0",
			AdminToken: "management-test-token-000000000001",
		},
		DeviceHub:       DeviceHubConfig{Address: "127.0.0.1:0"},
		Pairing:         testPairingService(t),
		CursorCollector: collector,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	_ = waitForRuntimeState(t, application, StateReady)
	select {
	case <-collector.started:
	case <-time.After(time.Second):
		t.Fatal("Cursor collector did not start")
	}
	provider, exists := application.CursorProvider()
	if !exists || provider.ID != "cursor" || *provider.Windows[0].UsedBasisPoints != 2500 {
		t.Fatalf("Cursor provider = %+v exists=%v", provider, exists)
	}
	*provider.Windows[0].UsedBasisPoints = 9999
	stored, _ := application.CursorProvider()
	if *stored.Windows[0].UsedBasisPoints != 2500 {
		t.Fatal("caller mutated runtime-owned Cursor provider")
	}

	// The collector has already failed, but both Companion listeners remain
	// ready until the shared runtime context is explicitly canceled.
	time.Sleep(20 * time.Millisecond)
	if status := application.Status(); status.State != StateReady {
		t.Fatalf("Cursor failure changed runtime state: %+v", status)
	}
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not stop after isolated Cursor failure")
	}
}

func TestRuntimeRejectsNonExperimentalCursorPublication(t *testing.T) {
	application, err := New(Config{
		Version: "1.2.3-test",
		Management: ManagementConfig{
			Address:    "127.0.0.1:0",
			AdminToken: "management-test-token-000000000001",
		},
		DeviceHub: DeviceHubConfig{Address: "127.0.0.1:0"},
		Pairing:   testPairingService(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = application.publishCursorProvider(context.Background(), aisnapshot.Provider{
		ID: "cursor",
	}); !errors.Is(err, cursorprovider.ErrUnavailable) {
		t.Fatalf("publishCursorProvider() error = %v", err)
	}
	if _, exists := application.CursorProvider(); exists {
		t.Fatal("invalid Cursor provider entered Runtime")
	}
}
