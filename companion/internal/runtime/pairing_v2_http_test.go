package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairingv2"
)

type pairingV2HTTPStub struct {
	mutex       sync.Mutex
	confirmCode string
	state       pairingv2.SessionState
}

func (*pairingV2HTTPStub) Scan(context.Context) ([]pairingv2.Candidate, error) {
	return []pairingv2.Candidate{{
		Reference: "opaque-candidate", Label: "S3 RLCD Deck · A1B2",
		ExpiresAt: time.Date(2026, 8, 18, 12, 0, 10, 0, time.UTC),
	}}, nil
}

func (stub *pairingV2HTTPStub) Begin(reference string) (pairingv2.SessionView, error) {
	if reference != "opaque-candidate" {
		return pairingv2.SessionView{}, pairingv2.ErrCandidateNotFound
	}
	stub.mutex.Lock()
	stub.state = pairingv2.SessionAwaitingCode
	stub.mutex.Unlock()
	return stub.view(), nil
}

func (stub *pairingV2HTTPStub) StartConfirm(reference string, code string) (pairingv2.SessionView, error) {
	if reference != "opaque-session" {
		return pairingv2.SessionView{}, pairingv2.ErrSessionNotFound
	}
	stub.mutex.Lock()
	stub.confirmCode = code
	stub.state = pairingv2.SessionAuthenticating
	stub.mutex.Unlock()
	return stub.view(), nil
}

func (stub *pairingV2HTTPStub) Status(reference string) (pairingv2.SessionView, error) {
	if reference != "opaque-session" {
		return pairingv2.SessionView{}, pairingv2.ErrSessionNotFound
	}
	return stub.view(), nil
}

func (stub *pairingV2HTTPStub) Cancel(reference string) (pairingv2.SessionView, error) {
	if reference != "opaque-session" {
		return pairingv2.SessionView{}, pairingv2.ErrSessionNotFound
	}
	stub.mutex.Lock()
	stub.state = pairingv2.SessionCancelled
	stub.mutex.Unlock()
	return stub.view(), nil
}

func (*pairingV2HTTPStub) ObserveProvisionalHeartbeat(string, string) error {
	return errors.New("not used")
}

func (*pairingV2HTTPStub) Close(context.Context) error { return nil }

func (stub *pairingV2HTTPStub) view() pairingv2.SessionView {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	return pairingv2.SessionView{
		Reference: "opaque-session", State: stub.state,
		ExpiresAt: time.Date(2026, 8, 18, 12, 2, 0, 0, time.UTC), ErrorCode: "none",
	}
}

func TestPairingV2ManagementFlowUsesOnlyOpaqueReferences(t *testing.T) {
	application, err := New(Config{
		Version: "0.4.0-test",
		Management: ManagementConfig{
			Address: "127.0.0.1:7777", AdminToken: strings.Repeat("m", 32),
		},
		DeviceHub: DeviceHubConfig{Address: "127.0.0.1:7780"},
		Pairing:   testPairingService(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer application.deviceLink.Close()
	defer application.serialHub.Close()
	stub := &pairingV2HTTPStub{}
	application.pairingV2 = stub
	handler := application.managementRoutes()

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/api/v1/pairing-v2/scan", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized scan = %d", unauthorized.Code)
	}

	const sessionToken = "pairing-v2-session"
	const csrfToken = "pairing-v2-csrf"
	if !application.sessions.add(sessionToken, csrfToken, time.Now()) {
		t.Fatal("add management session")
	}
	request := pairingV2WriteRequest(http.MethodPost, "/api/v1/pairing-v2/scan", nil, sessionToken, csrfToken)
	scan := httptest.NewRecorder()
	handler.ServeHTTP(scan, request)
	if scan.Code != http.StatusOK || bytes.Contains(scan.Body.Bytes(), []byte("192.168.")) {
		t.Fatalf("scan = %d %q", scan.Code, scan.Body.String())
	}

	beginBody := []byte(`{"candidate_ref":"opaque-candidate"}`)
	begin := httptest.NewRecorder()
	handler.ServeHTTP(begin, pairingV2WriteRequest(
		http.MethodPost, "/api/v1/pairing-v2/sessions", beginBody, sessionToken, csrfToken,
	))
	if begin.Code != http.StatusCreated || bytes.Contains(begin.Body.Bytes(), []byte("candidate")) {
		t.Fatalf("begin = %d %q", begin.Code, begin.Body.String())
	}

	confirm := httptest.NewRecorder()
	handler.ServeHTTP(confirm, pairingV2WriteRequest(
		http.MethodPost, "/api/v1/pairing-v2/sessions/opaque-session/confirm",
		[]byte(`{"code":"123456"}`), sessionToken, csrfToken,
	))
	if confirm.Code != http.StatusAccepted {
		t.Fatalf("confirm = %d %q", confirm.Code, confirm.Body.String())
	}
	stub.mutex.Lock()
	confirmCode := stub.confirmCode
	stub.mutex.Unlock()
	if confirmCode != "123456" {
		t.Fatal("confirmation code did not reach the Coordinator")
	}

	statusRequest := httptest.NewRequest(http.MethodGet, "/api/v1/pairing-v2/sessions/opaque-session", nil)
	statusRequest.AddCookie(&http.Cookie{Name: managementSessionCookie, Value: sessionToken})
	status := httptest.NewRecorder()
	handler.ServeHTTP(status, statusRequest)
	if status.Code != http.StatusOK {
		t.Fatalf("status = %d %q", status.Code, status.Body.String())
	}
	var view map[string]any
	if err = json.Unmarshal(status.Body.Bytes(), &view); err != nil || len(view) != 4 || view["session_ref"] != "opaque-session" {
		t.Fatalf("browser view = %#v, %v", view, err)
	}
	for _, forbidden := range []string{"address", "route", "token", "certificate", "nonce", "device_id", "transaction"} {
		if strings.Contains(status.Body.String(), forbidden) {
			t.Fatalf("browser view leaked backend field %q: %s", forbidden, status.Body.String())
		}
	}
}

func TestPairingV2ManagementRejectsStaleCandidateAndUnknownFields(t *testing.T) {
	application := &Runtime{pairingV2: &pairingV2HTTPStub{}, sessions: newManagementSessions()}
	badBody := []byte(`{"candidate_ref":"opaque-candidate","route":"192.168.1.9"}`)
	response := httptest.NewRecorder()
	application.handlePairingV2Begin(response, pairingV2WriteRequest(
		http.MethodPost, "/api/v1/pairing-v2/sessions", badBody, "", "",
	))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d %q", response.Code, response.Body.String())
	}

	missing := httptest.NewRecorder()
	application.handlePairingV2Begin(missing, pairingV2WriteRequest(
		http.MethodPost, "/api/v1/pairing-v2/sessions",
		[]byte(`{"candidate_ref":"missing"}`), "", "",
	))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing candidate = %d %q", missing.Code, missing.Body.String())
	}
}

func pairingV2WriteRequest(method, target string, body []byte, session, csrf string) *http.Request {
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	request.Host = "127.0.0.1:7777"
	request.Header.Set("Origin", "http://127.0.0.1:7777")
	request.Header.Set("X-CSRF-Token", csrf)
	request.Header.Set("Content-Type", "application/json")
	if session != "" {
		request.AddCookie(&http.Cookie{Name: managementSessionCookie, Value: session})
	}
	return request
}
