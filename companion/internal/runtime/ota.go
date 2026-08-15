package runtime

import (
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/ota"
)

const otaApplyRequestBytes = 4 << 10

type otaApplyRequest struct {
	Receipt  string `json:"receipt"`
	DeviceID string `json:"device_id"`
	Confirm  bool   `json:"confirm"`
}

func (application *Runtime) handleOTAPreview(response http.ResponseWriter, request *http.Request) {
	if application.ota == nil {
		http.Error(response, "OTA service unavailable", http.StatusServiceUnavailable)
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/json" && mediaType != "application/vnd.s3deck.ota+json") {
		http.Error(response, "signed firmware archive required", http.StatusUnsupportedMediaType)
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, ota.MaximumArchiveBytes)
	document, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(response, "signed firmware archive rejected", http.StatusBadRequest)
		return
	}
	defer clear(document)
	preview, err := application.ota.Preview(document)
	if err != nil {
		writeOTAError(response, err)
		return
	}
	writeManagementJSON(response, preview)
}

func (application *Runtime) handleOTAApply(response http.ResponseWriter, request *http.Request) {
	if application.ota == nil {
		http.Error(response, "OTA service unavailable", http.StatusServiceUnavailable)
		return
	}
	var input otaApplyRequest
	defer func() { input.Receipt = "" }()
	if err := decodeManagementJSONLimit(response, request, &input, otaApplyRequestBytes); err != nil || !input.Confirm {
		http.Error(response, "explicit OTA confirmation required", http.StatusBadRequest)
		return
	}
	if err := application.ota.Apply(input.Receipt, input.DeviceID); err != nil {
		writeOTAError(response, err)
		return
	}
	writeManagementJSONStatus(response, http.StatusAccepted, application.ota.Status(input.DeviceID))
}

func (application *Runtime) handleOTAStatus(response http.ResponseWriter, request *http.Request) {
	if application.ota == nil {
		http.Error(response, "OTA service unavailable", http.StatusServiceUnavailable)
		return
	}
	status := application.ota.Status(request.URL.Query().Get("device_id"))
	if status.DeviceID == "" {
		http.Error(response, "OTA transaction not found", http.StatusNotFound)
		return
	}
	writeManagementJSON(response, status)
}

func writeOTAError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ota.ErrInvalidArchive), errors.Is(err, ota.ErrInvalidReceipt):
		http.Error(response, "OTA request rejected", http.StatusBadRequest)
	case errors.Is(err, ota.ErrBusy):
		http.Error(response, "OTA transaction already active", http.StatusConflict)
	default:
		http.Error(response, "OTA service unavailable", http.StatusServiceUnavailable)
	}
}
