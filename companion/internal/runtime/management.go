package runtime

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	webapp "github.com/Vectorking-50kg/s3-rlcd-deck/companion/web"
)

const (
	managementSessionCookie = "s3deck_session"
	managementLoginMaxBytes = 4 << 10
	managementSessionTTL    = 8 * time.Hour
)

type managementSession struct {
	csrfHash  [sha256.Size]byte
	expiresAt time.Time
}

type managementSessions struct {
	entries map[[sha256.Size]byte]managementSession
}

func newManagementSessions() *managementSessions {
	return &managementSessions{entries: make(map[[sha256.Size]byte]managementSession)}
}

func (application *Runtime) managementRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/bootstrap", application.handleBootstrap)
	mux.HandleFunc("POST /api/v1/login", application.handleLogin)
	mux.HandleFunc("GET /api/v1/status", application.requireManagementSession(application.handleStatus))
	mux.HandleFunc("POST /api/v1/logout", application.requireManagementWrite(application.handleLogout))
	mux.HandleFunc("/api/", func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "not found", http.StatusNotFound)
	})
	mux.Handle("/", webapp.Handler())
	return mux
}

func (application *Runtime) handleBootstrap(response http.ResponseWriter, _ *http.Request) {
	writeManagementJSON(response, struct {
		Version              string `json:"version"`
		LoginRequired        bool   `json:"login_required"`
		LANManagementEnabled bool   `json:"lan_management_enabled"`
	}{
		Version:              application.config.Version,
		LoginRequired:        true,
		LANManagementEnabled: application.config.Management.AllowLAN,
	})
}

func (application *Runtime) handleLogin(response http.ResponseWriter, request *http.Request) {
	if !application.managementOriginValid(request) {
		http.Error(response, "forbidden", http.StatusForbidden)
		return
	}
	var credentials struct {
		Token string `json:"token"`
	}
	if err := decodeManagementJSON(response, request, &credentials); err != nil {
		http.Error(response, "malformed login request", http.StatusBadRequest)
		return
	}
	if !constantTimeTokenEqual(credentials.Token, application.config.Management.AdminToken) {
		http.Error(response, "unauthorized", http.StatusUnauthorized)
		return
	}
	sessionToken, err := randomWebToken()
	if err != nil {
		http.Error(response, "session unavailable", http.StatusInternalServerError)
		return
	}
	csrfToken, err := randomWebToken()
	if err != nil {
		http.Error(response, "session unavailable", http.StatusInternalServerError)
		return
	}
	application.sessionsMu.Lock()
	application.sessions.entries[sha256.Sum256([]byte(sessionToken))] = managementSession{
		csrfHash:  sha256.Sum256([]byte(csrfToken)),
		expiresAt: time.Now().Add(managementSessionTTL),
	}
	application.sessionsMu.Unlock()
	http.SetCookie(response, &http.Cookie{
		Name:     managementSessionCookie,
		Value:    sessionToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(managementSessionTTL.Seconds()),
	})
	writeManagementJSON(response, struct {
		CSRFToken string `json:"csrf_token"`
	}{CSRFToken: csrfToken})
}

func (application *Runtime) handleStatus(response http.ResponseWriter, _ *http.Request) {
	writeManagementJSON(response, application.Status())
}

func (application *Runtime) handleLogout(response http.ResponseWriter, request *http.Request) {
	cookie, _ := request.Cookie(managementSessionCookie)
	application.sessionsMu.Lock()
	delete(application.sessions.entries, sha256.Sum256([]byte(cookie.Value)))
	application.sessionsMu.Unlock()
	http.SetCookie(response, &http.Cookie{
		Name:     managementSessionCookie,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	response.WriteHeader(http.StatusNoContent)
}

func (application *Runtime) requireManagementSession(next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if _, valid := application.managementSession(request); !valid {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(response, request)
	}
}

func (application *Runtime) requireManagementWrite(next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		session, valid := application.managementSession(request)
		if !valid {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !application.managementOriginValid(request) ||
			!constantTimeHashEqual(session.csrfHash, sha256.Sum256([]byte(request.Header.Get("X-CSRF-Token")))) {
			http.Error(response, "forbidden", http.StatusForbidden)
			return
		}
		next(response, request)
	}
}

func (application *Runtime) managementSession(request *http.Request) (managementSession, bool) {
	cookie, err := request.Cookie(managementSessionCookie)
	if err != nil || cookie.Value == "" {
		return managementSession{}, false
	}
	key := sha256.Sum256([]byte(cookie.Value))
	application.sessionsMu.Lock()
	defer application.sessionsMu.Unlock()
	session, found := application.sessions.entries[key]
	if !found {
		return managementSession{}, false
	}
	if time.Now().After(session.expiresAt) {
		delete(application.sessions.entries, key)
		return managementSession{}, false
	}
	return session, true
}

func (application *Runtime) managementOriginValid(request *http.Request) bool {
	expected := application.config.Management.AllowedOrigin
	if expected == "" {
		expected = "http://" + application.Status().ManagementAddress
	}
	return request.Header.Get("Origin") == expected
}

func decodeManagementJSON(response http.ResponseWriter, request *http.Request, destination any) error {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("content type must be application/json")
	}
	request.Body = http.MaxBytesReader(response, request.Body, managementLoginMaxBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(destination); err != nil {
		return err
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON document")
	}
	return nil
}

func writeManagementJSON(response http.ResponseWriter, document any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(response).Encode(document)
}

func randomWebToken() (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(random), nil
}
