package runtime

import (
	"errors"
	"net/http"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/structuredprovider"
)

const providerManagementMaxBytes = 64 << 10

type providerSaveRequest struct {
	Definition   structuredprovider.Definition `json:"definition"`
	Secrets      []providerSecretInput         `json:"secrets"`
	KeepExisting []int                         `json:"keep_existing"`
}

type providerSecretInput struct {
	HeaderIndex int    `json:"header_index"`
	Value       []byte `json:"value"`
}

type providerListResponse struct {
	Providers []structuredprovider.DefinitionView `json:"providers"`
	Templates []structuredprovider.DefinitionView `json:"templates"`
	States    []aisnapshot.Provider               `json:"states"`
}

func (application *Runtime) handleProviders(response http.ResponseWriter, request *http.Request) {
	if application.structuredService == nil {
		http.Error(response, "Provider management unavailable", http.StatusServiceUnavailable)
		return
	}
	providers, err := application.structuredService.List(request.Context())
	if err != nil {
		http.Error(response, "Provider management unavailable", http.StatusServiceUnavailable)
		return
	}
	writeManagementJSON(response, providerListResponse{
		Providers: providers,
		Templates: application.structuredService.Templates(),
		States:    application.StructuredProviders(),
	})
}

func (application *Runtime) handleProviderCreate(response http.ResponseWriter, request *http.Request) {
	application.handleProviderSave(response, request, "")
}

func (application *Runtime) handleProviderUpdate(response http.ResponseWriter, request *http.Request) {
	application.handleProviderSave(response, request, request.PathValue("providerID"))
}

func (application *Runtime) handleProviderSave(
	response http.ResponseWriter,
	request *http.Request,
	currentID string,
) {
	if application.structuredService == nil {
		http.Error(response, "Provider management unavailable", http.StatusServiceUnavailable)
		return
	}
	var input providerSaveRequest
	if err := decodeManagementJSONLimit(
		response, request, &input, providerManagementMaxBytes,
	); err != nil {
		http.Error(response, "malformed Provider request", http.StatusBadRequest)
		return
	}
	defer func() {
		for index := range input.Secrets {
			clear(input.Secrets[index].Value)
			input.Secrets[index].Value = nil
		}
	}()
	if currentID != "" && input.Definition.ID != currentID {
		http.Error(response, "invalid Provider request", http.StatusBadRequest)
		return
	}
	bindings := make([]structuredprovider.SecretBinding, len(input.Secrets))
	for index := range input.Secrets {
		bindings[index] = structuredprovider.SecretBinding{
			HeaderIndex: input.Secrets[index].HeaderIndex,
			Value:       input.Secrets[index].Value,
		}
	}
	provider, err := application.structuredService.Save(
		request.Context(), currentID, input.Definition, bindings, input.KeepExisting,
	)
	if err != nil {
		writeProviderManagementError(response, err)
		return
	}
	status := http.StatusOK
	if currentID == "" {
		status = http.StatusCreated
	}
	writeManagementJSONStatus(response, status, provider)
}

func (application *Runtime) handleProviderOrder(response http.ResponseWriter, request *http.Request) {
	if application.structuredService == nil {
		http.Error(response, "Provider management unavailable", http.StatusServiceUnavailable)
		return
	}
	var input struct {
		ProviderIDs []string `json:"provider_ids"`
	}
	if err := decodeManagementJSONLimit(
		response, request, &input, providerManagementMaxBytes,
	); err != nil {
		http.Error(response, "malformed Provider order", http.StatusBadRequest)
		return
	}
	if err := application.structuredService.Reorder(request.Context(), input.ProviderIDs); err != nil {
		writeProviderManagementError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (application *Runtime) handleProviderDelete(response http.ResponseWriter, request *http.Request) {
	if application.structuredService == nil {
		http.Error(response, "Provider management unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := application.structuredService.Delete(
		request.Context(), request.PathValue("providerID"),
	); err != nil {
		writeProviderManagementError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (application *Runtime) handleProviderTest(response http.ResponseWriter, request *http.Request) {
	if application.structuredService == nil {
		http.Error(response, "Provider management unavailable", http.StatusServiceUnavailable)
		return
	}
	preview, testErr := application.structuredService.Test(
		request.Context(), request.PathValue("providerID"),
	)
	writeManagementJSON(response, struct {
		OK      bool                       `json:"ok"`
		Preview structuredprovider.Preview `json:"preview"`
	}{OK: testErr == nil, Preview: preview})
}

func writeProviderManagementError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, structuredprovider.ErrInvalidConfig):
		http.Error(response, "invalid Provider request", http.StatusBadRequest)
	case errors.Is(err, structuredprovider.ErrDefinitionCommit):
		http.Error(response, "Provider configuration changed", http.StatusConflict)
	default:
		http.Error(response, "Provider operation unavailable", http.StatusServiceUnavailable)
	}
}
