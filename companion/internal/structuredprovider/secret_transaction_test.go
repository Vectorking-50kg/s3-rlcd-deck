package structuredprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/secretstore"
)

type transactionSecretStore struct {
	values     map[secretstore.Reference][]byte
	putCalls   int
	putErrorAt int
	deleteErr  error
}

func newTransactionSecretStore() *transactionSecretStore {
	return &transactionSecretStore{values: make(map[secretstore.Reference][]byte)}
}

func (store *transactionSecretStore) Put(
	_ context.Context,
	current secretstore.Reference,
	value []byte,
) (secretstore.Reference, error) {
	store.putCalls++
	if store.putErrorAt == store.putCalls {
		return "", secretstore.ErrPermission
	}
	if current != "" {
		if _, exists := store.values[current]; !exists {
			return "", secretstore.ErrNotFound
		}
		store.values[current] = append([]byte(nil), value...)
		return current, nil
	}
	reference := secretstore.Reference(fmt.Sprintf("secret-%032x", store.putCalls))
	store.values[reference] = append([]byte(nil), value...)
	return reference, nil
}

func (store *transactionSecretStore) Get(
	_ context.Context,
	reference secretstore.Reference,
) ([]byte, error) {
	value, exists := store.values[reference]
	if !exists {
		return nil, secretstore.ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (store *transactionSecretStore) Delete(
	_ context.Context,
	reference secretstore.Reference,
) error {
	if store.deleteErr != nil {
		return store.deleteErr
	}
	delete(store.values, reference)
	return nil
}

func TestCommitDefinitionPersistsOnlyOpaqueReferenceAndResolvesForRequest(t *testing.T) {
	const canary = "PRIVATE_PROVIDER_COMMIT_CANARY"
	template := Templates()[0]
	secrets := newTransactionSecretStore()
	var persisted []byte
	committed, err := CommitDefinition(
		context.Background(),
		template.Definition,
		[]SecretBinding{{HeaderIndex: 0, Value: []byte(canary)}},
		secrets,
		func(_ context.Context, definition Definition) error {
			var marshalErr error
			persisted, marshalErr = json.Marshal(definition)
			return marshalErr
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	reference := committed.Request.Headers[0].SecretReference
	if _, err = secretstore.ParseReference(reference.String()); err != nil {
		t.Fatalf("committed reference = %q", reference)
	}
	if bytes.Contains(persisted, []byte(canary)) ||
		bytes.Contains(persisted, []byte(templateSecretReference)) ||
		!bytes.Contains(persisted, []byte(reference)) {
		t.Fatalf("persisted definition crossed secret boundary: %s", persisted)
	}
	resolved, err := secrets.Get(context.Background(), reference)
	if err != nil || string(resolved) != canary {
		t.Fatalf("resolved secret = %q, %v", resolved, err)
	}
	overwrite(resolved)
	if _, err = New(Config{Definition: committed, Secrets: secrets}); err != nil {
		t.Fatalf("collector rejected committed secret reference: %v", err)
	}
}

func TestCommitDefinitionRollsBackConfigAndPartialSecretFailures(t *testing.T) {
	const canary = "PRIVATE_PROVIDER_ROLLBACK_CANARY"
	template := Templates()[0]
	secrets := newTransactionSecretStore()
	_, err := CommitDefinition(
		context.Background(),
		template.Definition,
		[]SecretBinding{{HeaderIndex: 0, Value: []byte(canary)}},
		secrets,
		func(context.Context, Definition) error { return errors.New(canary) },
	)
	if !errors.Is(err, ErrDefinitionCommit) || strings.Contains(err.Error(), canary) ||
		len(secrets.values) != 0 {
		t.Fatalf("failed config commit = %v, remaining secrets = %d", err, len(secrets.values))
	}

	definition := template.Definition
	definition.Request.Headers = append(definition.Request.Headers, Header{Name: "X-API-Key"})
	secrets = newTransactionSecretStore()
	secrets.putErrorAt = 2
	_, err = CommitDefinition(
		context.Background(),
		definition,
		[]SecretBinding{
			{HeaderIndex: 0, Value: []byte("first")},
			{HeaderIndex: 1, Value: []byte("second")},
		},
		secrets,
		func(context.Context, Definition) error { return nil },
	)
	if !errors.Is(err, secretstore.ErrPermission) || len(secrets.values) != 0 {
		t.Fatalf("partial secret failure = %v, remaining secrets = %d", err, len(secrets.values))
	}
}

func TestCommitCurlImportTransfersAndClearsSecretBuffers(t *testing.T) {
	const canary = "PRIVATE_CURL_COMMIT_CANARY"
	imported, err := ImportCurl(
		"curl https://usage.example.test/v1/balance -H 'Authorization: Bearer " + canary + "'",
	)
	if err != nil {
		t.Fatal(err)
	}
	originalBuffer := imported.Secrets[0].Value
	secrets := newTransactionSecretStore()
	var persisted []byte
	committed, err := CommitCurlImport(
		context.Background(),
		validDefinition(),
		&imported,
		secrets,
		func(_ context.Context, definition Definition) error {
			persisted, err = json.Marshal(definition)
			return err
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(imported.Secrets) != 0 ||
		!bytes.Equal(originalBuffer, make([]byte, len(originalBuffer))) {
		t.Fatal("CommitCurlImport retained caller-visible secret bytes")
	}
	if bytes.Contains(persisted, []byte(canary)) ||
		committed.Request.Headers[0].SecretReference == "" {
		t.Fatalf("committed curl definition crossed secret boundary: %s", persisted)
	}
}

func TestCommitDefinitionReportsCompensationFailureWithoutLeakingSecret(t *testing.T) {
	const canary = "PRIVATE_PROVIDER_COMPENSATION_CANARY"
	secrets := newTransactionSecretStore()
	secrets.deleteErr = errors.New(canary)
	_, err := CommitDefinition(
		context.Background(),
		Templates()[0].Definition,
		[]SecretBinding{{HeaderIndex: 0, Value: []byte(canary)}},
		secrets,
		func(context.Context, Definition) error { return errors.New(canary) },
	)
	if !errors.Is(err, ErrSecretRollback) || strings.Contains(err.Error(), canary) {
		t.Fatalf("compensation error = %v", err)
	}
	var rollback *SecretRollbackError
	if !errors.As(err, &rollback) || len(rollback.PendingReferences()) != 1 {
		t.Fatalf("compensation did not preserve cleanup metadata: %#v", err)
	}
}
