package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/diagnostics"
)

var diagnosticConfigurationSchemaKeys = []string{
	"application.history_enabled",
	"application.serial_presets[]",
	"devices[].board",
	"devices[].capabilities[]",
	"devices[].firmware_version",
	"providers[].display_name",
	"providers[].experimental",
	"providers[].mapping",
	"providers[].refresh_minutes",
	"providers[].request.method",
	"providers[].request_timeout_seconds",
	"web.allow_lan",
	"web.allowed_origin",
	"web.management_address",
}

type diagnosticStatus struct {
	diagnostics.Status
	DeckRings   int      `json:"deck_rings"`
	BundleFiles []string `json:"bundle_files"`
}

func (application *Runtime) handleDiagnosticsStatus(
	response http.ResponseWriter,
	_ *http.Request,
) {
	if application.diagnostics == nil {
		http.Error(response, "diagnostics unavailable", http.StatusServiceUnavailable)
		return
	}
	writeManagementJSON(response, diagnosticStatus{
		Status:    application.diagnostics.Status(),
		DeckRings: len(application.deviceLink.DiagnosticRings()),
		BundleFiles: []string{
			"manifest.json", "companion/events.jsonl", "deck/ring.json",
			"configuration/schema-keys.json",
		},
	})
}

func (application *Runtime) handleDiagnosticsExport(
	response http.ResponseWriter,
	request *http.Request,
) {
	if application.diagnostics == nil {
		http.Error(response, "diagnostics unavailable", http.StatusServiceUnavailable)
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, 1)
	body, readErr := io.ReadAll(request.Body)
	if readErr != nil || len(body) != 0 {
		http.Error(response, "malformed diagnostic export request", http.StatusBadRequest)
		return
	}
	deviceSnapshots := application.deviceLink.DiagnosticRings()
	rings := make([]diagnostics.DeckRing, len(deviceSnapshots))
	for index, device := range deviceSnapshots {
		ring := diagnostics.DeckRing{
			DeviceIDHash: diagnostics.HashIdentifier(device.DeviceID),
			Dropped:      device.Snapshot.Dropped,
			Events:       make([]diagnostics.DeckEvent, len(device.Snapshot.Events)),
		}
		for eventIndex, event := range device.Snapshot.Events {
			ring.Events[eventIndex] = diagnostics.DeckEvent{
				MonotonicMS: event.MonotonicMS,
				Level:       diagnostics.DeckLevel(event.Level),
				Component:   diagnostics.DeckComponent(event.Component),
				Code:        diagnostics.DeckCode(event.Code),
				Value:       event.Value,
			}
		}
		rings[index] = ring
	}
	bundle, _, err := application.diagnostics.Export(request.Context(), diagnostics.BundleInput{
		BuildVersion: application.config.Version,
		BuildCommit:  application.config.Commit,
		ConfigurationSchemaKeys: append(
			[]string(nil),
			diagnosticConfigurationSchemaKeys...,
		),
		DeckRings: rings,
	})
	if err != nil {
		http.Error(response, "diagnostic bundle unavailable", http.StatusServiceUnavailable)
		return
	}
	digest := sha256.Sum256(bundle)
	response.Header().Set("Content-Type", "application/zip")
	response.Header().Set("Content-Disposition", `attachment; filename="s3-rlcd-deck-diagnostics.zip"`)
	response.Header().Set("X-Content-SHA256", hex.EncodeToString(digest[:]))
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(http.StatusOK)
	written, writeErr := response.Write(bundle)
	if writeErr == nil && written == len(bundle) {
		application.recordDiagnostic(diagnostics.Event{
			Level: diagnostics.LevelInfo, Module: diagnostics.ModuleDiagnostics,
			Code: diagnostics.CodeBundleExported,
		})
	}
}
