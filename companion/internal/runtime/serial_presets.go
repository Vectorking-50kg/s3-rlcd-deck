package runtime

import (
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/configmodel"
)

type serialPresetDocument struct {
	ID         string                       `json:"id"`
	Name       string                       `json:"name"`
	Mode       configmodel.SerialPresetMode `json:"mode"`
	Payload    string                       `json:"payload"`
	LineEnding configmodel.SerialLineEnding `json:"line_ending"`
}

func (application *Runtime) handleSerialPresets(response http.ResponseWriter, request *http.Request) {
	if application.configuration == nil {
		http.Error(response, "Serial presets unavailable", http.StatusServiceUnavailable)
		return
	}
	presets, err := application.configuration.SerialPresets(request.Context())
	if err != nil {
		http.Error(response, "Serial presets unavailable", http.StatusServiceUnavailable)
		return
	}
	defer configmodel.DestroySerialPresets(presets)
	documents := make([]serialPresetDocument, len(presets))
	for index := range presets {
		documents[index] = serialPresetDocument{
			ID: presets[index].ID, Name: presets[index].Name, Mode: presets[index].Mode,
			LineEnding: presets[index].LineEnding,
		}
		if presets[index].Mode == configmodel.SerialPresetHex {
			documents[index].Payload = formatPresetHex(presets[index].Payload)
		} else {
			documents[index].Payload = string(presets[index].Payload)
		}
	}
	writeManagementJSON(response, struct {
		Presets []serialPresetDocument `json:"presets"`
	}{Presets: documents})
}

func (application *Runtime) handleSerialPresetsUpdate(response http.ResponseWriter, request *http.Request) {
	if application.configuration == nil {
		http.Error(response, "Serial presets unavailable", http.StatusServiceUnavailable)
		return
	}
	var document struct {
		Presets []serialPresetDocument `json:"presets"`
	}
	if err := decodeManagementJSON(response, request, &document); err != nil || document.Presets == nil {
		http.Error(response, "malformed Serial presets", http.StatusBadRequest)
		return
	}
	presets := make([]configmodel.SerialPreset, len(document.Presets))
	defer configmodel.DestroySerialPresets(presets)
	for index, source := range document.Presets {
		payload := []byte(source.Payload)
		if source.Mode == configmodel.SerialPresetHex {
			var valid bool
			payload, valid = parsePresetHex(source.Payload)
			if !valid {
				http.Error(response, "invalid Serial presets", http.StatusBadRequest)
				return
			}
		}
		presets[index] = configmodel.SerialPreset{
			ID: source.ID, Name: source.Name, Mode: source.Mode,
			Payload: payload, LineEnding: source.LineEnding,
		}
	}
	if !configmodel.ValidateSerialPresets(presets) {
		http.Error(response, "invalid Serial presets", http.StatusBadRequest)
		return
	}
	if err := application.configuration.UpdateSerialPresets(request.Context(), presets); err != nil {
		http.Error(response, "Serial presets unavailable", http.StatusServiceUnavailable)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func parsePresetHex(value string) ([]byte, bool) {
	if value == "" {
		return nil, false
	}
	var compact strings.Builder
	compact.Grow(len(value))
	for _, character := range value {
		switch {
		case character >= '0' && character <= '9', character >= 'a' && character <= 'f', character >= 'A' && character <= 'F':
			compact.WriteRune(character)
		case character == ' ', character == '\t', character == '\r', character == '\n':
		default:
			return nil, false
		}
	}
	if compact.Len() == 0 || compact.Len()%2 != 0 || compact.Len()/2 > 256 {
		return nil, false
	}
	payload, err := hex.DecodeString(compact.String())
	return payload, err == nil
}

func formatPresetHex(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	encoded := strings.ToUpper(hex.EncodeToString(payload))
	var formatted strings.Builder
	formatted.Grow(len(encoded) + len(payload) - 1)
	for index := 0; index < len(encoded); index += 2 {
		if index != 0 {
			formatted.WriteByte(' ')
		}
		formatted.WriteString(encoded[index : index+2])
	}
	return formatted.String()
}
