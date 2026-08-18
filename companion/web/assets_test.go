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
			"ota-file", "ota-device-id", "ota-confirm", "ota-apply",
			"pairing-v2-dialog", "pairing-v2-code", "扫描并配对", "六位码只显示在 Deck",
			`role="alert"`, "console-alert", "providers-alert", "history-alert", "toast-alert-region",
		},
		"/app.js": {
			"/api/v1/session/refresh", "/api/v1/console", "/api/v1/providers", "/api/v1/providers/order", "/test",
			"/api/v1/history", "/api/v1/backups/export", "/api/v1/backups/preview",
			"/api/v1/ota/preview", "/api/v1/ota/apply", "/api/v1/ota/status",
			"/api/v1/pairing-v2/scan", "/api/v1/pairing-v2/sessions", "renderDeckPreview", "showDialog", "visibilitychange",
			"textContent", "TextEncoder", "reset_format", "keep_existing",
			"keep_current", "use_backup", "resetBackupPreview", "#import-file",
			"scrubSensitiveState", "resumePairingV2Session", "authEpoch", "TX 未启用",
			"state.sync.console.lastSuccess", "providerDataWritable", "保留最后有效数据", "确认替换当前配置",
			"/api/v1/serial/presets", "connectSerialObserver", "submitSerial",
			`$("#serial-compose").addEventListener`, `$("#serial-lease").addEventListener`,
			`$("#serial-preset-form").addEventListener`, "downloadSerialCapture",
			"serialPresetOperationController", "serialPresetOperationIsCurrent", "scrubSerialPresetEditor",
			"previewOTA", "applyOTA", "pollOTAStatus", "resetOTAPreview", "otaPreviewEpoch",
		},
		"/serial-terminal.js": {
			"S3DeckSerialTerminal", "createClient", "decodeFrame", "MAX_TRANSMIT_BYTES",
		},
		"/pairing-v2-ui.js": {
			"S3DeckPairingV2UI", "awaiting_code", "proving_link", "committing", "paired",
			"authentication_failed", "storage_failure", "link_failed", "validCode",
		},
		"/vendor/xterm/xterm.js":           {"Terminal", "Uint8Array", "dispose"},
		"/vendor/xterm/addon-fit.js":       {"FitAddon"},
		"/vendor/xterm/addon-search.js":    {"SearchAddon"},
		"/vendor/xterm/addon-unicode11.js": {"Unicode11Addon"},
		"/vendor/xterm/xterm.css":          {".xterm", ".xterm-viewport"},
		"/app.css":                         {"focus-visible", "prefers-reduced-motion", "@media (max-width: 700px)", "@media (max-width: 410px)"},
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
		firstPartyAsset := path == "/" || path == "/app.js" || path == "/app.css" ||
			path == "/pairing-v2-ui.js" || path == "/serial-terminal.js"
		if firstPartyAsset && (strings.Contains(string(body), "innerHTML") ||
			strings.Contains(string(body), "localStorage") ||
			strings.Contains(string(body), "navigator.clipboard") ||
			strings.Contains(string(body), "https://")) {
			t.Errorf("GET %s contains an unsafe browser boundary", path)
		}
	}
}
