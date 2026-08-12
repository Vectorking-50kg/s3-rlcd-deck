package runtime

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
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
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/device/health", application.handleDeviceHealth)
	return newDeviceHubGateway(application.config.DeviceHub, mux)
}

func newDeviceHubGateway(config DeviceHubConfig, next http.Handler) http.Handler {
	concurrency := make(chan struct{}, config.Limits.MaxConcurrent)
	rateLimiter := newIPRateLimiter(
		config.Limits.RateLimitRequests,
		config.Limits.RateLimitWindow,
	)
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
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
		if !deviceTokenValid(request, config.BootstrapToken) {
			response.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(response, "unauthorized", http.StatusUnauthorized)
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

func deviceTokenValid(request *http.Request, expected string) bool {
	scheme, token, found := strings.Cut(request.Header.Get("Authorization"), " ")
	return found && scheme == "Bearer" && constantTimeTokenEqual(token, expected)
}
