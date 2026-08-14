package codexappserver

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
)

// This opt-in smoke verifies the installed official App Server without
// printing or persisting its raw responses. CI uses transcript tests because it
// does not own the developer's Codex login.
func TestInstalledCodexAppServer(t *testing.T) {
	if os.Getenv("S3DECK_TEST_CODEX_APP_SERVER") != "1" {
		t.Skip("set S3DECK_TEST_CODEX_APP_SERVER=1 for the local official-server smoke")
	}
	collector, err := New(Config{
		ClientVersion:  "0.3.0-integration-test",
		RequestTimeout: 10 * time.Second,
		ReconnectDelay: 100 * time.Millisecond,
		Now:            func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	connection, err := collector.config.Connector.Connect(ctx)
	if err != nil {
		t.Fatalf("connect official App Server: %v", err)
	}
	client := newRPCClient(connection, collector.config.MaximumDocument)
	defer client.Close()
	if err = initialize(ctx, client, collector.config.ClientVersion); err != nil {
		t.Fatalf("initialize official App Server: %v", err)
	}
	provider, err := collectProvider(ctx, client, collector.config.Now())
	if err != nil {
		t.Fatalf("collect official App Server DTO: %v", err)
	}
	update := Update{Provider: provider, Sessions: []aisnapshot.Session{}}
	if update.Provider.ID != providerID || update.Provider.Status != aisnapshot.ProviderOK {
		t.Fatalf("official App Server did not yield a healthy normalized Codex Provider: status=%s error=%+v",
			update.Provider.Status, update.Provider.Error)
	}
	assertWireValid(t, update)
}
