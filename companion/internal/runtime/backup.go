package runtime

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/backup"
)

const maximumBackupRequestBytes = 12 << 20

type backupExportRequest struct {
	Passphrase string `json:"passphrase"`
}

type backupImportRequest struct {
	Passphrase string                             `json:"passphrase"`
	Archive    []byte                             `json:"archive"`
	Mode       backup.ImportMode                  `json:"mode"`
	Decisions  map[string]backup.ConflictDecision `json:"decisions,omitempty"`
	PreviewID  string                             `json:"preview_id,omitempty"`
}

func (application *Runtime) handleBackupExport(
	response http.ResponseWriter,
	request *http.Request,
) {
	response.Header().Set("Cache-Control", "no-store")
	if application.backup == nil {
		http.Error(response, "backup service unavailable", http.StatusServiceUnavailable)
		return
	}
	var input backupExportRequest
	if err := decodeManagementJSON(response, request, &input); err != nil {
		http.Error(response, "malformed backup export request", http.StatusBadRequest)
		return
	}
	passphrase := []byte(input.Passphrase)
	input.Passphrase = ""
	defer overwriteRuntimeBytes(passphrase)
	encrypted, err := application.backup.Export(request.Context(), passphrase)
	if err != nil {
		writeBackupError(response, err)
		return
	}
	defer overwriteRuntimeBytes(encrypted)
	response.Header().Set("Content-Type", "application/vnd.age")
	response.Header().Set("Content-Disposition", `attachment; filename="s3-rlcd-deck-backup.age"`)
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = response.Write(encrypted)
}

func (application *Runtime) handleBackupPreview(
	response http.ResponseWriter,
	request *http.Request,
) {
	response.Header().Set("Cache-Control", "no-store")
	if application.backup == nil {
		http.Error(response, "backup service unavailable", http.StatusServiceUnavailable)
		return
	}
	input, passphrase, ok := decodeBackupImportRequest(response, request)
	if !ok {
		return
	}
	defer input.destroy(passphrase)
	preview, err := application.backup.Preview(
		request.Context(), input.Archive, passphrase, input.Mode,
	)
	if err != nil {
		writeBackupError(response, err)
		return
	}
	writeManagementJSON(response, preview)
}

func (application *Runtime) handleBackupImport(
	response http.ResponseWriter,
	request *http.Request,
) {
	response.Header().Set("Cache-Control", "no-store")
	if application.backup == nil {
		http.Error(response, "backup service unavailable", http.StatusServiceUnavailable)
		return
	}
	input, passphrase, ok := decodeBackupImportRequest(response, request)
	if !ok {
		return
	}
	defer input.destroy(passphrase)
	result, err := application.backup.Import(
		request.Context(), input.Archive, passphrase, input.Mode, input.Decisions, input.PreviewID,
	)
	if err != nil && !result.Committed && !errors.Is(err, backup.ErrCleanupPending) {
		writeBackupError(response, err)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(response).Encode(result)
}

func decodeBackupImportRequest(
	response http.ResponseWriter,
	request *http.Request,
) (*backupImportRequest, []byte, bool) {
	var input backupImportRequest
	if err := decodeManagementJSONLimit(
		response, request, &input, maximumBackupRequestBytes,
	); err != nil {
		input.destroy(nil)
		http.Error(response, "malformed backup request", http.StatusBadRequest)
		return nil, nil, false
	}
	passphrase := []byte(input.Passphrase)
	input.Passphrase = ""
	return &input, passphrase, true
}

func (input *backupImportRequest) destroy(passphrase []byte) {
	if input == nil {
		return
	}
	input.Passphrase = ""
	input.PreviewID = ""
	overwriteRuntimeBytes(passphrase)
	overwriteRuntimeBytes(input.Archive)
	input.Archive = nil
	clear(input.Decisions)
	input.Decisions = nil
}

func writeBackupError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, backup.ErrInvalidPassphrase),
		errors.Is(err, backup.ErrDecrypt),
		errors.Is(err, backup.ErrArchiveSchema),
		errors.Is(err, backup.ErrArchiveTooLarge),
		errors.Is(err, backup.ErrInvalidMode),
		errors.Is(err, backup.ErrConflictDecision),
		errors.Is(err, backup.ErrPreviewRequired):
		http.Error(response, "backup request rejected", http.StatusBadRequest)
	default:
		http.Error(response, "backup service unavailable", http.StatusServiceUnavailable)
	}
}

func overwriteRuntimeBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
