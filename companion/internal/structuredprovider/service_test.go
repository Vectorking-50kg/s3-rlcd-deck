package structuredprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
)

func TestServiceCreatesUpdatesOrdersAndDeletesWithoutExposingSecretReferences(t *testing.T) {
	owner, err := OpenDefinitionStore(filepath.Join(t.TempDir(), "providers.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	secrets := newTransactionSecretStore()
	service, err := NewService(owner, secrets)
	if err != nil {
		t.Fatal(err)
	}
	firstDraft := Templates()[0].Definition
	first, err := service.Save(
		context.Background(),
		"",
		firstDraft,
		[]SecretBinding{{HeaderIndex: 0, Value: []byte("PRIVATE_FIRST_PROVIDER_KEY")}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Request.Headers) != 1 || !first.Request.Headers[0].SecretConfigured {
		t.Fatalf("created Provider view = %#v", first)
	}
	serialized, _ := json.Marshal(first)
	if bytes.Contains(serialized, []byte("PRIVATE_FIRST_PROVIDER_KEY")) ||
		bytes.Contains(serialized, []byte("secret-")) {
		t.Fatalf("Provider view exposed credential metadata: %s", serialized)
	}

	secondDraft := Templates()[1].Definition
	if _, err = service.Save(
		context.Background(), "", secondDraft,
		[]SecretBinding{{HeaderIndex: 0, Value: []byte("PRIVATE_SECOND_PROVIDER_KEY")}}, nil,
	); err != nil {
		t.Fatal(err)
	}
	if err = service.Reorder(context.Background(), []string{"deepseek", "aihubmix"}); err != nil {
		t.Fatal(err)
	}
	firstDraft.DisplayName = "AIHubMix Primary"
	updated, err := service.Save(
		context.Background(), "aihubmix", firstDraft, nil, []int{0},
	)
	if err != nil || updated.DisplayName != "AIHubMix Primary" {
		t.Fatalf("updated Provider = %#v, %v", updated, err)
	}
	listed, err := service.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{listed[0].ID, listed[1].ID}; !reflect.DeepEqual(got, []string{"deepseek", "aihubmix"}) {
		t.Fatalf("ordered Provider IDs = %#v", got)
	}
	if err = service.Delete(context.Background(), "deepseek"); err != nil {
		t.Fatal(err)
	}
	listed, err = service.List(context.Background())
	if err != nil || len(listed) != 1 || listed[0].ID != "aihubmix" {
		t.Fatalf("Providers after delete = %#v, %v", listed, err)
	}
}

func TestServiceRejectsClientSuppliedReferencesAndClearsCredentialInput(t *testing.T) {
	owner, err := OpenDefinitionStore(filepath.Join(t.TempDir(), "providers.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	service, err := NewService(owner, newTransactionSecretStore())
	if err != nil {
		t.Fatal(err)
	}
	draft := Templates()[0].Definition
	draft.Request.Headers[0].SecretReference = "secret-11111111111111111111111111111111"
	secret := []byte("PRIVATE_REJECTED_PROVIDER_KEY")
	_, err = service.Save(
		context.Background(), "", draft,
		[]SecretBinding{{HeaderIndex: 0, Value: secret}}, nil,
	)
	if err == nil {
		t.Fatal("client-supplied Secret Reference was accepted")
	}
	if !bytes.Equal(secret, make([]byte, len(secret))) {
		t.Fatal("rejected credential input was not cleared")
	}
}

type serviceState struct {
	order     []string
	providers []aisnapshot.Provider
}

type fakeManagedCollector struct {
	provider aisnapshot.Provider
	terminal error
}

func (collector *fakeManagedCollector) Run(ctx context.Context, publish Publisher) error {
	if err := publish(ctx, collector.provider); err != nil {
		return err
	}
	if collector.terminal != nil {
		return collector.terminal
	}
	<-ctx.Done()
	return nil
}

func (collector *fakeManagedCollector) TestRequest(context.Context) (Preview, error) {
	return Preview{Provider: collector.provider.Clone()}, collector.terminal
}

func TestServiceReconcilesDynamicDefinitionsOrderAndIsolatesFailure(t *testing.T) {
	owner, err := OpenDefinitionStore(filepath.Join(t.TempDir(), "providers.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	service, err := NewService(owner, newTransactionSecretStore())
	if err != nil {
		t.Fatal(err)
	}
	service.newCollector = func(config Config) (managedCollector, error) {
		provider := aisnapshot.Provider{
			SchemaVersion: aisnapshot.SchemaVersion{Major: 1, Minor: 0},
			ID:            config.Definition.ID, DisplayName: config.Definition.DisplayName,
			Status: aisnapshot.ProviderOK, Source: aisnapshot.ProviderSourceStructuredHTTP,
			Confidence: aisnapshot.ConfidenceVerified, Windows: []aisnapshot.QuotaWindow{},
		}
		collector := &fakeManagedCollector{provider: provider}
		if provider.ID == "deepseek" {
			collector.terminal = errors.New("isolated Provider failure")
		}
		return collector, nil
	}
	states := make(chan serviceState, 32)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- service.Run(ctx, func(_ context.Context, order []string, providers []aisnapshot.Provider) error {
			states <- serviceState{order: append([]string(nil), order...), providers: providers}
			return nil
		})
	}()
	waitServiceState(t, states, func(state serviceState) bool { return len(state.order) == 0 })

	first := Templates()[0].Definition
	if _, err = service.Save(context.Background(), "", first,
		[]SecretBinding{{HeaderIndex: 0, Value: []byte("PRIVATE_FIRST")}}, nil); err != nil {
		t.Fatal(err)
	}
	waitServiceState(t, states, func(state serviceState) bool {
		return reflect.DeepEqual(state.order, []string{"aihubmix"}) &&
			len(state.providers) == 1 && state.providers[0].Status == aisnapshot.ProviderOK
	})

	second := Templates()[1].Definition
	if _, err = service.Save(context.Background(), "", second,
		[]SecretBinding{{HeaderIndex: 0, Value: []byte("PRIVATE_SECOND")}}, nil); err != nil {
		t.Fatal(err)
	}
	waitServiceState(t, states, func(state serviceState) bool {
		return reflect.DeepEqual(state.order, []string{"aihubmix", "deepseek"}) &&
			len(state.providers) == 2 && state.providers[1].Status == aisnapshot.ProviderUnavailable
	})
	if err = service.Reorder(context.Background(), []string{"deepseek", "aihubmix"}); err != nil {
		t.Fatal(err)
	}
	waitServiceState(t, states, func(state serviceState) bool {
		return reflect.DeepEqual(state.order, []string{"deepseek", "aihubmix"}) &&
			len(state.providers) == 2 && state.providers[1].Status == aisnapshot.ProviderOK
	})
	if err = service.Delete(context.Background(), "deepseek"); err != nil {
		t.Fatal(err)
	}
	waitServiceState(t, states, func(state serviceState) bool {
		return reflect.DeepEqual(state.order, []string{"aihubmix"}) && len(state.providers) == 1
	})
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Service did not stop")
	}
}

func TestServiceTestRequestUsesPersistedDefinitionAndReturnsOnlyPreview(t *testing.T) {
	owner, err := OpenDefinitionStore(filepath.Join(t.TempDir(), "providers.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	service, err := NewService(owner, newTransactionSecretStore())
	if err != nil {
		t.Fatal(err)
	}
	draft := Templates()[0].Definition
	if _, err = service.Save(context.Background(), "", draft,
		[]SecretBinding{{HeaderIndex: 0, Value: []byte("PRIVATE_TEST_REQUEST")}}, nil); err != nil {
		t.Fatal(err)
	}
	if err = service.SetDiagnosticSink(func(Diagnostic) {}); err != nil {
		t.Fatal(err)
	}
	observedDiagnosticSink := false
	service.newCollector = func(config Config) (managedCollector, error) {
		observedDiagnosticSink = config.Diagnostic != nil
		return &fakeManagedCollector{provider: aisnapshot.Provider{
			SchemaVersion: aisnapshot.SchemaVersion{Major: 1, Minor: 0},
			ID:            config.Definition.ID, DisplayName: config.Definition.DisplayName,
			Status: aisnapshot.ProviderOK, Source: aisnapshot.ProviderSourceStructuredHTTP,
			Confidence: aisnapshot.ConfidenceVerified, Windows: []aisnapshot.QuotaWindow{},
		}}, nil
	}
	preview, err := service.Test(context.Background(), "aihubmix")
	if err != nil || preview.Provider.ID != "aihubmix" {
		t.Fatalf("Test() = %#v, %v", preview, err)
	}
	if !observedDiagnosticSink {
		t.Fatal("service did not connect the fixed diagnostic sink")
	}
	encoded, _ := json.Marshal(preview)
	if bytes.Contains(encoded, []byte("PRIVATE_TEST_REQUEST")) ||
		bytes.Contains(encoded, []byte("secret-")) || bytes.Contains(encoded, []byte("aihubmix.com")) {
		t.Fatalf("Test() exposed request boundary: %s", encoded)
	}
}

func waitServiceState(t *testing.T, states <-chan serviceState, accept func(serviceState) bool) serviceState {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case state := <-states:
			if accept(state) {
				return state
			}
		case <-deadline.C:
			t.Fatal("timed out waiting for structured Provider service state")
		}
	}
}
