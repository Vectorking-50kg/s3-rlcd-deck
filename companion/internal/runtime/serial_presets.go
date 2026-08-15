package runtime

import (
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/configmodel"
)

const maximumSerialPresetCollectionBytes = 96 << 10

type serialPresetDocument struct {
	ID         string                       `json:"id"`
	Name       string                       `json:"name"`
	Mode       configmodel.SerialPresetMode `json:"mode"`
	Payload    string                       `json:"payload"`
	LineEnding configmodel.SerialLineEnding `json:"line_ending"`
}

type serialPresetSummaryDocument struct {
	ID            string                       `json:"id"`
	Name          string                       `json:"name"`
	Mode          configmodel.SerialPresetMode `json:"mode"`
	PayloadBytes  int                          `json:"payload_bytes"`
	TransmitBytes int                          `json:"transmit_bytes"`
	LineEnding    configmodel.SerialLineEnding `json:"line_ending"`
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
	documents := make([]serialPresetSummaryDocument, len(presets))
	for index := range presets {
		documents[index] = serialPresetSummaryDocument{
			ID: presets[index].ID, Name: presets[index].Name, Mode: presets[index].Mode,
			PayloadBytes:  len(presets[index].Payload),
			TransmitBytes: len(presets[index].Payload) + presetLineEndingBytes(presets[index]),
			LineEnding:    presets[index].LineEnding,
		}
	}
	writeManagementJSON(response, struct {
		Presets []serialPresetSummaryDocument `json:"presets"`
	}{Presets: documents})
}

func (application *Runtime) handleSerialPreset(response http.ResponseWriter, request *http.Request) {
	presets, available := application.loadSerialPresets(response, request)
	if !available {
		return
	}
	defer configmodel.DestroySerialPresets(presets)
	identifier := request.PathValue("presetID")
	for index := range presets {
		if presets[index].ID == identifier {
			writeManagementJSON(response, serialPresetDocumentFromPreset(presets[index]))
			return
		}
	}
	http.Error(response, "Serial preset not found", http.StatusNotFound)
}

