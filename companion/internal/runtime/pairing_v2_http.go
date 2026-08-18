package runtime

import (
	"errors"
	"net/http"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairingv2"
)

const pairingV2RequestMaxBytes = 1024

func (application *Runtime) handlePairingV2Scan(response http.ResponseWriter, request *http.Request) {
	if application.pairingV2 == nil {
		http.Error(response, "Pairing v2 unavailable", http.StatusServiceUnavailable)
		return
	}
	candidates, err := application.pairingV2.Scan(request.Context())
	if err != nil {
		http.Error(response, "Pairing v2 scan unavailable", http.StatusServiceUnavailable)
		return
	}
	writeManagementJSON(response, struct {
		Candidates []pairingv2.Candidate `json:"candidates"`
	}{Candidates: candidates})
}

func (application *Runtime) handlePairingV2Begin(response http.ResponseWriter, request *http.Request) {
	if application.pairingV2 == nil {
		http.Error(response, "Pairing v2 unavailable", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		CandidateReference string `json:"candidate_ref"`
	}
	if decodeManagementJSONLimit(response, request, &body, pairingV2RequestMaxBytes) != nil ||
		body.CandidateReference == "" {
		http.Error(response, "malformed Pairing v2 request", http.StatusBadRequest)
		return
	}
	view, err := application.pairingV2.Begin(body.CandidateReference)
	if err != nil {
		if errors.Is(err, pairingv2.ErrCandidateNotFound) ||
			errors.Is(err, pairingv2.ErrCandidateExpired) {
			http.Error(response, "Pairing v2 candidate unavailable", http.StatusNotFound)
			return
		}
		http.Error(response, "Pairing v2 session unavailable", http.StatusServiceUnavailable)
		return
	}
	writeManagementJSONStatus(response, http.StatusCreated, view)
}

func (application *Runtime) handlePairingV2Status(response http.ResponseWriter, request *http.Request) {
	if application.pairingV2 == nil {
		http.Error(response, "Pairing v2 unavailable", http.StatusServiceUnavailable)
		return
	}
	view, err := application.pairingV2.Status(request.PathValue("sessionRef"))
	if err != nil {
		writePairingV2SessionError(response, err)
		return
	}
	writeManagementJSON(response, view)
}

func (application *Runtime) handlePairingV2Confirm(response http.ResponseWriter, request *http.Request) {
	if application.pairingV2 == nil {
		http.Error(response, "Pairing v2 unavailable", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if decodeManagementJSONLimit(response, request, &body, pairingV2RequestMaxBytes) != nil {
		http.Error(response, "malformed Pairing v2 request", http.StatusBadRequest)
		return
	}
	view, err := application.pairingV2.StartConfirm(request.PathValue("sessionRef"), body.Code)
	if err != nil {
		if errors.Is(err, pairingv2.ErrPairingFailed) {
			http.Error(response, "malformed Pairing v2 request", http.StatusBadRequest)
			return
		}
		writePairingV2SessionError(response, err)
		return
	}
	writeManagementJSONStatus(response, http.StatusAccepted, view)
}

func (application *Runtime) handlePairingV2Cancel(response http.ResponseWriter, request *http.Request) {
	if application.pairingV2 == nil {
		http.Error(response, "Pairing v2 unavailable", http.StatusServiceUnavailable)
		return
	}
	view, err := application.pairingV2.Cancel(request.PathValue("sessionRef"))
	if err != nil {
		writePairingV2SessionError(response, err)
		return
	}
	writeManagementJSON(response, view)
}

func writePairingV2SessionError(response http.ResponseWriter, err error) {
	if errors.Is(err, pairingv2.ErrSessionNotFound) {
		http.Error(response, "Pairing v2 session unavailable", http.StatusNotFound)
		return
	}
	if errors.Is(err, pairingv2.ErrSessionState) {
		http.Error(response, "Pairing v2 session state conflict", http.StatusConflict)
		return
	}
	http.Error(response, "Pairing v2 session unavailable", http.StatusServiceUnavailable)
}
