package runtime

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairing"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
)

type rateWindow struct {
	started time.Time
	count   int
}

type ipRateLimiter struct {
	mu        sync.Mutex
	windows   map[string]rateWindow
	limit     int
	duration  time.Duration
	lastSweep time.Time
}

const maxTrackedRateLimitIPs = 4096

func newIPRateLimiter(limit int, duration time.Duration) *ipRateLimiter {
	return &ipRateLimiter{
		windows:  make(map[string]rateWindow),
		limit:    limit,
		duration: duration,
	}
}

func (limiter *ipRateLimiter) allow(address string, now time.Time) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if limiter.lastSweep.IsZero() || now.Sub(limiter.lastSweep) >= limiter.duration {
		for trackedHost, trackedWindow := range limiter.windows {
			if now.Sub(trackedWindow.started) >= limiter.duration {
				delete(limiter.windows, trackedHost)
			}
		}
		limiter.lastSweep = now
	}
	window := limiter.windows[host]
	if window.started.IsZero() || now.Sub(window.started) >= limiter.duration {
		if _, tracked := limiter.windows[host]; !tracked && len(limiter.windows) >= maxTrackedRateLimitIPs {
			return false
		}
		limiter.windows[host] = rateWindow{started: now, count: 1}
		return true
	}
	if window.count >= limiter.limit {
		return false
	}
	window.count++
	limiter.windows[host] = window
	return true
}

func (application *Runtime) deviceHubRoutes() http.Handler {
	limits := application.config.DeviceHub.Limits
	pairingLimiter := newIPRateLimiter(limits.PairingAttempts, limits.PairingRateWindow)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/device/health", application.handleDeviceHealth)
	mux.Handle("GET /api/v1/device/link", application.deviceLink)
	mux.HandleFunc("POST /api/v1/pairing/redeem", func(response http.ResponseWriter, request *http.Request) {
		if !pairingLimiter.allow(request.RemoteAddr, time.Now()) {
			response.Header().Set("Retry-After", strconv.Itoa(max(1, int(limits.PairingRateWindow.Seconds()))))
			http.Error(response, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		application.handlePairingRedeem(response, request)
	})
	return newDeviceHubGateway(application.config.DeviceHub, mux)
}

func newDeviceHubGateway(config DeviceHubConfig, next http.Handler) http.Handler {
	concurrency := make(chan struct{}, config.Limits.MaxConcurrent)
	rateLimiter := newIPRateLimiter(
		config.Limits.RateLimitRequests,
		config.Limits.RateLimitWindow,
	)
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		select {
		case concurrency <- struct{}{}:
			defer func() { <-concurrency }()
		default:
			response.Header().Set("Retry-After", "1")
			http.Error(response, "Device Hub is busy", http.StatusServiceUnavailable)
			return
		}
		if !rateLimiter.allow(request.RemoteAddr, time.Now()) {
			retryAfter := max(1, int(config.Limits.RateLimitWindow.Seconds()))
			response.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			http.Error(response, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		if request.ContentLength > config.Limits.MaxBodyBytes {
			http.Error(response, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		request.Body = http.MaxBytesReader(response, request.Body, config.Limits.MaxBodyBytes)
		next.ServeHTTP(response, request)
	})
}

func (application *Runtime) handleDeviceHealth(response http.ResponseWriter, request *http.Request) {
	deviceID := request.Header.Get("X-Device-ID")
	deviceIdentity := request.Header.Get("X-Device-Identity")
	protocolVersion, protocolErr := strconv.Atoi(request.Header.Get("X-Protocol-Version"))
	token, present := bearerToken(request)
	valid := false
	var verifyErr error
	if present && protocolErr == nil {
		valid, verifyErr = application.pairing.Verify(request.Context(), pairing.Authentication{
			DeviceID:        deviceID,
			Token:           token,
			DeviceIdentity:  deviceIdentity,
			ProtocolVersion: protocolVersion,
		})
	}
	if verifyErr != nil {
		http.Error(response, "device trust unavailable", http.StatusServiceUnavailable)
		return
	}
	if !valid {
		response.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(response, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(response, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(response, "cannot read request", http.StatusBadRequest)
		return
	}
	if len(body) != 0 {
		http.Error(response, "health request body must be empty", http.StatusBadRequest)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(response).Encode(struct {
		Status string `json:"status"`
	}{Status: "ok"})
}

func (application *Runtime) handlePairingRedeem(response http.ResponseWriter, request *http.Request) {
	var redeemRequest pairing.RedeemRequest
	if err := decodeDeviceJSON(request, &redeemRequest); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(response, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(response, "malformed pairing request", http.StatusBadRequest)
		return
	}
	credential, err := application.pairing.Redeem(request.Context(), redeemRequest)
	if err != nil {
		switch {
		case errors.Is(err, pairing.ErrUnsupportedProtocol):
			http.Error(response, "unsupported protocol version", http.StatusUpgradeRequired)
		case errors.Is(err, pairing.ErrInvalidRequest):
			http.Error(response, "malformed pairing request", http.StatusBadRequest)
		case errors.Is(err, pairing.ErrCodeUnavailable):
			http.Error(response, "pairing rejected", http.StatusUnauthorized)
		default:
			http.Error(response, "pairing unavailable", http.StatusServiceUnavailable)
		}
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(response).Encode(credential)
}

func decodeDeviceJSON(request *http.Request, destination any) error {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("content type must be application/json")
	}
	message, err := io.ReadAll(request.Body)
	if err != nil {
		return err
	}
	return protocol.DecodeStrictDocument(message, destination)
}

func bearerToken(request *http.Request) (string, bool) {
	scheme, token, found := strings.Cut(request.Header.Get("Authorization"), " ")
	return token, found && scheme == "Bearer" && token != ""
}
