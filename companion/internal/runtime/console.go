package runtime

import (
	"net/http"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
)

// consoleViewResponse is the management-safe ViewModel consumed by the
// embedded Web console. It deliberately contains normalized DTOs only: raw
// Provider responses, credentials, prompts, paths and serial bodies have no
// representation at this seam.
type consoleViewResponse struct {
	Runtime      Status                `json:"runtime"`
	Providers    []aisnapshot.Provider `json:"providers"`
	Sessions     []aisnapshot.Session  `json:"sessions"`
	Capabilities consoleCapabilities   `json:"capabilities"`
}

type consoleCapabilities struct {
	ProviderManagement bool `json:"provider_management"`
	History            bool `json:"history"`
	Backup             bool `json:"backup"`
	Pairing            bool `json:"pairing"`
	Serial             bool `json:"serial"`
	Updates            bool `json:"updates"`
	Diagnostics        bool `json:"diagnostics"`
}

func (application *Runtime) handleConsoleView(response http.ResponseWriter, _ *http.Request) {
	writeManagementJSON(response, application.consoleView())
}

func (application *Runtime) consoleView() consoleViewResponse {
	providers := make([]aisnapshot.Provider, 0, 10)
	sessions := make([]aisnapshot.Session, 0, 16)
	if update, exists := application.CodexUpdate(); exists {
		providers = append(providers, update.Provider)
		sessions = append(sessions, update.Sessions...)
	}
	if cursor, exists := application.CursorProvider(); exists {
		providers = append(providers, cursor)
	}
	providers = append(providers, application.StructuredProviders()...)
	if providers == nil {
		providers = []aisnapshot.Provider{}
	}
	if sessions == nil {
		sessions = []aisnapshot.Session{}
	}
	status := application.Status()
	return consoleViewResponse{
		Runtime:   status,
		Providers: providers,
		Sessions:  sessions,
		Capabilities: consoleCapabilities{
			ProviderManagement: application.structuredService != nil,
			History:            status.HistoryAvailable,
			Backup:             application.backup != nil,
			Pairing:            application.pairing != nil,
			Serial:             application.serialHub != nil,
			Updates:            false,
			Diagnostics:        false,
		},
	}
}
