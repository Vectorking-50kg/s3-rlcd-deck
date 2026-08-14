package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/codexappserver"
)

type fakeRuntimeCodexCollector struct {
	update  codexappserver.Update
	started chan struct{}
	stopped chan struct{}
	loaded  chan string
	once    sync.Once
}

func (collector *fakeRuntimeCodexCollector) Run(
	ctx context.Context,
	publish codexappserver.Publisher,
) error {
	if err := publish(ctx, collector.update); err != nil {
		return err
	}
	collector.once.Do(func() { close(collector.started) })
	<-ctx.Done()
	close(collector.stopped)
	return nil
}

func (collector *fakeRuntimeCodexCollector) LoadThread(
	_ context.Context,
	threadID string,
) error {
	collector.loaded <- threadID
	return nil
}

func TestRuntimeSupervisesNormalizedCodexUpdatesWithoutSharingOwnership(t *testing.T) {
	used := uint16(2500)
	display := "private alias"
	collector := &fakeRuntimeCodexCollector{
		update: codexappserver.Update{
			Provider: aisnapshot.Provider{
				ID:      "codex",
				Windows: []aisnapshot.QuotaWindow{{Name: "dynamic", UsedBasisPoints: &used}},
			},
			Sessions: []aisnapshot.Session{{ID: "codex_0123456789abcdef", DisplayName: &display}},
		},
		started: make(chan struct{}),
		stopped: make(chan struct{}),
		loaded:  make(chan string, 1),
	}
	application, err := New(Config{
		Version: "1.2.3-test",
		Management: ManagementConfig{
			Address:    "127.0.0.1:0",
			AdminToken: "management-test-token-000000000001",
		},
		DeviceHub:      DeviceHubConfig{Address: "127.0.0.1:0"},
		Pairing:        testPairingService(t),
		CodexCollector: collector,
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
		t.Fatal("Codex collector did not start")
	}

	update, exists := application.CodexUpdate()
	if !exists || update.Provider.ID != "codex" || len(update.Sessions) != 1 {
		t.Fatalf("runtime Codex update = %+v, exists=%v", update, exists)
	}
	*update.Provider.Windows[0].UsedBasisPoints = 9999
	*update.Sessions[0].DisplayName = "mutated"
	stored, _ := application.CodexUpdate()
	if *stored.Provider.Windows[0].UsedBasisPoints != 2500 ||
		*stored.Sessions[0].DisplayName != "private alias" {
		t.Fatalf("caller mutated runtime-owned update: %+v", stored)
	}

	if err = application.LoadCodexThread(context.Background(), "owned-thread"); err != nil {
		t.Fatalf("LoadCodexThread() error = %v", err)
	}
	select {
	case loaded := <-collector.loaded:
		if loaded != "owned-thread" {
			t.Fatalf("loaded thread = %q", loaded)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not delegate owned thread load")
	}

	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not stop")
	}
	select {
	case <-collector.stopped:
	default:
		t.Fatal("runtime returned before stopping Codex collector")
	}
}

func TestRuntimeRejectsOwnedThreadLoadWithoutCollector(t *testing.T) {
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
	if err = application.LoadCodexThread(context.Background(), "thread"); err != ErrCodexUnavailable {
		t.Fatalf("LoadCodexThread() error = %v, want %v", err, ErrCodexUnavailable)
	}
}
