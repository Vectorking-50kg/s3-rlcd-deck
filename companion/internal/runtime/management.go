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
	"strconv"
	"sync"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairing"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/notices"
	webapp "github.com/Vectorking-50kg/s3-rlcd-deck/companion/web"
)

const (
	managementSessionCookie = "s3deck_session"
	managementLoginMaxBytes = 4 << 10
	managementSessionTTL    = 8 * time.Hour
	maxManagementSessions   = 1024
)

type managementSession struct {
	csrfHash  [sha256.Size]byte
	expiresAt time.Time
}

type managementSessions struct {
	mu      sync.Mutex
	entries map[[sha256.Size]byte]managementSession
	ttl     time.Duration
	maxSize int
}

func newManagementSessions() *managementSessions {
	return &managementSessions{
		entries: make(map[[sha256.Size]byte]managementSession),
		ttl:     managementSessionTTL,
		maxSize: maxManagementSessions,
	}
}

func (application *Runtime) managementRoutes() http.Handler {
	limits := application.config.Management.Limits
	sensitiveRateLimiter := newIPRateLimiter(
		limits.SensitiveRequests,
		limits.SensitiveRateWindow,
	)
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+ConsoleAccessPath, application.ServeConsoleAccess)
	mux.HandleFunc("GET /third-party-licenses.txt", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		response.Header().Set("Cache-Control", "no-store")
		_, _ = response.Write([]byte(notices.ThirdParty()))
	})
	mux.HandleFunc("GET /api/v1/bootstrap", application.handleBootstrap)
	mux.HandleFunc("POST /api/v1/login", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.handleLogin,
	))
	mux.HandleFunc("POST /api/v1/session/refresh", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.requireManagementSession(application.handleSessionRefresh),
	))
	mux.HandleFunc("GET /api/v1/status", application.requireManagementSession(application.handleStatus))
	mux.HandleFunc("GET /api/v1/devices", application.requireManagementSession(application.handleListDevices))
	mux.HandleFunc("GET /api/v1/serial/status", application.requireManagementSession(application.handleSerialStatus))
	mux.HandleFunc("GET /api/v1/serial/download", application.requireManagementSession(application.handleSerialDownload))
	mux.HandleFunc("GET /api/v1/serial/observe", application.handleSerialObserve)
	mux.HandleFunc("GET /api/v1/serial/presets", application.requireManagementSession(application.handleSerialPresets))
	mux.HandleFunc("GET /api/v1/serial/presets/{presetID}", application.requireManagementSession(application.handleSerialPreset))
	mux.HandleFunc("PUT /api/v1/serial/presets", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.requireManagementWrite(application.handleSerialPresetsUpdate),
	))
	mux.HandleFunc("PUT /api/v1/serial/presets/{presetID}", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.requireManagementWrite(application.handleSerialPresetUpdate),
	))
	mux.HandleFunc("DELETE /api/v1/serial/presets/{presetID}", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.requireManagementWrite(application.handleSerialPresetDelete),
	))
	mux.HandleFunc("GET /api/v1/console", application.requireManagementSession(application.handleConsoleView))
	mux.HandleFunc("GET /api/v1/providers", application.requireManagementSession(application.handleProviders))
	mux.HandleFunc("POST /api/v1/providers", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.requireManagementWrite(application.handleProviderCreate),
	))
	mux.HandleFunc("PUT /api/v1/providers/order", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.requireManagementWrite(application.handleProviderOrder),
	))
	mux.HandleFunc("PUT /api/v1/providers/{providerID}", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.requireManagementWrite(application.handleProviderUpdate),
	))
	mux.HandleFunc("DELETE /api/v1/providers/{providerID}", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.requireManagementWrite(application.handleProviderDelete),
	))
	mux.HandleFunc("POST /api/v1/providers/{providerID}/test", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.requireManagementWrite(application.handleProviderTest),
	))
	mux.HandleFunc("GET /api/v1/history", application.requireManagementSession(application.handleHistoryQuery))
	mux.HandleFunc("GET /api/v1/history/export.csv", application.requireManagementSession(application.handleHistoryExport))
	mux.HandleFunc("GET /api/v1/history/settings", application.requireManagementSession(application.handleHistorySettings))
	mux.HandleFunc("PUT /api/v1/history/settings", application.requireManagementWrite(application.handleHistorySettingsUpdate))
	mux.HandleFunc("DELETE /api/v1/history", application.requireManagementWrite(application.handleHistoryClear))
	mux.HandleFunc("POST /api/v1/backups/export", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.requireManagementWrite(application.handleBackupExport),
	))
	mux.HandleFunc("POST /api/v1/backups/preview", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.requireManagementWrite(application.handleBackupPreview),
	))
	mux.HandleFunc("POST /api/v1/backups/import", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.requireManagementWrite(application.handleBackupImport),
	))
	mux.HandleFunc("POST /api/v1/ota/preview", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.requireManagementWrite(application.handleOTAPreview),
	))
	mux.HandleFunc("POST /api/v1/ota/apply", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.requireManagementWrite(application.handleOTAApply),
	))
	mux.HandleFunc("GET /api/v1/ota/status", application.requireManagementSession(application.handleOTAStatus))
	mux.HandleFunc("GET /api/v1/diagnostics", application.requireManagementSession(application.handleDiagnosticsStatus))
	mux.HandleFunc("POST /api/v1/diagnostics/export", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.requireManagementWrite(application.handleDiagnosticsExport),
	))
	mux.HandleFunc("POST /api/v1/logout", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.requireManagementWrite(application.handleLogout),
	))
	mux.HandleFunc("POST /api/v1/pairing/codes", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.requireManagementWrite(application.handleIssuePairingCode),
	))
	mux.HandleFunc("POST /api/v1/pairing-v2/scan", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.requireManagementWrite(application.handlePairingV2Scan),
	))
	mux.HandleFunc("POST /api/v1/pairing-v2/sessions", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.requireManagementWrite(application.handlePairingV2Begin),
	))
	mux.HandleFunc("GET /api/v1/pairing-v2/sessions/{sessionRef}",
		application.requireManagementSession(application.handlePairingV2Status),
	)
	mux.HandleFunc("POST /api/v1/pairing-v2/sessions/{sessionRef}/confirm", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.requireManagementWrite(application.handlePairingV2Confirm),
	))
	mux.HandleFunc("DELETE /api/v1/pairing-v2/sessions/{sessionRef}", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.requireManagementWrite(application.handlePairingV2Cancel),
	))
	mux.HandleFunc("POST /api/v1/devices/{deviceID}/rotate", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.requireManagementWrite(application.handleRotateDeviceToken),
	))
	mux.HandleFunc("DELETE /api/v1/devices/{deviceID}", limitManagementRequests(
		sensitiveRateLimiter,
		limits.SensitiveRateWindow,
		application.requireManagementWrite(application.handleRevokeDevice),
	))
	mux.HandleFunc("/api/", func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "not found", http.StatusNotFound)
	})
	mux.Handle("/", webapp.Handler())
	return secureManagementResponses(mux)
}

