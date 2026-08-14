package structuredprovider

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/secretstore"
)

const testSecretReference = secretstore.Reference("secret-0123456789abcdef0123456789abcdef")

type staticHTTPClient struct {
	response func(*http.Request) (*http.Response, error)
}

func (client staticHTTPClient) Do(request *http.Request) (*http.Response, error) {
	return client.response(request)
}

type countingSecrets struct {
	mutex    sync.Mutex
	values   [][]byte
	requests int
}

func (secrets *countingSecrets) Get(context.Context, secretstore.Reference) ([]byte, error) {
	secrets.mutex.Lock()
	defer secrets.mutex.Unlock()
	secrets.requests++
	value := []byte("secret-" + string(rune('0'+secrets.requests)))
	secrets.values = append(secrets.values, value)
	return value, nil
}

func validDefinition() Definition {
	return Definition{
		ID:          "custom",
		DisplayName: "Custom",
		Request: Request{
			Method: MethodGET,
			URL:    "https://usage.example.test/v1/balance",
			Headers: []Header{{
				Name:            "Authorization",
				SecretReference: testSecretReference,
				Prefix:          "Bearer ",
			}},
		},
		Mapping: Mapping{
			BalancePath:  "$.account.balance",
			UsedPath:     "$.quota.used",
			TotalPath:    "$.quota.total",
			ResetPath:    "$.quota.reset",
			CurrencyPath: "$.account.currency",
			WindowName:   "monthly",
			ResetFormat:  ResetRFC3339,
		},
		RefreshMinutes: 5,
	}
}

func jsonResponse(status int, document string) *http.Response {
	return &http.Response{
		StatusCode:    status,
		ContentLength: int64(len(document)),
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(strings.NewReader(document)),
	}
}

const validDocument = `{
  "account":{"balance":"12.345678","currency":"USD"},
  "quota":{"used":25,"total":100,"reset":"2026-08-15T00:00:00Z"}
}`

func TestStructuredProviderNormalizesAndRereadsSecret(t *testing.T) {
	secrets := &countingSecrets{}
	var authorizations []string
	client := staticHTTPClient{response: func(request *http.Request) (*http.Response, error) {
		authorizations = append(authorizations, request.Header.Get("Authorization"))
		if request.Header.Get("Accept-Encoding") != "identity" {
			t.Fatal("request did not opt out of compressed responses")
		}
		return jsonResponse(http.StatusOK, validDocument), nil
	}}
	definition := validDefinition()
	collector, err := New(Config{
		Definition: definition,
		Secrets:    secrets,
		Now:        func() time.Time { return time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC) },
		client:     client,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		preview, requestErr := collector.TestRequest(context.Background())
		if requestErr != nil {
			t.Fatalf("TestRequest() error = %v", requestErr)
		}
		provider := preview.Provider
		if provider.ID != "custom" || provider.Source != "structured_http" ||
			provider.Balance == nil || provider.Balance.AmountMicros != 12_345_678 ||
			provider.Balance.Currency != "USD" || len(provider.Windows) != 1 ||
			*provider.Windows[0].UsedBasisPoints != 2500 ||
			*provider.Windows[0].RemainingBasisPoints != 7500 ||
			*provider.Windows[0].ResetsAt != "2026-08-15T00:00:00Z" ||
			preview.Diagnostic.HTTPStatus != 200 || !preview.Diagnostic.ResponseAccepted {
			t.Fatalf("preview = %+v", preview)
		}
	}
	if strings.Join(authorizations, ",") != "Bearer secret-1,Bearer secret-2" {
		t.Fatalf("Authorization history = %v", authorizations)
	}
	for _, resolved := range secrets.values {
		if !bytes.Equal(resolved, make([]byte, len(resolved))) {
			t.Fatal("resolved secret was not overwritten")
		}
	}
}

