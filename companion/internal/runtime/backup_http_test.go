package runtime_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/backup"
	companionruntime "github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/runtime"
)

type backupHTTPService struct {
	passphrase       string
	archive          []byte
	previewID        string
	imported         bool
	importPassphrase string
	importArchive    []byte
	importMode       backup.ImportMode
	importPreviewID  string
}

func (service *backupHTTPService) Export(_ context.Context, passphrase []byte) ([]byte, error) {
	service.passphrase = string(passphrase)
	return []byte("age-encrypted-archive"), nil
}

func (service *backupHTTPService) Preview(
	_ context.Context,
	archive []byte,
	passphrase []byte,
	mode backup.ImportMode,
) (backup.Preview, error) {
	service.passphrase = string(passphrase)
	service.archive = append([]byte(nil), archive...)
	service.previewID = "preview_receipt_without_secrets"
	return backup.Preview{
		SchemaVersion: backup.SchemaVersion{Major: 1, Minor: 0},
		Mode:          mode,
		PreviewID:     service.previewID,
		Providers: []backup.ProviderPreview{{
			ID: "aihubmix", DisplayName: "AIHubMix", SecretCount: 1,
		}},
		ExcludedDataClasses: []string{"pairing_tokens"},
	}, nil
}

func (service *backupHTTPService) Import(
	_ context.Context,
	archive []byte,
	passphrase []byte,
	mode backup.ImportMode,
	_ map[string]backup.ConflictDecision,
	previewID string,
) (backup.ImportResult, error) {
	service.importPassphrase = string(passphrase)
	service.importArchive = append([]byte(nil), archive...)
	service.importMode = mode
	service.importPreviewID = previewID
	service.imported = bytes.Equal(archive, service.archive) &&
		string(passphrase) == service.passphrase && mode == backup.ModeReplace &&
		previewID == service.previewID
	return backup.ImportResult{
		Committed: true, RestartRequired: true, ImportedProviders: 1,
	}, nil
}

func TestManagementBackupRoutesRequirePreviewAndKeepCredentialsOutOfResponses(t *testing.T) {
	const passphrase = "PRIVATE_BACKUP_PASSPHRASE_CANARY"
	service := &backupHTTPService{}
	config := testConfig()
	config.Backup = service
	application, err := companionruntime.New(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	status := waitForState(t, application, companionruntime.StateReady)
	client, session, csrf := loginManagement(t, status.ManagementAddress, config.Management.AdminToken)

	exportBody, _ := json.Marshal(map[string]string{"passphrase": passphrase})
	response := managementWrite(t, client, status.ManagementAddress, session, csrf, "/api/v1/backups/export", exportBody)
	exported, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(exported) != "age-encrypted-archive" ||
		response.Header.Get("Content-Type") != "application/vnd.age" || service.passphrase != passphrase {
		t.Fatalf("export status=%d headers=%v body=%q", response.StatusCode, response.Header, exported)
	}

	previewBody, _ := json.Marshal(map[string]any{
		"passphrase": passphrase,
		"archive":    base64.StdEncoding.EncodeToString(exported),
		"mode":       backup.ModeReplace,
	})
	response = managementWrite(t, client, status.ManagementAddress, session, csrf, "/api/v1/backups/preview", previewBody)
	previewResponse, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || bytes.Contains(previewResponse, []byte(passphrase)) ||
		bytes.Contains(previewResponse, exported) || !strings.Contains(string(previewResponse), service.previewID) {
		t.Fatalf("preview status=%d body=%q", response.StatusCode, previewResponse)
	}

	importBody, _ := json.Marshal(map[string]any{
		"passphrase": passphrase,
		"archive":    base64.StdEncoding.EncodeToString(exported),
		"mode":       backup.ModeReplace,
		"preview_id": service.previewID,
	})
	response = managementWrite(t, client, status.ManagementAddress, session, csrf, "/api/v1/backups/import", importBody)
	importResponse, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted || !service.imported ||
		bytes.Contains(importResponse, []byte(passphrase)) || bytes.Contains(importResponse, exported) {
		t.Fatalf("import status=%d imported=%v body=%q pass=%q wantpass=%q archive=%q wantarchive=%q mode=%q preview=%q wantpreview=%q", response.StatusCode, service.imported, importResponse, service.importPassphrase, service.passphrase, service.importArchive, service.archive, service.importMode, service.importPreviewID, service.previewID)
	}

	unauthorized, _ := http.NewRequest(
		http.MethodPost,
		"http://"+status.ManagementAddress+"/api/v1/backups/import",
		bytes.NewReader(importBody),
	)
	unauthorized.Header.Set("Content-Type", "application/json")
	response, err = client.Do(unauthorized)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated import status=%d", response.StatusCode)
	}

	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func managementWrite(
	t *testing.T,
	client *http.Client,
	address string,
	session *http.Cookie,
	csrf string,
	path string,
	body []byte,
) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "http://"+address+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(session)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://"+address)
	request.Header.Set("X-CSRF-Token", csrf)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
