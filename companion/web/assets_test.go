package webapp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedConsoleContainsProviderHistoryAndBackupWorkflows(t *testing.T) {
	handler := Handler()
	for path, required := range map[string][]string{
		"/": {
			"AI Providers", "新增 Provider", "历史记录", "备份与恢复",
			"provider-form", "management-token", "provider-headers", "map-reset-format",
			"backup-mode", "backup-conflicts", "providers_only", "replace", "merge",
		},
		"/app.js": {
			"/api/v1/providers", "/api/v1/providers/order", "/test",
			"/api/v1/history", "/api/v1/backups/export", "/api/v1/backups/preview",
			"textContent", "TextEncoder", "reset_format", "keep_existing",
			"keep_current", "use_backup", "resetBackupPreview", "#import-file",
		},
		"/app.css": {"focus-visible", "prefers-reduced-motion", "@media (max-width: 620px)"},
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		body, err := io.ReadAll(response.Result().Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, response.Code)
		}
		for _, expected := range required {
			if !strings.Contains(string(body), expected) {
				t.Errorf("GET %s omitted %q", path, expected)
			}
		}
		if strings.Contains(string(body), "innerHTML") ||
			strings.Contains(string(body), "localStorage") ||
			strings.Contains(string(body), "https://") {
			t.Errorf("GET %s contains an unsafe browser boundary", path)
		}
	}
}