func secureManagementResponses(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(response, request)
	})
}

func (application *Runtime) handleIssuePairingCode(response http.ResponseWriter, request *http.Request) {
	advertisedAddress := application.deviceHubAdvertisedAddress(request.Context())
	if advertisedAddress == "" {
		http.Error(response, "Device Hub advertised address unavailable", http.StatusServiceUnavailable)
		return
	}
	issued, err := application.pairing.Issue(request.Context())
	if err != nil {
		http.Error(response, "pairing code unavailable", http.StatusServiceUnavailable)
		return
	}
	writeManagementJSON(response, struct {
		pairing.IssuedCode
		DeviceHubAddress string `json:"device_hub_address"`
	}{IssuedCode: issued, DeviceHubAddress: advertisedAddress})
}

func (application *Runtime) handleRotateDeviceToken(response http.ResponseWriter, request *http.Request) {
	issued, err := application.pairing.IssueRotation(request.Context(), request.PathValue("deviceID"))
	if err != nil {
		if errors.Is(err, pairing.ErrTrustNotFound) || errors.Is(err, pairing.ErrInvalidRequest) {
			http.Error(response, "device not found", http.StatusNotFound)
			return
		}
		http.Error(response, "device token rotation unavailable", http.StatusServiceUnavailable)
		return
	}
	writeManagementJSON(response, issued)
}