func (application *Runtime) handleSerialPresetsUpdate(response http.ResponseWriter, request *http.Request) {
	if application.configuration == nil {
		http.Error(response, "Serial presets unavailable", http.StatusServiceUnavailable)
		return
	}
	var document struct {
		Presets []serialPresetDocument `json:"presets"`
	}
	if err := decodeManagementJSONLimit(
		response, request, &document, maximumSerialPresetCollectionBytes,
	); err != nil || document.Presets == nil {
		http.Error(response, "malformed Serial presets", http.StatusBadRequest)
		return
	}
	defer clearSerialPresetDocuments(document.Presets)
	presets := make([]configmodel.SerialPreset, len(document.Presets))
	defer configmodel.DestroySerialPresets(presets)
	for index, source := range document.Presets {
		preset, valid := serialPresetFromDocument(source)
		if !valid {
			http.Error(response, "invalid Serial presets", http.StatusBadRequest)
			return
		}
		presets[index] = preset
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

func (application *Runtime) handleSerialPresetUpdate(response http.ResponseWriter, request *http.Request) {
	var document serialPresetDocument
	if err := decodeManagementJSON(response, request, &document); err != nil {
		http.Error(response, "malformed Serial preset", http.StatusBadRequest)
		return
	}
	defer func() { document.Payload = "" }()
	identifier := request.PathValue("presetID")
	preset, valid := serialPresetFromDocument(document)
	if !valid || identifier == "" || preset.ID != identifier {
		configmodel.DestroySerialPresets([]configmodel.SerialPreset{preset})
		http.Error(response, "invalid Serial preset", http.StatusBadRequest)
		return
	}
	defer configmodel.DestroySerialPresets([]configmodel.SerialPreset{preset})
	presets, available := application.loadSerialPresets(response, request)
	if !available {
		return
	}
	defer configmodel.DestroySerialPresets(presets)
	replaced := false
	for index := range presets {
		if presets[index].ID == identifier {
			clear(presets[index].Payload)
			presets[index] = configmodel.CloneSerialPresets([]configmodel.SerialPreset{preset})[0]
			replaced = true
			break
		}
	}
	if !replaced {
		presets = append(presets, configmodel.CloneSerialPresets([]configmodel.SerialPreset{preset})[0])
	}
	if !configmodel.ValidateSerialPresets(presets) {
		http.Error(response, "invalid Serial preset", http.StatusBadRequest)
		return
	}
	if err := application.configuration.UpdateSerialPresets(request.Context(), presets); err != nil {
		http.Error(response, "Serial presets unavailable", http.StatusServiceUnavailable)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (application *Runtime) handleSerialPresetDelete(response http.ResponseWriter, request *http.Request) {
	presets, available := application.loadSerialPresets(response, request)
	if !available {
		return
	}
	defer configmodel.DestroySerialPresets(presets)
	identifier := request.PathValue("presetID")
	index := -1
	for candidate := range presets {
		if presets[candidate].ID == identifier {
			index = candidate
			break
		}
	}
	if index < 0 {
		http.Error(response, "Serial preset not found", http.StatusNotFound)
		return
	}
	clear(presets[index].Payload)
	copy(presets[index:], presets[index+1:])
	presets[len(presets)-1] = configmodel.SerialPreset{}
	presets = presets[:len(presets)-1]
	if err := application.configuration.UpdateSerialPresets(request.Context(), presets); err != nil {
		http.Error(response, "Serial presets unavailable", http.StatusServiceUnavailable)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (application *Runtime) loadSerialPresets(
	response http.ResponseWriter,
	request *http.Request,
) ([]configmodel.SerialPreset, bool) {
	if application.configuration == nil {
		http.Error(response, "Serial presets unavailable", http.StatusServiceUnavailable)
		return nil, false
	}
	presets, err := application.configuration.SerialPresets(request.Context())
	if err != nil {
		http.Error(response, "Serial presets unavailable", http.StatusServiceUnavailable)
		return nil, false
	}
	return presets, true
}

func serialPresetFromDocument(source serialPresetDocument) (configmodel.SerialPreset, bool) {
	var payload []byte
	if source.Mode == configmodel.SerialPresetHex {
		var valid bool
		payload, valid = parsePresetHex(source.Payload)
		if !valid {
			return configmodel.SerialPreset{}, false
		}
	} else {
		payload = []byte(source.Payload)
	}
	preset := configmodel.SerialPreset{
		ID: source.ID, Name: source.Name, Mode: source.Mode,
		Payload: payload, LineEnding: source.LineEnding,
	}
	if !configmodel.ValidateSerialPresets([]configmodel.SerialPreset{preset}) {
		clear(preset.Payload)
		return configmodel.SerialPreset{}, false
	}
	return preset, true
}

func serialPresetDocumentFromPreset(preset configmodel.SerialPreset) serialPresetDocument {
	document := serialPresetDocument{
		ID: preset.ID, Name: preset.Name, Mode: preset.Mode, LineEnding: preset.LineEnding,
	}
	if preset.Mode == configmodel.SerialPresetHex {
		document.Payload = formatPresetHex(preset.Payload)
	} else {
		document.Payload = string(preset.Payload)
	}
	return document
}

func clearSerialPresetDocuments(documents []serialPresetDocument) {
	for index := range documents {
		documents[index].Payload = ""
	}
}

func presetLineEndingBytes(preset configmodel.SerialPreset) int {
	switch preset.LineEnding {
	case configmodel.SerialLineEndingCurrent, configmodel.SerialLineEndingCRLF:
		return 2
	case configmodel.SerialLineEndingLF, configmodel.SerialLineEndingCR:
		return 1
	default:
		return 0
	}
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
