package runtime

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/diagnostics"
)

func TestDiagnosticsRoutesRequireLoginAndExportHashedRedactedBundle(t *testing.T) {
	diagnosticService, err := diagnostics.Open(diagnostics.Config{
		Directory: filepath.Join(t.TempDir(), "diagnostics"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if closeErr := diagnosticService.Close(ctx); closeErr != nil {
			t.Fatal(closeErr)
		}
	}()
	if !diagnosticService.Record(diagnostics.Event{
		Level: diagnostics.LevelInfo, Module: diagnostics.ModuleRuntime,
		Code: diagnostics.CodeRuntimeReady,
	}) {
		t.Fatal("record failed")
	}
	application, err := New(Config{
		Version: "0.3.1-dev", Commit: strings.Repeat("a", 40),
		Management: ManagementConfig{
			Address: "127.0.0.1:7777", AdminToken: strings.Repeat("m", 32),
		},
		DeviceHub: DeviceHubConfig{Address: "127.0.0.1:7780"},
		Pairing:   testPairingService(t), Diagnostics: diagnosticService,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := application.managementRoutes()
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/diagnostics", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated diagnostics = %d", unauthorized.Code)
	}
	const session = "diagnostics-session"
	const csrf = "diagnostics-csrf"
	if !application.sessions.add(session, csrf, time.Now()) {
		t.Fatal("add management session")
	}
	statusRequest := httptest.NewRequest(http.MethodGet, "/api/v1/diagnostics", nil)
	statusRequest.AddCookie(&http.Cookie{Name: managementSessionCookie, Value: session})
	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("diagnostics status = %d %q", statusResponse.Code, statusResponse.Body.String())
	}
	var status diagnosticStatus
	if err = json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil ||
		!status.Available || status.RetentionDays != 7 || status.MaximumBytes != 50<<20 ||
		len(status.BundleFiles) != 4 {
		t.Fatalf("diagnostics status = %#v, %v", status, err)
	}
	exportRequest := httptest.NewRequest(http.MethodPost, "/api/v1/diagnostics/export", nil)
	exportRequest.Host = "127.0.0.1:7777"
	exportRequest.Header.Set("Origin", "http://127.0.0.1:7777")
	exportRequest.Header.Set("X-CSRF-Token", csrf)
	exportRequest.AddCookie(&http.Cookie{Name: managementSessionCookie, Value: session})
	exportResponse := httptest.NewRecorder()
	handler.ServeHTTP(exportResponse, exportRequest)
	if exportResponse.Code != http.StatusOK ||
		exportResponse.Header().Get("Content-Type") != "application/zip" ||
		!strings.Contains(exportResponse.Header().Get("Content-Disposition"), "diagnostics.zip") {
		t.Fatalf("diagnostics export = %d %#v %q", exportResponse.Code, exportResponse.Header(), exportResponse.Body.String())
	}
	digest := sha256.Sum256(exportResponse.Body.Bytes())
	if exportResponse.Header().Get("X-Content-SHA256") != hex.EncodeToString(digest[:]) {
		t.Fatal("bundle hash header mismatch")
	}
	archive, err := zip.NewReader(bytes.NewReader(exportResponse.Body.Bytes()), int64(exportResponse.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var all bytes.Buffer
	for _, file := range archive.File {
		opened, openErr := file.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		_, openErr = io.Copy(&all, opened)
		_ = opened.Close()
		if openErr != nil {
			t.Fatal(openErr)
		}
	}
	for _, canary := range []string{"Authorization", "Cookie", "API_KEY", "/Users/", "serial body"} {
		if bytes.Contains(all.Bytes(), []byte(canary)) {
			t.Fatalf("bundle contains %q", canary)
		}
	}
}