func (application *Runtime) handleRevokeDevice(response http.ResponseWriter, request *http.Request) {
	err := application.pairing.Revoke(request.Context(), request.PathValue("deviceID"))
	if err != nil {
		if errors.Is(err, pairing.ErrTrustNotFound) || errors.Is(err, pairing.ErrInvalidRequest) {
			http.Error(response, "device not found", http.StatusNotFound)
			return
		}
		http.Error(response, "device revocation unavailable", http.StatusServiceUnavailable)
		return
	}
	response.WriteHeader(http.StatusNoContent)
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
	if !application.sessions.add(sessionToken, csrfToken, time.Now()) {
		http.Error(response, "session capacity reached", http.StatusServiceUnavailable)
		return
	}
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

func (application *Runtime) handleListDevices(response http.ResponseWriter, request *http.Request) {
	devices, err := application.pairing.ListTrusts(request.Context())
	if err != nil {
		http.Error(response, "paired devices unavailable", http.StatusServiceUnavailable)
		return
	}
	writeManagementJSON(response, struct {
		Devices []pairing.TrustView `json:"devices"`
	}{Devices: devices})
}

func (application *Runtime) handleSessionRefresh(response http.ResponseWriter, request *http.Request) {
	if !application.managementOriginValid(request) {
		http.Error(response, "forbidden", http.StatusForbidden)
		return
	}
	cookie, err := request.Cookie(managementSessionCookie)
	if err != nil || cookie.Value == "" {
		http.Error(response, "unauthorized", http.StatusUnauthorized)
		return
	}
	csrfToken, err := randomWebToken()
	if err != nil {
		http.Error(response, "session unavailable", http.StatusInternalServerError)
		return
	}
	if !application.sessions.rotateCSRF(cookie.Value, csrfToken, time.Now()) {
		http.Error(response, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeManagementJSON(response, struct {
		CSRFToken string `json:"csrf_token"`
	}{CSRFToken: csrfToken})
}

func (application *Runtime) handleLogout(response http.ResponseWriter, request *http.Request) {
	cookie, _ := request.Cookie(managementSessionCookie)
	application.sessions.revoke(cookie.Value)
	if application.ota != nil {
		application.ota.RevokePreviews()
	}
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
	return application.sessions.lookupKey(key, time.Now())
}

func (sessions *managementSessions) add(sessionToken string, csrfToken string, now time.Time) bool {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	sessions.pruneExpired(now)
	if len(sessions.entries) >= sessions.maxSize {
		return false
	}
	sessions.entries[sha256.Sum256([]byte(sessionToken))] = managementSession{
		csrfHash:  sha256.Sum256([]byte(csrfToken)),
		expiresAt: now.Add(sessions.ttl),
	}
	return true
}

func (sessions *managementSessions) lookupKey(
	key [sha256.Size]byte,
	now time.Time,
) (managementSession, bool) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	sessions.pruneExpired(now)
	session, found := sessions.entries[key]
	return session, found
}

func (sessions *managementSessions) revoke(sessionToken string) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	delete(sessions.entries, sha256.Sum256([]byte(sessionToken)))
}

func (sessions *managementSessions) rotateCSRF(
	sessionToken string,
	csrfToken string,
	now time.Time,
) bool {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	sessions.pruneExpired(now)
	key := sha256.Sum256([]byte(sessionToken))
	session, found := sessions.entries[key]
	if !found {
		return false
	}
	session.csrfHash = sha256.Sum256([]byte(csrfToken))
	sessions.entries[key] = session
	return true
}

func (sessions *managementSessions) pruneExpired(now time.Time) {
	for key, session := range sessions.entries {
		if !now.Before(session.expiresAt) {
			delete(sessions.entries, key)
		}
	}
}

func limitManagementRequests(
	limiter *ipRateLimiter,
	window time.Duration,
	next http.HandlerFunc,
) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if !limiter.allow(request.RemoteAddr, time.Now()) {
			response.Header().Set("Retry-After", strconv.Itoa(max(1, int(window.Seconds()))))
			http.Error(response, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next(response, request)
	}
}

func (application *Runtime) managementOriginValid(request *http.Request) bool {
	expected := application.config.Management.AllowedOrigin
	if expected == "" {
		expected = "http://" + application.Status().ManagementAddress
	}
	return request.Header.Get("Origin") == expected
}

func decodeManagementJSON(response http.ResponseWriter, request *http.Request, destination any) error {
	return decodeManagementJSONLimit(
		response, request, destination, managementLoginMaxBytes,
	)
}

func decodeManagementJSONLimit(
	response http.ResponseWriter,
	request *http.Request,
	destination any,
	maximumBytes int,
) error {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("content type must be application/json")
	}
	request.Body = http.MaxBytesReader(response, request.Body, int64(maximumBytes))
	message, err := io.ReadAll(request.Body)
	if err != nil {
		return err
	}
	defer clear(message)
	return protocol.DecodeStrictDocumentLimit(message, maximumBytes, destination)
}

func writeManagementJSON(response http.ResponseWriter, document any) {
	writeManagementJSONStatus(response, http.StatusOK, document)
}

func writeManagementJSONStatus(response http.ResponseWriter, status int, document any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(document)
}

func randomWebToken() (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(random), nil
}
