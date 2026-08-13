package runtime_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	companionruntime "github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/runtime"
)

func TestConsoleAccessCreatesOneTimeShortLivedBrowserSession(t *testing.T) {
	config := testConfig()
	application, err := companionruntime.New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	waitForState(t, application, companionruntime.StateReady)
	defer func() {
		cancel()
		if runErr := <-done; runErr != nil {
			t.Errorf("Run() error = %v", runErr)
		}
	}()
	expires := time.Now().Add(30 * time.Second)
	accessURL, err := application.IssueConsoleAccess(expires)
	if err != nil {
		t.Fatalf("IssueConsoleAccess() error = %v", err)
	}
	parsed, err := url.Parse(accessURL)
	if err != nil {
		t.Fatalf("Parse(access URL) error = %v", err)
	}
	if parsed.Path != companionruntime.ConsoleAccessPath || parsed.RawQuery == "" {
		t.Fatalf("access URL = %q, want one-time access route", accessURL)
	}

	request := httptest.NewRequest(http.MethodGet, accessURL, nil)
	response := httptest.NewRecorder()
	application.ServeConsoleAccess(response, request)
	result := response.Result()
	if result.StatusCode != http.StatusSeeOther || result.Header.Get("Location") != "/" {
		t.Fatalf("first access = %d Location %q, want 303 /", result.StatusCode, result.Header.Get("Location"))
	}
	cookies := result.Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookies = %#v, want one protected session", cookies)
	}

	replay := httptest.NewRecorder()
	application.ServeConsoleAccess(replay, request)
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replayed access = %d, want 401", replay.Code)
	}
	if strings.Contains(accessURL, config.Management.AdminToken) {
		t.Fatal("access URL exposed the long-lived management token")
	}
}
