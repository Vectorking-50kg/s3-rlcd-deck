package runtime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/history"
	companionruntime "github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/runtime"
)

func TestManagementHistoryRoutesQueryExportDisableAndClear(t *testing.T) {
	historyStore, err := history.Open(context.Background(), history.Config{
		Path: filepath.Join(t.TempDir(), "provider-history.sqlite3"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer historyStore.Close(context.Background())
	observedAt := time.Date(2026, 8, 15, 10, 30, 0, 0, time.UTC)
	used := uint16(3200)
	provider := aisnapshot.Provider{
		SchemaVersion: aisnapshot.SchemaVersion{Major: 1, Minor: 0},
		ID:            "codex",
		DisplayName:   "Codex",
		Status:        aisnapshot.ProviderOK,
		Source:        aisnapshot.ProviderSourceCodexAppServer,
		Confidence:    aisnapshot.ConfidenceVerified,
		Windows:       []aisnapshot.QuotaWindow{{Name: "primary", UsedBasisPoints: &used}},
	}
	if err = historyStore.Capture(context.Background(), provider, observedAt); err != nil {
		t.Fatal(err)
	}
	if err = historyStore.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	config := testConfig()
	config.History = historyStore
	application, err := companionruntime.New(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	status := waitForState(t, application, companionruntime.StateReady)
	client, session, csrf := loginManagement(t, status.ManagementAddress, config.Management.AdminToken)
	query := url.Values{
		"from":  {observedAt.Add(-time.Hour).Format(time.RFC3339)},
		"until": {observedAt.Add(time.Hour).Format(time.RFC3339)},
		"limit": {"10"},
	}.Encode()

	request, _ := http.NewRequest(http.MethodGet, "http://"+status.ManagementAddress+"/api/v1/history?"+query, nil)
	request.AddCookie(session)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Records []history.Record `json:"records"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&page) != nil {
		response.Body.Close()
		t.Fatalf("GET history status = %d", response.StatusCode)
	}
	response.Body.Close()
	if len(page.Records) != 1 || page.Records[0].ProviderID != "codex" {
		t.Fatalf("history page = %+v", page)
	}

	request, _ = http.NewRequest(http.MethodGet, "http://"+status.ManagementAddress+"/api/v1/history/export.csv?"+query, nil)
	request.AddCookie(session)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	exported, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(exported), "provider_id") ||
		!strings.Contains(string(exported), "codex") {
		t.Fatalf("CSV status=%d body=%q", response.StatusCode, exported)
	}

	settingsBody := bytes.NewBufferString(`{"enabled":false}`)
	request, _ = http.NewRequest(http.MethodPut, "http://"+status.ManagementAddress+"/api/v1/history/settings", settingsBody)
	request.AddCookie(session)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://"+status.ManagementAddress)
	request.Header.Set("X-CSRF-Token", csrf)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent || historyStore.Enabled() {
		t.Fatalf("PUT settings status=%d enabled=%v", response.StatusCode, historyStore.Enabled())
	}

	request, _ = http.NewRequest(http.MethodDelete, "http://"+status.ManagementAddress+"/api/v1/history", nil)
	request.AddCookie(session)
	request.Header.Set("Origin", "http://"+status.ManagementAddress)
	request.Header.Set("X-CSRF-Token", csrf)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE history status=%d", response.StatusCode)
	}

	if err = historyStore.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	request, _ = http.NewRequest(http.MethodGet, "http://"+status.ManagementAddress+"/api/v1/history?"+query, nil)
	request.AddCookie(session)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("degraded GET history status=%d, want 503", response.StatusCode)
	}
	if current := application.Status(); current.HistoryAvailable || current.HistoryEnabled {
		t.Fatalf("degraded history status = %+v", current)
	}

	cancel()
	if err = <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}