func TestStructuredProviderRejectsUnsafeOrMalformedResponses(t *testing.T) {
	tooComplex := `{"account":{"balance":1,"currency":"USD"},"quota":{"used":1,"total":2,"reset":"2026-08-15T00:00:00Z"},"padding":[` +
		strings.Repeat("0,", maximumJSONNodes) + `0]}`
	tests := []struct {
		name      string
		document  string
		encoding  string
		length    int64
		wantError error
	}{
		{name: "negative", document: `{"account":{"balance":-1,"currency":"USD"},"quota":{"used":1,"total":2,"reset":"2026-08-15T00:00:00Z"}}`, wantError: ErrSchemaChanged},
		{name: "nan string", document: `{"account":{"balance":"NaN","currency":"USD"},"quota":{"used":1,"total":2,"reset":"2026-08-15T00:00:00Z"}}`, wantError: ErrSchemaChanged},
		{name: "infinite string", document: `{"account":{"balance":"Inf","currency":"USD"},"quota":{"used":1,"total":2,"reset":"2026-08-15T00:00:00Z"}}`, wantError: ErrSchemaChanged},
		{name: "used exceeds total", document: `{"account":{"balance":1,"currency":"USD"},"quota":{"used":3,"total":2,"reset":"2026-08-15T00:00:00Z"}}`, wantError: ErrSchemaChanged},
		{name: "duplicate", document: `{"account":{"balance":1,"balance":2,"currency":"USD"},"quota":{"used":1,"total":2,"reset":"2026-08-15T00:00:00Z"}}`, wantError: ErrSchemaChanged},
		{name: "compression", document: validDocument, encoding: "gzip", wantError: ErrSchemaChanged},
		{name: "declared oversized", document: `{}`, length: DefaultMaximumResponse + 1, wantError: ErrResponseTooLarge},
		{name: "streamed oversized", document: strings.Repeat(" ", DefaultMaximumResponse+1), wantError: ErrResponseTooLarge},
		{name: "too complex", document: tooComplex, wantError: ErrSchemaChanged},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			definition := validDefinition()
			client := staticHTTPClient{response: func(*http.Request) (*http.Response, error) {
				response := jsonResponse(http.StatusOK, testCase.document)
				if testCase.length != 0 {
					response.ContentLength = testCase.length
				}
				if testCase.encoding != "" {
					response.Header.Set("Content-Encoding", testCase.encoding)
				}
				return response, nil
			}}
			collector, err := New(Config{Definition: definition, Secrets: &countingSecrets{}, client: client})
			if err != nil {
				t.Fatal(err)
			}
			_, err = collector.TestRequest(context.Background())
			if !errors.Is(err, testCase.wantError) {
				t.Fatalf("TestRequest() error = %v, want %v", err, testCase.wantError)
			}
		})
	}
}

func TestFailureRetainsOnlyLastNormalizedProvider(t *testing.T) {
	var calls atomic.Int32
	client := staticHTTPClient{response: func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return jsonResponse(http.StatusOK, validDocument), nil
		}
		return jsonResponse(http.StatusUnauthorized, `{}`), nil
	}}
	collector, err := New(Config{Definition: validDefinition(), Secrets: &countingSecrets{}, client: client})
	if err != nil {
		t.Fatal(err)
	}
	first, err := collector.collectRetained(context.Background())
	if err != nil || first.Status != aisnapshot.ProviderOK {
		t.Fatalf("first = %+v, %v", first, err)
	}
	second, err := collector.collectRetained(context.Background())
	if err != nil || second.Status != aisnapshot.ProviderDegraded || second.Balance == nil ||
		second.Error == nil || second.Error.Code != aisnapshot.ProviderErrorAuthStale {
		t.Fatalf("second = %+v, %v", second, err)
	}
}

