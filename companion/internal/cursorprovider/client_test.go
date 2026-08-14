package cursorprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
)

var cursorTestNow = time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)

type sequenceTokenSource struct {
	mutex  sync.Mutex
	tokens [][]byte
	err    error
	calls  int
}

func (source *sequenceTokenSource) AccessToken(context.Context) ([]byte, error) {
	source.mutex.Lock()
	defer source.mutex.Unlock()
	source.calls++
	if source.err != nil {
		return nil, source.err
	}
	index := source.calls - 1
	if index >= len(source.tokens) {
		index = len(source.tokens) - 1
	}
	return append([]byte(nil), source.tokens[index]...), nil
}

func validCursorUsageDocument() string {
	return `{
		"billingCycleStart":"1785542400000",
		"billingCycleEnd":"1788220800000",
		"planUsage":{
			"totalSpend":2500,
			"includedSpend":2500,
			"bonusSpend":0,
			"remaining":7500,
			"limit":10000,
			"totalPercentUsed":25
		},
		"spendLimitUsage":{
			"totalSpend":200,
			"individualLimit":1000,
			"individualUsed":200,
			"individualRemaining":800,
			"limitType":"individual"
		},
		"displayThreshold":50,
		"enabled":true,
		"displayMessage":"",
		"autoBucketModels":[]
	}`
}

func newCursorTestCollector(
	t *testing.T,
	tokenSource AccessTokenSource,
	handler http.HandlerFunc,
) (*Collector, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	collector, err := New(Config{
		TokenSource:           tokenSource,
		HTTPClient:            server.Client(),
		AdapterVersion:        AdapterVersion,
		ResponseSchemaVersion: ResponseSchemaVersion,
		RequestTimeout:        time.Second,
		RefreshInterval:       time.Hour,
		RetryInterval:         time.Hour,
		Now:                   func() time.Time { return cursorTestNow },
		endpointURL:           server.URL + "/aiserver.v1.DashboardService/GetCurrentPeriodUsage",
	})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return collector, server
}

func writeCursorJSON(response http.ResponseWriter, status int, document string) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_, _ = io.WriteString(response, document)
}

func TestCollectsCurrentCursorUsageIntoExperimentalSnapshotProvider(t *testing.T) {
	token := "cursor-access-token-a"
	source := &sequenceTokenSource{tokens: [][]byte{[]byte(token)}}
	collector, server := newCursorTestCollector(t, source, func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		if request.Method != http.MethodPost ||
			request.URL.Path != "/aiserver.v1.DashboardService/GetCurrentPeriodUsage" ||
			request.Header.Get("Authorization") != "Bearer "+token ||
			request.Header.Get("Connect-Protocol-Version") != "1" ||
			request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected private endpoint request: %s %s headers=%v", request.Method, request.URL, request.Header)
		}
		body, _ := io.ReadAll(request.Body)
		if string(body) != "{}" {
			t.Errorf("request body = %q", body)
		}
		writeCursorJSON(response, http.StatusOK, validCursorUsageDocument())
	})
	defer server.Close()

	provider, err := collectProvider(context.Background(), collector.config)
	if err != nil {
		t.Fatal(err)
	}
	if provider.ID != "cursor" || provider.DisplayName != "Cursor" ||
		provider.Status != aisnapshot.ProviderOK ||
		provider.Source != aisnapshot.ProviderSourceCursorLocal ||
		provider.Confidence != aisnapshot.ConfidenceInferred || !provider.Experimental ||
		provider.Error != nil || len(provider.Windows) != 2 {
		t.Fatalf("provider = %+v", provider)
	}
	if got := *provider.Windows[0].UsedBasisPoints; got != 2500 {
		t.Fatalf("billing used = %d", got)
	}
	if got := *provider.Windows[1].UsedBasisPoints; got != 2000 {
		t.Fatalf("spend-limit used = %d", got)
	}
	generatedAt := cursorTestNow.Format(time.RFC3339Nano)
	if _, err = aisnapshot.Encode(aisnapshot.Snapshot{
		Type:            "snapshot.ai",
		ProtocolVersion: protocol.CurrentVersion,
		SchemaVersion:   aisnapshot.SchemaVersion{Major: 1, Minor: 0},
		GeneratedAt:     generatedAt,
		ProviderOrder:   []string{"cursor"},
		Providers:       []aisnapshot.Provider{provider},
		Sessions:        []aisnapshot.Session{},
		NextRefresh:     300,
	}); err != nil {
		t.Fatalf("normalized provider violates AI Snapshot contract: %v", err)
	}
}

