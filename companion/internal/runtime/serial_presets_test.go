package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func (owner *serialPresetSettingsOwner) UpdateSerialPreset(
	_ context.Context,
	preset configmodel.SerialPreset,
) (bool, error) {
	for index := range owner.presets {
		if owner.presets[index].ID == preset.ID {
			configmodel.DestroySerialPresets(owner.presets[index : index+1])
			owner.presets[index] = configmodel.CloneSerialPresets([]configmodel.SerialPreset{preset})[0]
			return true, nil
		}
	}
	if len(owner.presets) >= configmodel.MaximumSerialPresets {
		return false, nil
	}
	owner.presets = append(owner.presets, configmodel.CloneSerialPresets([]configmodel.SerialPreset{preset})[0])
	return true, nil
}

func (owner *serialPresetSettingsOwner) DeleteSerialPreset(_ context.Context, identifier string) (bool, error) {
	for index := range owner.presets {
		if owner.presets[index].ID != identifier {
			continue
		}
		configmodel.DestroySerialPresets(owner.presets[index : index+1])
		copy(owner.presets[index:], owner.presets[index+1:])
		owner.presets[len(owner.presets)-1] = configmodel.SerialPreset{}
		owner.presets = owner.presets[:len(owner.presets)-1]
		return true, nil
	}
	return false, nil
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
		Presets []map[string]any `json:"presets"`
	}
	if err = json.NewDecoder(response.Body).Decode(&document); err != nil || response.StatusCode != http.StatusOK ||
		len(document.Presets) != 2 || document.Presets[0]["id"] != "health" ||
		document.Presets[0]["payload_bytes"] != float64(len("health --token PRESET_SECRET")) {
		t.Fatalf("GET presets status=%d document=%#v error=%v", response.StatusCode, document, err)
	}
	for _, preset := range document.Presets {
		if _, exposesPayload := preset["payload"]; exposesPayload {
			t.Fatalf("preset list exposed command content: %#v", preset)
		}
	}

	request, _ = http.NewRequest(http.MethodGet, server.URL+"/api/v1/serial/presets/health", nil)
	request.AddCookie(&http.Cookie{Name: managementSessionCookie, Value: sessionToken})
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var detail serialPresetDocument
	err = json.NewDecoder(response.Body).Decode(&detail)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || detail.ID != "health" ||
		detail.Payload != "health --token PRESET_SECRET" {
		t.Fatalf("GET preset detail status=%d document=%#v error=%v", response.StatusCode, detail, err)
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

func TestSerialPresetAPIAcceptsTheMaximumLegalCollectionAndMutatesOneItem(t *testing.T) {
	application := newSerialHTTPRuntime(t)
	owner := &serialPresetSettingsOwner{}
	application.configuration = owner
	server := httptest.NewServer(application.managementRoutes())
	defer server.Close()
	application.config.Management.AllowedOrigin = server.URL
	const sessionToken = "serial-preset-maximum-session"
	const csrfToken = "serial-preset-maximum-csrf"
	if !application.sessions.add(sessionToken, csrfToken, time.Now()) {
		t.Fatal("add management session")
	}

	documents := make([]serialPresetDocument, configmodel.MaximumSerialPresets)
	for index := range documents {
		documents[index] = serialPresetDocument{
			ID: fmt.Sprintf("preset_%02d", index), Name: strings.Repeat("W", 48),
			Mode: configmodel.SerialPresetHex, Payload: strings.TrimSpace(strings.Repeat("00 ", 256)),
			LineEnding: configmodel.SerialLineEndingNone,
		}
	}
	body, err := json.Marshal(map[string]any{"presets": documents})
	if err != nil || len(body) <= managementLoginMaxBytes {
		t.Fatalf("maximum preset document bytes=%d error=%v", len(body), err)
	}
	request, _ := http.NewRequest(http.MethodPut, server.URL+"/api/v1/serial/presets", bytes.NewReader(body))
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
	if response.StatusCode != http.StatusNoContent || len(owner.presets) != configmodel.MaximumSerialPresets {
		t.Fatalf("maximum PUT status=%d presets=%d body=%s", response.StatusCode, len(owner.presets), responseDocument)
	}

	updated := serialPresetDocument{
		ID: "preset_07", Name: "Revised", Mode: configmodel.SerialPresetText,
		Payload: "status", LineEnding: configmodel.SerialLineEndingLF,
	}
	body, _ = json.Marshal(updated)
	request, _ = http.NewRequest(http.MethodPut, server.URL+"/api/v1/serial/presets/preset_07", bytes.NewReader(body))
	request.AddCookie(&http.Cookie{Name: managementSessionCookie, Value: sessionToken})
	request.Header.Set("Origin", server.URL)
	request.Header.Set("X-CSRF-Token", csrfToken)
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent || owner.presets[7].Name != "Revised" ||
		string(owner.presets[7].Payload) != "status" {
		t.Fatalf("single PUT status=%d preset=%#v", response.StatusCode, owner.presets[7])
	}

	request, _ = http.NewRequest(http.MethodDelete, server.URL+"/api/v1/serial/presets/preset_07", nil)
	request.AddCookie(&http.Cookie{Name: managementSessionCookie, Value: sessionToken})
	request.Header.Set("Origin", server.URL)
	request.Header.Set("X-CSRF-Token", csrfToken)
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent || len(owner.presets) != configmodel.MaximumSerialPresets-1 {
		t.Fatalf("single DELETE status=%d presets=%d", response.StatusCode, len(owner.presets))
	}
}
