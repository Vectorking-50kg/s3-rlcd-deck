package webapp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedConsoleContainsCompleteChineseSchemeCWorkflows(t *testing.T) {
	handler := Handler()
	for path, required := range map[string][]string{
		"/": {
			"AI Provider", "添加 Provider", "用量历史", "备份与恢复", "串口终端",
			"Deck 清单", "网络与信任", "系统设置", "固件更新", "诊断", "托盘 / 菜单",
			"provider-form", "management-token", "provider-headers", "map-reset-format",
			"backup-mode", "backup-conflicts", "providers_only", "replace", "merge",
		},
		"/app.js": {
			"/api/v1/session/refresh", "/api/v1/console", "/api/v1/providers", "/api/v1/providers/order", "/test",
			"/api/v1/history", "/api/v1/backups/export", "/api/v1/backups/preview",
			"/api/v1/pairing/codes", "renderDeckPreview", "showDialog", "visibilitychange",
			"textContent", "TextEncoder", "reset_format", "keep_existing",
			"keep_current", "use_backup", "resetBackupPreview", "#import-file",
		},
		"/app.css": {"focus-visible", "prefers-reduced-motion", "@media (max-width: 700px)", "@media (max-width: 410px)"},
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
		if path == "/" && strings.Count(string(body), `data-page-view="`) != 16 {
			t.Errorf("GET / contains %d authenticated views, want 16 plus login", strings.Count(string(body), `data-page-view="`))
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