func TestTemplatesMatchSupportedProviderContracts(t *testing.T) {
	templates := Templates()
	if len(templates) != 2 || templates[0].ID != "aihubmix" || templates[1].ID != "deepseek" {
		t.Fatalf("Templates() = %+v", templates)
	}
	for _, template := range templates {
		if len(template.SecretSlots) != 1 || template.Definition.Request.Method != MethodGET {
			t.Fatalf("template = %+v", template)
		}
		definition := cloneDefinition(template.Definition)
		definition.Request.Headers[0].SecretReference = testSecretReference
		if _, err := New(Config{Definition: definition}); err != nil {
			t.Fatalf("New(%s) error = %v", template.ID, err)
		}
	}
	tests := []struct {
		index    int
		document string
		micros   uint64
		currency string
	}{
		{index: 0, document: `{"data":{"quota":500000,"used_quota":250000}}`, micros: 1_000_000, currency: "USD"},
		{index: 1, document: `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"110.00","granted_balance":"10.00","topped_up_balance":"100.00"}]}`, micros: 110_000_000, currency: "CNY"},
	}
	for _, testCase := range tests {
		template := templates[testCase.index]
		definition := cloneDefinition(template.Definition)
		definition.Request.Headers[0].SecretReference = testSecretReference
		collector, err := New(Config{
			Definition: definition,
			Secrets:    &countingSecrets{},
			client: staticHTTPClient{response: func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, testCase.document), nil
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		preview, err := collector.TestRequest(context.Background())
		if err != nil || preview.Provider.Balance == nil ||
			preview.Provider.Balance.AmountMicros != testCase.micros ||
			preview.Provider.Balance.Currency != testCase.currency {
			t.Fatalf("template %s preview = %+v, %v", template.ID, preview, err)
		}
	}
}

func TestHTTPStatusTimeoutAndDiagnosticsUseFixedErrorCodes(t *testing.T) {
	tests := []struct {
		name      string
		response  func(*http.Request) (*http.Response, error)
		wantError error
		wantCode  string
	}{
		{name: "auth", response: func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusUnauthorized, `{}`), nil
		}, wantError: ErrAuthStale, wantCode: "auth_stale"},
		{name: "permission", response: func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusForbidden, `{}`), nil
		}, wantError: ErrPermission, wantCode: "permission_denied"},
		{name: "timeout", response: func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		}, wantError: context.DeadlineExceeded, wantCode: "timeout"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			definition := validDefinition()
			var observed Diagnostic
			collector, err := New(Config{
				Definition: definition,
				Secrets:    &countingSecrets{},
				Diagnostic: func(diagnostic Diagnostic) { observed = diagnostic },
				client:     staticHTTPClient{response: testCase.response},
				requestTimeout: func() time.Duration {
					if testCase.name == "timeout" {
						return 20 * time.Millisecond
					}
					return 0
				}(),
			})
			if err != nil {
				t.Fatal(err)
			}
			preview, err := collector.TestRequest(context.Background())
			if !errors.Is(err, testCase.wantError) || preview.Diagnostic.ErrorCode != testCase.wantCode ||
				observed != preview.Diagnostic {
				t.Fatalf("preview = %+v, observed = %+v, error = %v", preview, observed, err)
			}
		})
	}
}

func TestConcurrentRefreshIsSerializedAndBounded(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var active atomic.Int32
	var maximum atomic.Int32
	client := staticHTTPClient{response: func(*http.Request) (*http.Response, error) {
		current := active.Add(1)
		defer active.Add(-1)
		if current > maximum.Load() {
			maximum.Store(current)
		}
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
		return jsonResponse(http.StatusOK, validDocument), nil
	}}
	collector, err := New(Config{Definition: validDefinition(), Secrets: &countingSecrets{}, client: client})
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() {
		_, requestErr := collector.TestRequest(context.Background())
		firstDone <- requestErr
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, err = collector.TestRequest(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrent TestRequest() error = %v", err)
	}
	close(release)
	if err = <-firstDone; err != nil {
		t.Fatalf("first TestRequest() error = %v", err)
	}
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent requests = %d", maximum.Load())
	}
}

func TestHTTPDefinitionWarnsAndStillUsesNetworkPolicy(t *testing.T) {
	definition := validDefinition()
	definition.Request.URL = "http://usage.lan/v1"
	collector, err := New(Config{
		Definition: definition,
		Secrets:    &countingSecrets{},
		client: staticHTTPClient{response: func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, validDocument), nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := collector.TestRequest(context.Background())
	if err != nil || preview.Warning == "" {
		t.Fatalf("HTTP preview = %+v, %v", preview, err)
	}
}
