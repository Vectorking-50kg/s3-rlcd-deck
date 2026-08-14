package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/codexappserver"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/codexobserver"
)

type fakeRuntimeCodexCollector struct {
	update  codexappserver.Update
	started chan struct{}
	stopped chan struct{}
	loaded  chan string
	once    sync.Once
}

type fakeRuntimeCodexObserver struct {
	sessions []aisnapshot.Session
	started  chan struct{}
	stopped  chan struct{}
	once     sync.Once
}

func (observer *fakeRuntimeCodexObserver) Run(
	ctx context.Context,
	publish codexobserver.Publisher,
) error {
	if err := publish(ctx, observer.sessions); err != nil {
		return err
	}
	observer.once.Do(func() { close(observer.started) })
	<-ctx.Done()
	close(observer.stopped)
	return nil
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
			Sessions: []aisnapshot.Session{{
				ID:          "codex_0123456789abcdef",
				DisplayName: &display,
				State:       aisnapshot.SessionRunning,
				Source:      aisnapshot.SessionSourceCodexAppServerOwned,
				Confidence:  aisnapshot.ConfidenceVerified,
			}},
		},
		started: make(chan struct{}),
		stopped: make(chan struct{}),
		loaded:  make(chan string, 1),
	}
	observer := &fakeRuntimeCodexObserver{
		sessions: []aisnapshot.Session{
			{
				SchemaVersion: aisnapshot.SchemaVersion{Major: 1, Minor: 0},
				ID:            "codex_0123456789abcdef",
				ProviderID:    "codex",
				State:         aisnapshot.SessionRecent,
				Source:        aisnapshot.SessionSourceProcessJSONL,
				Confidence:    aisnapshot.ConfidenceInferred,
			},
			{
				SchemaVersion: aisnapshot.SchemaVersion{Major: 1, Minor: 0},
				ID:            "codex_fedcba9876543210",
				ProviderID:    "codex",
				State:         aisnapshot.SessionUnknown,
				Source:        aisnapshot.SessionSourceProcessJSONL,
				Confidence:    aisnapshot.ConfidenceInferred,
			},
		},
		started: make(chan struct{}),
		stopped: make(chan struct{}),
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
		CodexObserver:  observer,
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
	select {
	case <-observer.started:
	case <-time.After(time.Second):
		t.Fatal("Codex observer did not start")
	}

	update, exists := application.CodexUpdate()
	if !exists || update.Provider.ID != "codex" || len(update.Sessions) != 2 {
		t.Fatalf("runtime Codex update = %+v, exists=%v", update, exists)
	}
	if update.Sessions[0].Source != aisnapshot.SessionSourceCodexAppServerOwned ||
		update.Sessions[1].Source != aisnapshot.SessionSourceProcessJSONL {
		t.Fatalf("verified session did not win anonymous-id deduplication: %+v", update.Sessions)
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
	select {
	case <-observer.stopped:
	default:
		t.Fatal("runtime returned before stopping Codex observer")
	}
}

func TestObserverFailureCannotRemoveOfficialCodexProviderOrSessions(t *testing.T) {
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
	verified := aisnapshot.Session{
		ID:         "codex_verified",
		Source:     aisnapshot.SessionSourceCodexAppServerOwned,
		Confidence: aisnapshot.ConfidenceVerified,
	}
	if err = application.publishCodexUpdate(context.Background(), codexappserver.Update{
		Provider: aisnapshot.Provider{ID: "codex", Status: aisnapshot.ProviderOK},
		Sessions: []aisnapshot.Session{verified},
	}); err != nil {
		t.Fatal(err)
	}
	if err = application.publishCodexSessions(context.Background(), []aisnapshot.Session{{
		SchemaVersion: aisnapshot.SchemaVersion{Major: 1, Minor: 0},
		ID:            "codex_aaaaaaaaaaaaaaaa",
		ProviderID:    "codex",
		State:         aisnapshot.SessionUnknown,
		Source:        aisnapshot.SessionSourceProcessJSONL,
		Confidence:    aisnapshot.ConfidenceInferred,
	}}); err != nil {
		t.Fatal(err)
	}
	if err = application.publishCodexSessions(context.Background(), []aisnapshot.Session{}); err != nil {
		t.Fatal(err)
	}
	update, exists := application.CodexUpdate()
	if !exists || update.Provider.ID != "codex" || len(update.Sessions) != 1 ||
		update.Sessions[0].ID != verified.ID {
		t.Fatalf("observer failure damaged official update: %+v, exists=%v", update, exists)
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
