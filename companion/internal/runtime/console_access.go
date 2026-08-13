package runtime

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	ConsoleAccessPath        = "/api/v1/desktop/access"
	maxConsoleAccessGrants   = 8
	maxConsoleAccessLifespan = time.Minute
)

type consoleAccessGrant struct {
	hash      [sha256.Size]byte
	expiresAt time.Time
}

type consoleAccessResult int

const (
	consoleAccessInvalid consoleAccessResult = iota
	consoleAccessConsumed
	consoleAccessSessionUnavailable
)

type consoleAccessGrants struct {
	mu     sync.Mutex
	grants []consoleAccessGrant
}

func (application *Runtime) IssueConsoleAccess(expiresAt time.Time) (string, error) {
	now := time.Now()
	if !expiresAt.After(now) || expiresAt.After(now.Add(maxConsoleAccessLifespan)) {
		return "", errors.New("console access expiry is outside the permitted window")
	}
	status := application.Status()
	if status.State != StateReady {
		return "", errors.New("Companion runtime is not ready")
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", errors.New("console access random source is unavailable")
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	application.consoleAccess.add(sha256.Sum256([]byte(token)), expiresAt, now)
	return "http://" + status.ManagementAddress + ConsoleAccessPath + "?token=" + url.QueryEscape(token), nil
}

func (application *Runtime) ServeConsoleAccess(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Referrer-Policy", "no-referrer")
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", http.MethodGet)
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	values, ok := request.URL.Query()["token"]
	if !ok || len(values) != 1 || values[0] == "" {
		http.Error(response, "unauthorized", http.StatusUnauthorized)
		return
	}
	var sessionToken string
	result := application.consoleAccess.consume(
		sha256.Sum256([]byte(values[0])),
		time.Now(),
		func() bool {
			generatedSession, err := randomWebToken()
			if err != nil {
				return false
			}
			csrfToken, err := randomWebToken()
			if err != nil || !application.sessions.add(generatedSession, csrfToken, time.Now()) {
				return false
			}
			sessionToken = generatedSession
			return true
		},
	)
	if result == consoleAccessInvalid {
		http.Error(response, "unauthorized", http.StatusUnauthorized)
		return
	}
	if result == consoleAccessSessionUnavailable {
		http.Error(response, "session unavailable", http.StatusServiceUnavailable)
		return
	}
	http.SetCookie(response, &http.Cookie{
		Name: managementSessionCookie, Value: sessionToken, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
		MaxAge: int(managementSessionTTL.Seconds()),
	})
	http.Redirect(response, request, "/", http.StatusSeeOther)
}

func (grants *consoleAccessGrants) add(hash [sha256.Size]byte, expiresAt time.Time, now time.Time) {
	grants.mu.Lock()
	defer grants.mu.Unlock()
	grants.prune(now)
	if len(grants.grants) >= maxConsoleAccessGrants {
		copy(grants.grants, grants.grants[1:])
		grants.grants = grants.grants[:len(grants.grants)-1]
	}
	grants.grants = append(grants.grants, consoleAccessGrant{hash: hash, expiresAt: expiresAt})
}

func (grants *consoleAccessGrants) consume(
	hash [sha256.Size]byte,
	now time.Time,
	createSession func() bool,
) consoleAccessResult {
	grants.mu.Lock()
	defer grants.mu.Unlock()
	grants.prune(now)
	for index := range grants.grants {
		if constantTimeHashEqual(grants.grants[index].hash, hash) {
			if !createSession() {
				return consoleAccessSessionUnavailable
			}
			grants.grants = append(grants.grants[:index], grants.grants[index+1:]...)
			return consoleAccessConsumed
		}
	}
	return consoleAccessInvalid
}

func (grants *consoleAccessGrants) prune(now time.Time) {
	kept := grants.grants[:0]
	for _, grant := range grants.grants {
		if now.Before(grant.expiresAt) {
			kept = append(kept, grant)
		}
	}
	grants.grants = kept
}
