package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/configmodel"
)

type serialPresetSettingsOwner struct {
	presets []configmodel.SerialPreset
}

func (*serialPresetSettingsOwner) UpdateApplicationSettings(context.Context, configmodel.ApplicationSettings) error {
	return nil
}

func (*serialPresetSettingsOwner) UpdateHistoryEnabled(context.Context, bool) error { return nil }

func (owner *serialPresetSettingsOwner) SerialPresets(context.Context) ([]configmodel.SerialPreset, error) {
	return configmodel.CloneSerialPresets(owner.presets), nil
}

func (owner *serialPresetSettingsOwner) UpdateSerialPresets(
	_ context.Context,
	presets []configmodel.SerialPreset,
) error {
	owner.presets = configmodel.CloneSerialPresets(presets)
	return nil
}

func (*serialPresetSettingsOwner) UpdateDeviceProfile(context.Context, configmodel.DeviceProfile) error {
	return nil
}

func TestSerialPresetAPIStoresBoundedCommandsWithoutLoggingTheirPayload(t *testing.T) {
	application := newSerialHTTPRuntime(t)
	owner := &serialPresetSettingsOwner{}
	application.configuration = owner
	server := httptest.NewServer(application.managementRoutes())
	defer server.Close()
	application.config.Management.AllowedOrigin = server.URL
	const sessionToken = "serial-preset-session"
	const csrfToken = "serial-preset-csrf"
	if !application.sessions.add(sessionToken, csrfToken, time.Now()) {
		t.Fatal("add management session")
	}

	requestBody := []byte(`{"presets":[{"id":"health","name":"健康检查","mode":"text","payload":"health --token PRESET_SECRET","line_ending":"crlf"},{"id":"frame","name":"二进制帧","mode":"hex","payload":"00 ff 41","line_ending":"none"}]}`)
	request, _ := http.NewRequest(http.MethodPut, server.URL+"/api/v1/serial/presets", bytes.NewReader(requestBody))
	request.AddCookie(&http.Cookie{Name: managementSessionCookie, Value: sessionToken})
	request.Header.Set("Origin", server.URL)
	request.Header.Set("X-CSRF-Token", csrfToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseDocument, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT presets status=%d body=%s", response.StatusCode, responseDocument)
	}
	if len(owner.presets) != 2 || string(owner.presets[0].Payload) != "health --token PRESET_SECRET" ||
		!bytes.Equal(owner.presets[1].Payload, []byte{0x00, 0xff, 0x41}) {
		t.Fatalf("stored presets=%#v", owner.presets)
	}

	request, _ = http.NewRequest(http.MethodGet, server.URL+"/api/v1/serial/presets", nil)
	request.AddCookie(&http.Cookie{Name: managementSessionCookie, Value: sessionToken})
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var document struct {
		Presets []struct {
			ID      string `json:"id"`
			Payload string `json:"payload"`
		} `json:"presets"`
	}
	if err = json.NewDecoder(response.Body).Decode(&document); err != nil || response.StatusCode != http.StatusOK ||
		len(document.Presets) != 2 || document.Presets[0].Payload != "health --token PRESET_SECRET" ||
		document.Presets[1].Payload != "00 FF 41" {
		t.Fatalf("GET presets status=%d document=%#v error=%v", response.StatusCode, document, err)
	}

	const invalidCanary = "INVALID_PRESET_SECRET_CANARY"
	invalidPayload := invalidCanary + string(bytes.Repeat([]byte{'x'}, 257))
	invalidDocument, _ := json.Marshal(map[string]any{"presets": []map[string]any{{
		"id": "oversized", "name": "Oversized", "mode": "text",
		"payload": invalidPayload, "line_ending": "none",
	}}})
	request, _ = http.NewRequest(http.MethodPut, server.URL+"/api/v1/serial/presets", bytes.NewReader(invalidDocument))
	request.AddCookie(&http.Cookie{Name: managementSessionCookie, Value: sessionToken})
	request.Header.Set("Origin", server.URL)
	request.Header.Set("X-CSRF-Token", csrfToken)
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseDocument, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || bytes.Contains(responseDocument, []byte(invalidCanary)) {
		t.Fatalf("invalid preset response status=%d body=%q", response.StatusCode, responseDocument)
	}

	response, err = http.Get(server.URL + "/api/v1/serial/presets")
	if err != nil {
		t.Fatal(err)
	}
	unauthorizedDocument, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || bytes.Contains(unauthorizedDocument, []byte("PRESET_SECRET")) {
		t.Fatalf("unauthorized preset response status=%d body=%q", response.StatusCode, unauthorizedDocument)
	}
}