func TestReadsFreshAccessTokenForEveryPrivateRequest(t *testing.T) {
	source := &sequenceTokenSource{tokens: [][]byte{
		[]byte("cursor-access-token-first"),
		[]byte("cursor-access-token-second"),
	}}
	var authorizations []string
	collector, server := newCursorTestCollector(t, source, func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		authorizations = append(authorizations, request.Header.Get("Authorization"))
		writeCursorJSON(response, http.StatusOK, validCursorUsageDocument())
	})
	defer server.Close()
	for range 2 {
		if _, err := collectProvider(context.Background(), collector.config); err != nil {
			t.Fatal(err)
		}
	}
	if source.calls != 2 || fmt.Sprint(authorizations) !=
		"[Bearer cursor-access-token-first Bearer cursor-access-token-second]" {
		t.Fatalf("calls=%d authorization=%v", source.calls, authorizations)
	}
}

func TestPrivateSchemaChangesAndOversizedBodiesFailClosed(t *testing.T) {
	tests := []struct {
		name     string
		document string
		limit    int64
	}{
		{name: "unknown field", document: strings.Replace(validCursorUsageDocument(), `"enabled":true`, `"enabled":true,"futureField":1`, 1)},
		{name: "null scalar", document: strings.Replace(validCursorUsageDocument(), `"displayMessage":""`, `"displayMessage":null`, 1)},
		{name: "unpaired surrogate", document: strings.Replace(validCursorUsageDocument(), `"displayMessage":""`, `"displayMessage":"\ud800"`, 1)},
		{name: "wrong int64 encoding", document: strings.Replace(validCursorUsageDocument(), `"billingCycleStart":"1785542400000"`, `"billingCycleStart":1785542400000`, 1)},
		{name: "oversized", document: validCursorUsageDocument() + strings.Repeat(" ", 2048), limit: 512},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			collector, server := newCursorTestCollector(t, &sequenceTokenSource{tokens: [][]byte{[]byte("token")}}, func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				writeCursorJSON(response, http.StatusOK, test.document)
			})
			defer server.Close()
			if test.limit != 0 {
				collector.config.maximumResponse = test.limit
			}
			_, err := collectProvider(context.Background(), collector.config)
			if !errors.Is(err, ErrSchemaChanged) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestPrivateEndpointErrorsAreStableAndCredentialFree(t *testing.T) {
	secret := "cursor-private-secret-never-log"
	tests := []struct {
		name      string
		status    int
		sourceErr error
		want      error
		content   string
	}{
		{name: "authentication", status: http.StatusUnauthorized, want: ErrNotLoggedIn},
		{name: "permission", status: http.StatusForbidden, want: ErrPermission},
		{name: "server", status: http.StatusServiceUnavailable, want: ErrUnavailable},
		{name: "source", sourceErr: fmt.Errorf("source failed with %s", secret), want: ErrUnavailable},
		{name: "schema", status: http.StatusOK, content: `{"leak":"` + secret + `"}`, want: ErrSchemaChanged},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &sequenceTokenSource{tokens: [][]byte{[]byte(secret)}, err: test.sourceErr}
			collector, server := newCursorTestCollector(t, source, func(response http.ResponseWriter, _ *http.Request) {
				document := test.content
				if document == "" {
					document = `{}`
				}
				writeCursorJSON(response, test.status, document)
			})
			defer server.Close()
			_, err := collectProvider(context.Background(), collector.config)
			if !errors.Is(err, test.want) || strings.Contains(fmt.Sprint(err), secret) {
				t.Fatalf("error = %q, want %v without secret", err, test.want)
			}
		})
	}
}

func TestPrivateRequestTimeoutAndRedirectStayInsideCursorAdapter(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		collector, server := newCursorTestCollector(t, &sequenceTokenSource{tokens: [][]byte{[]byte("token")}}, func(
			_ http.ResponseWriter,
			_ *http.Request,
		) {
			time.Sleep(100 * time.Millisecond)
		})
		defer server.Close()
		collector.config.RequestTimeout = 20 * time.Millisecond
		ctx, cancel := context.WithTimeout(context.Background(), collector.config.RequestTimeout)
		defer cancel()
		_, err := collectProvider(ctx, collector.config)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("redirect", func(t *testing.T) {
		redirectFollowed := false
		var server *httptest.Server
		server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/redirected" {
				redirectFollowed = true
				writeCursorJSON(response, http.StatusOK, validCursorUsageDocument())
				return
			}
			http.Redirect(response, request, server.URL+"/redirected", http.StatusTemporaryRedirect)
		}))
		defer server.Close()
		collector, err := New(Config{
			TokenSource: &sequenceTokenSource{tokens: [][]byte{[]byte("token")}},
			HTTPClient:  server.Client(),
			Now:         func() time.Time { return cursorTestNow },
			endpointURL: server.URL + "/usage",
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = collectProvider(context.Background(), collector.config)
		if !errors.Is(err, ErrUnavailable) || redirectFollowed {
			t.Fatalf("error=%v redirectFollowed=%v", err, redirectFollowed)
		}
	})
}

