package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/structuredprovider"
)

type fakeStructuredCollector struct {
	provider aisnapshot.Provider
	started  chan struct{}
	terminal error
}

func (collector *fakeStructuredCollector) ProviderID() string {
	return collector.provider.ID
}

func (collector *fakeStructuredCollector) Run(
	ctx context.Context,
	publish structuredprovider.Publisher,
) error {
	if err := publish(ctx, collector.provider); err != nil {
		return err
	}
	close(collector.started)
	if collector.terminal != nil {
		return collector.terminal
	}
	<-ctx.Done()
	return nil
}

func TestRuntimeOwnsStructuredProvidersAndIsolatesCollectorFailure(t *testing.T) {
	used := uint16(2500)
	collector := &fakeStructuredCollector{
		provider: aisnapshot.Provider{
			SchemaVersion: aisnapshot.SchemaVersion{Major: 1, Minor: 0},
			ID:            "deepseek",
			DisplayName:   "DeepSeek",
			Status:        aisnapshot.ProviderOK,
			Source:        aisnapshot.ProviderSourceStructuredHTTP,
			Confidence:    aisnapshot.ConfidenceVerified,
			Windows: []aisnapshot.QuotaWindow{{
				Name:            "account",
				UsedBasisPoints: &used,
			}},
		},
		started:  make(chan struct{}),
		terminal: errors.New("DeepSeek endpoint unavailable"),
	}
	application, err := New(Config{
		Version: "1.2.3-test",
		Management: ManagementConfig{
			Address:    "127.0.0.1:0",
			AdminToken: "management-test-token-000000000001",
		},
		DeviceHub:            DeviceHubConfig{Address: "127.0.0.1:0"},
		Pairing:              testPairingService(t),
		StructuredCollectors: []StructuredCollector{collector},
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
		t.Fatal("structured Provider collector did not start")
	}
	providers := application.StructuredProviders()
	if len(providers) != 1 || providers[0].ID != "deepseek" ||
		*providers[0].Windows[0].UsedBasisPoints != 2500 {
		t.Fatalf("StructuredProviders() = %+v", providers)
	}
	*providers[0].Windows[0].UsedBasisPoints = 9999
	if stored := application.StructuredProviders(); *stored[0].Windows[0].UsedBasisPoints != 2500 {
		t.Fatal("caller mutated runtime-owned structured Provider")
	}
	time.Sleep(20 * time.Millisecond)
	if status := application.Status(); status.State != StateReady {
		t.Fatalf("structured Provider failure changed runtime state: %+v", status)
	}
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Runtime did not stop")
	}
}

func TestRuntimeRejectsReservedStructuredProvider(t *testing.T) {
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
	err = application.publishStructuredProvider(context.Background(), aisnapshot.Provider{
		ID:     "codex",
		Source: aisnapshot.ProviderSourceStructuredHTTP,
	})
	if !errors.Is(err, structuredprovider.ErrUnavailable) {
		t.Fatalf("publishStructuredProvider() error = %v", err)
	}
}

func TestRuntimePreservesConfiguredStructuredProviderOrder(t *testing.T) {
	provider := func(id string) aisnapshot.Provider {
		return aisnapshot.Provider{
			SchemaVersion: aisnapshot.SchemaVersion{Major: 1, Minor: 0},
			ID:            id, DisplayName: id, Status: aisnapshot.ProviderOK,
			Source:     aisnapshot.ProviderSourceStructuredHTTP,
			Confidence: aisnapshot.ConfidenceVerified,
			Windows:    []aisnapshot.QuotaWindow{},
		}
	}
	first := &fakeStructuredCollector{provider: provider("zeta")}
	second := &fakeStructuredCollector{provider: provider("alpha")}
	application, err := New(Config{
		Version: "1.2.3-test",
		Management: ManagementConfig{
			Address: "127.0.0.1:0", AdminToken: "management-test-token-000000000001",
		},
		DeviceHub:            DeviceHubConfig{Address: "127.0.0.1:0"},
		Pairing:              testPairingService(t),
		StructuredCollectors: []StructuredCollector{first, second},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = application.publishStructuredProvider(context.Background(), second.provider); err != nil {
		t.Fatal(err)
	}
	if err = application.publishStructuredProvider(context.Background(), first.provider); err != nil {
		t.Fatal(err)
	}
	providers := application.StructuredProviders()
	if len(providers) != 2 || providers[0].ID != "zeta" || providers[1].ID != "alpha" {
		t.Fatalf("configured Provider order = %#v", providers)
	}
}
