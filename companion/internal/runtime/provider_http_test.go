package runtime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	companionruntime "github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/runtime"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/secretstore"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/structuredprovider"
)

type providerHTTPSecrets struct {
	mutex  sync.Mutex
	next   int
	values map[secretstore.Reference][]byte
}

func newProviderHTTPSecrets() *providerHTTPSecrets {
	return &providerHTTPSecrets{values: make(map[secretstore.Reference][]byte)}
}

func (store *providerHTTPSecrets) PutNew(
	ctx context.Context,
	value []byte,
	before func(secretstore.Reference) error,
) (secretstore.Reference, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	store.next++
	reference := secretstore.Reference(fmt.Sprintf("secret-%032x", store.next))
	if err := before(reference); err != nil {
		return "", err
	}
	store.values[reference] = append([]byte(nil), value...)
	return reference, nil
}

func (store *providerHTTPSecrets) Get(
	ctx context.Context,
	reference secretstore.Reference,
) ([]byte, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	value, exists := store.values[reference]
	if !exists {
		return nil, secretstore.ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (store *providerHTTPSecrets) Delete(
	ctx context.Context,
	reference secretstore.Reference,
) error {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	delete(store.values, reference)
	return nil
}

func TestManagementProviderCRUDIsAuthenticatedTransactionalAndRedacted(t *testing.T) {
	owner, err := structuredprovider.OpenDefinitionStore(filepath.Join(t.TempDir(), "providers.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	service, err := structuredprovider.NewService(owner, newProviderHTTPSecrets())
	if err != nil {
		t.Fatal(err)
	}
	config := testConfig()
	config.StructuredProviders = service
	// Login and all five Provider mutations below share the sensitive request
	// budget. A seventh mutation proves reorder and delete cannot bypass it.
	config.Management.Limits.SensitiveRequests = 6
	application, err := companionruntime.New(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	status := waitForState(t, application, companionruntime.StateReady)
	client, session, csrf := loginManagement(t, status.ManagementAddress, config.Management.AdminToken)

	unauthorized, err := http.Get("http://" + status.ManagementAddress + "/api/v1/providers")
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated Provider list = %d", unauthorized.StatusCode)
	}

	template := service.Templates()[0]
	definition := structuredprovider.Definition{
		ID: template.ID, DisplayName: template.DisplayName,
		Request: structuredprovider.Request{
			Method: template.Request.Method, URL: template.Request.URL,
			Headers: []structuredprovider.Header{{
				Name: template.Request.Headers[0].Name, Prefix: template.Request.Headers[0].Prefix,
			}},
			Body: template.Request.Body,
		},
		Mapping: template.Mapping, RefreshMinutes: template.RefreshMinutes,
		RequestTimeoutSeconds: 1, MaximumResponseBytes: template.MaximumResponseBytes,
	}
	const credential = "PRIVATE_PROVIDER_HTTP_CANARY"
	createBody, _ := json.Marshal(map[string]any{
		"definition":    definition,
		"secrets":       []map[string]any{{"header_index": 0, "value": []byte(credential)}},
		"keep_existing": []int{},
	})
	response := providerManagementRequest(
		t, client, status.ManagementAddress, session, csrf,
		http.MethodPost, "/api/v1/providers", createBody,
	)
	createdBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusCreated ||
		bytes.Contains(createdBody, []byte(credential)) || bytes.Contains(createdBody, []byte("secret-")) ||
		!bytes.Contains(createdBody, []byte(`"secret_configured":true`)) {
		t.Fatalf("create status=%d body=%s", response.StatusCode, createdBody)
	}

	request, _ := http.NewRequest(
		http.MethodGet, "http://"+status.ManagementAddress+"/api/v1/providers", nil,
	)
	request.AddCookie(session)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	listedBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || bytes.Contains(listedBody, []byte(credential)) ||
		bytes.Contains(listedBody, []byte("secret-")) || !bytes.Contains(listedBody, []byte(`"templates"`)) {
		t.Fatalf("list status=%d body=%s", response.StatusCode, listedBody)
	}

	definition.DisplayName = "AIHubMix Primary"
	updateBody, _ := json.Marshal(map[string]any{
		"definition": definition, "secrets": []any{}, "keep_existing": []int{0},
	})
	response = providerManagementRequest(
		t, client, status.ManagementAddress, session, csrf,
		http.MethodPut, "/api/v1/providers/aihubmix", updateBody,
	)
	updatedBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !bytes.Contains(updatedBody, []byte("AIHubMix Primary")) {
		t.Fatalf("update status=%d body=%s", response.StatusCode, updatedBody)
	}

	orderBody := []byte(`{"provider_ids":["aihubmix"]}`)
	response = providerManagementRequest(
		t, client, status.ManagementAddress, session, csrf,
		http.MethodPut, "/api/v1/providers/order", orderBody,
	)
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("reorder status=%d", response.StatusCode)
	}

	response = providerManagementRequest(
		t, client, status.ManagementAddress, session, csrf,
		http.MethodPost, "/api/v1/providers/missing/test", []byte(`{}`),
	)
	testBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(testBody), `"ok":false`) ||
		bytes.Contains(testBody, []byte(credential)) || bytes.Contains(testBody, []byte("secret-")) {
		t.Fatalf("test status=%d body=%s", response.StatusCode, testBody)
	}

	response = providerManagementRequest(
		t, client, status.ManagementAddress, session, csrf,
		http.MethodDelete, "/api/v1/providers/aihubmix", nil,
	)
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status=%d", response.StatusCode)
	}

	response = providerManagementRequest(
		t, client, status.ManagementAddress, session, csrf,
		http.MethodPut, "/api/v1/providers/order", []byte(`{"provider_ids":[]}`),
	)
	response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("Provider mutation after sensitive budget = %d, want 429", response.StatusCode)
	}

	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func providerManagementRequest(
	t *testing.T,
	client *http.Client,
	address string,
	session *http.Cookie,
	csrf string,
	method string,
	path string,
	body []byte,
) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, "http://"+address+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(session)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://"+address)
	request.Header.Set("X-CSRF-Token", csrf)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