func TestCollectorRetainsLastCursorPageAsDegradedOnFailure(t *testing.T) {
	requestCount := 0
	collector, server := newCursorTestCollector(t, &sequenceTokenSource{tokens: [][]byte{[]byte("token")}}, func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		requestCount++
		if requestCount == 1 {
			writeCursorJSON(response, http.StatusOK, validCursorUsageDocument())
			return
		}
		writeCursorJSON(response, http.StatusServiceUnavailable, `{}`)
	})
	defer server.Close()
	first, succeeded, err := collector.collectRetained(context.Background())
	if err != nil || !succeeded || first.Status != aisnapshot.ProviderOK {
		t.Fatalf("first = %+v succeeded=%v err=%v", first, succeeded, err)
	}
	second, succeeded, err := collector.collectRetained(context.Background())
	if err != nil || succeeded || second.Status != aisnapshot.ProviderDegraded ||
		second.Error == nil || second.Error.Code != aisnapshot.ProviderErrorUnavailable ||
		len(second.Windows) != len(first.Windows) ||
		*second.Windows[0].UsedBasisPoints != *first.Windows[0].UsedBasisPoints ||
		*second.UpdatedAt != *first.UpdatedAt {
		t.Fatalf("retained = %+v succeeded=%v err=%v", second, succeeded, err)
	}
	*second.Windows[0].UsedBasisPoints = 9999
	third, _, _ := collector.collectRetained(context.Background())
	if *third.Windows[0].UsedBasisPoints != 2500 {
		t.Fatal("publisher-owned provider mutated retained Cursor page")
	}
}

func TestCollectorPublishesUnavailableBeforeAnySuccessfulCursorRead(t *testing.T) {
	collector, server := newCursorTestCollector(t, &sequenceTokenSource{err: ErrNotLoggedIn}, func(
		http.ResponseWriter,
		*http.Request,
	) {
		t.Fatal("HTTP request must not run without an access token")
	})
	defer server.Close()
	provider, succeeded, err := collector.collectRetained(context.Background())
	if err != nil || succeeded || provider.Status != aisnapshot.ProviderUnavailable ||
		provider.Source != aisnapshot.ProviderSourceNone ||
		provider.Confidence != aisnapshot.ConfidenceUnavailable || !provider.Experimental ||
		provider.Error == nil || provider.Error.Code != aisnapshot.ProviderErrorAuthStale {
		t.Fatalf("provider = %+v succeeded=%v err=%v", provider, succeeded, err)
	}
}

func TestCollectorStopsWithinContextAndDoesNotLeakCollectionFailure(t *testing.T) {
	collector, server := newCursorTestCollector(t, &sequenceTokenSource{err: ErrDatabaseLocked}, func(
		http.ResponseWriter,
		*http.Request,
	) {
	})
	defer server.Close()
	collector.config.RetryInterval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	published := make(chan aisnapshot.Provider, 1)
	done := make(chan error, 1)
	go func() {
		done <- collector.Run(ctx, func(_ context.Context, provider aisnapshot.Provider) error {
			published <- provider
			return nil
		})
	}()
	select {
	case provider := <-published:
		if provider.Error == nil || provider.Error.Code != aisnapshot.ProviderErrorUnavailable {
			t.Fatalf("provider = %+v", provider)
		}
	case <-time.After(time.Second):
		t.Fatal("collector did not publish isolated unavailable state")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("collector did not stop")
	}
}

func TestVersionedAdapterRejectsUnknownVersions(t *testing.T) {
	for _, config := range []Config{
		{AdapterVersion: AdapterVersion + 1},
		{ResponseSchemaVersion: ResponseSchemaVersion + 1},
	} {
		if _, err := New(config); err == nil {
			t.Fatalf("New(%+v) succeeded", config)
		}
	}
}

func TestValidResponseFixtureRemainsJSON(t *testing.T) {
	var value any
	if err := json.Unmarshal([]byte(validCursorUsageDocument()), &value); err != nil {
		t.Fatal(err)
	}
}
