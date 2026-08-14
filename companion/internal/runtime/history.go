package runtime

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/history"
)

func (application *Runtime) handleHistoryQuery(response http.ResponseWriter, request *http.Request) {
	store := application.history
	if store == nil {
		http.Error(response, "Provider history unavailable", http.StatusServiceUnavailable)
		return
	}
	query, err := historyQuery(request, 1000)
	if err != nil {
		http.Error(response, "invalid Provider history query", http.StatusBadRequest)
		return
	}
	records, err := store.Query(request.Context(), query)
	if err != nil {
		writeHistoryError(response, err)
		return
	}
	writeManagementJSON(response, struct {
		Records []history.Record `json:"records"`
	}{Records: records})
}

func (application *Runtime) handleHistoryExport(response http.ResponseWriter, request *http.Request) {
	store := application.history
	if store == nil {
		http.Error(response, "Provider history unavailable", http.StatusServiceUnavailable)
		return
	}
	query, err := historyQuery(request, 20_000)
	if err != nil {
		http.Error(response, "invalid Provider history query", http.StatusBadRequest)
		return
	}
	response.Header().Set("Content-Type", "text/csv; charset=utf-8")
	response.Header().Set("Content-Disposition", `attachment; filename="provider-history.csv"`)
	response.Header().Set("Cache-Control", "no-store")
	if err = store.ExportCSV(request.Context(), response, query); err != nil {
		// Export performs its complete bounded database read before its first
		// response write, so query/storage failures remain safe to classify here.
		writeHistoryError(response, err)
	}
}

func (application *Runtime) handleHistorySettings(response http.ResponseWriter, _ *http.Request) {
	if application.history == nil {
		http.Error(response, "Provider history unavailable", http.StatusServiceUnavailable)
		return
	}
	writeManagementJSON(response, application.history.Settings())
}

func (application *Runtime) handleHistorySettingsUpdate(response http.ResponseWriter, request *http.Request) {
	if application.history == nil {
		http.Error(response, "Provider history unavailable", http.StatusServiceUnavailable)
		return
	}
	var settings struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeManagementJSON(response, request, &settings); err != nil {
		http.Error(response, "malformed Provider history settings", http.StatusBadRequest)
		return
	}
	if err := application.history.SetEnabled(request.Context(), settings.Enabled); err != nil {
		writeHistoryError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (application *Runtime) handleHistoryClear(response http.ResponseWriter, request *http.Request) {
	if application.history == nil {
		http.Error(response, "Provider history unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := application.history.Clear(request.Context()); err != nil {
		writeHistoryError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func historyQuery(request *http.Request, defaultLimit int) (history.Query, error) {
	values := request.URL.Query()
	allowed := map[string]bool{"provider_id": true, "from": true, "until": true, "limit": true}
	for key, entries := range values {
		if !allowed[key] || len(entries) != 1 {
			return history.Query{}, history.ErrInvalid
		}
	}
	until := time.Now().UTC().Truncate(time.Hour).Add(time.Hour)
	from := until.Add(-history.DefaultRetention)
	var err error
	if value := values.Get("from"); value != "" {
		from, err = canonicalHistoryTime(value)
		if err != nil {
			return history.Query{}, err
		}
	}
	if value := values.Get("until"); value != "" {
		until, err = canonicalHistoryTime(value)
		if err != nil {
			return history.Query{}, err
		}
	}
	limit := defaultLimit
	if value := values.Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil {
			return history.Query{}, history.ErrInvalid
		}
	}
	return history.Query{
		ProviderID: values.Get("provider_id"),
		From:       from,
		Until:      until,
		Limit:      limit,
	}, nil
}

func canonicalHistoryTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC || parsed.Format(time.RFC3339Nano) != value {
		return time.Time{}, history.ErrInvalid
	}
	return parsed, nil
}

func writeHistoryError(response http.ResponseWriter, err error) {
	if errors.Is(err, history.ErrInvalid) {
		http.Error(response, "invalid Provider history request", http.StatusBadRequest)
		return
	}
	http.Error(response, "Provider history unavailable", http.StatusServiceUnavailable)
}
