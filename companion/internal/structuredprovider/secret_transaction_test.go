package structuredprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/secretstore"
)

type transactionSecretStore struct {
	values               map[secretstore.Reference][]byte
	putCalls             int
	putErrorAt           int
	putErrorAfterStageAt int
	deleteErr            error
}

type transactionDefinitionOwner struct {
	definition  *Definition
	pending     map[secretstore.Reference]struct{}
	publishErr  error
	completeErr error
}

func newTransactionDefinitionOwner() *transactionDefinitionOwner {
	return &transactionDefinitionOwner{pending: make(map[secretstore.Reference]struct{})}
}

func (owner *transactionDefinitionOwner) StageCleanup(
	_ context.Context,
	references []secretstore.Reference,
) error {
	for _, reference := range references {
		owner.pending[reference] = struct{}{}
	}
	return nil
}

func (owner *transactionDefinitionOwner) Publish(
	_ context.Context,
	current *Definition,
	replacement Definition,
	activated []secretstore.Reference,
) (bool, error) {
	if owner.publishErr != nil {
		return false, owner.publishErr
	}
	if current != nil && (owner.definition == nil || !reflect.DeepEqual(*owner.definition, *current)) {
		return false, ErrDefinitionCommit
	}
	for _, reference := range activated {
		delete(owner.pending, reference)
	}
	for _, reference := range retiredReferences(current, replacement) {
		owner.pending[reference] = struct{}{}
	}
	copy := cloneDefinition(replacement)
	owner.definition = &copy
	return true, nil
}

func (owner *transactionDefinitionOwner) CompleteCleanup(
	_ context.Context,
	references []secretstore.Reference,
) error {
	if owner.completeErr != nil {
		return owner.completeErr
	}
	for _, reference := range references {
		delete(owner.pending, reference)
	}
	return nil
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

func (store *transactionSecretStore) PutNew(
	ctx context.Context,
	value []byte,
	beforeCreate func(secretstore.Reference) error,
) (secretstore.Reference, error) {
	store.putCalls++
	if store.putErrorAt == store.putCalls {
		return "", secretstore.ErrPermission
	}
	reference := secretstore.Reference(fmt.Sprintf("secret-%032x", store.putCalls))
	if err := beforeCreate(reference); err != nil {
		return "", err
	}
	if store.putErrorAfterStageAt == store.putCalls {
		return "", secretstore.ErrPermission
	}
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
		nil,
		template.Definition,
		[]SecretBinding{{HeaderIndex: 0, Value: []byte(canary)}},
		secrets,
		newTransactionDefinitionOwner(),
	)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err = json.Marshal(committed)
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
		nil,
		template.Definition,
		[]SecretBinding{{HeaderIndex: 0, Value: []byte(canary)}},
		secrets,
		&transactionDefinitionOwner{
			pending:    make(map[secretstore.Reference]struct{}),
			publishErr: errors.New(canary),
		},
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
		nil,
		definition,
		[]SecretBinding{
			{HeaderIndex: 0, Value: []byte("first")},
			{HeaderIndex: 1, Value: []byte("second")},
		},
		secrets,
		newTransactionDefinitionOwner(),
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
		nil,
		validDefinition(),
		&imported,
		secrets,
		newTransactionDefinitionOwner(),
	)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err = json.Marshal(committed)
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
		nil,
		Templates()[0].Definition,
		[]SecretBinding{{HeaderIndex: 0, Value: []byte(canary)}},
		secrets,
		&transactionDefinitionOwner{
			pending:    make(map[secretstore.Reference]struct{}),
			publishErr: errors.New(canary),
		},
	)
	if !errors.Is(err, ErrSecretRollback) || strings.Contains(err.Error(), canary) {
		t.Fatalf("compensation error = %v", err)
	}
	var rollback *SecretRollbackError
	if !errors.As(err, &rollback) || len(rollback.PendingReferences()) != 1 {
		t.Fatalf("compensation did not preserve cleanup metadata: %#v", err)
	}
}

func TestDefinitionStoreUpdateRetiresOldSecretWithoutOrphan(t *testing.T) {
	const firstCanary = "PRIVATE_PROVIDER_OLD_CANARY"
	const secondCanary = "PRIVATE_PROVIDER_NEW_CANARY"
	path := filepath.Join(t.TempDir(), "structured-providers.json")
	owner, err := OpenDefinitionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	secrets := newTransactionSecretStore()
	first, err := CommitDefinition(
		context.Background(), nil, Templates()[0].Definition,
		[]SecretBinding{{HeaderIndex: 0, Value: []byte(firstCanary)}}, secrets, owner,
	)
	if err != nil {
		t.Fatal(err)
	}
	oldReference := first.Request.Headers[0].SecretReference
	draft := cloneDefinition(first)
	draft.Request.Headers[0].SecretReference = ""
	updated, err := CommitDefinition(
		context.Background(), &first, draft,
		[]SecretBinding{{HeaderIndex: 0, Value: []byte(secondCanary)}}, secrets, owner,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := secrets.values[oldReference]; exists {
		t.Fatal("retired secret was not deleted")
	}
	newReference := updated.Request.Headers[0].SecretReference
	if string(secrets.values[newReference]) != secondCanary {
		t.Fatal("replacement secret was not retained")
	}
	definitions, err := owner.Definitions(context.Background())
	if err != nil || len(definitions) != 1 || !reflect.DeepEqual(definitions[0], updated) {
		t.Fatalf("persisted definitions = %#v, %v", definitions, err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || bytes.Contains(contents, []byte(firstCanary)) ||
		bytes.Contains(contents, []byte(secondCanary)) {
		t.Fatalf("definition file crossed secret boundary: %s, %v", contents, err)
	}
}

func TestDefinitionStoreDeleteFailureIsDurableAndRetryable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "structured-providers.json")
	owner, err := OpenDefinitionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	secrets := newTransactionSecretStore()
	committed, err := CommitDefinition(
		context.Background(), nil, Templates()[0].Definition,
		[]SecretBinding{{HeaderIndex: 0, Value: []byte("delete-me")}}, secrets, owner,
	)
	if err != nil {
		t.Fatal(err)
	}
	secrets.deleteErr = secretstore.ErrLocked
	err = owner.DeleteDefinition(context.Background(), committed.ID, secrets)
	if !errors.Is(err, ErrSecretRollback) {
		t.Fatalf("delete failure = %v", err)
	}
	if definitions, loadErr := owner.Definitions(context.Background()); loadErr != nil || len(definitions) != 0 {
		t.Fatalf("definition remained after committed delete: %#v, %v", definitions, loadErr)
	}
	if err = owner.Close(); err != nil {
		t.Fatal(err)
	}
	owner, err = OpenDefinitionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	secrets.deleteErr = nil
	if err = owner.RetryCleanup(context.Background(), secrets); err != nil {
		t.Fatal(err)
	}
	if len(secrets.values) != 0 || len(owner.state.PendingSecretDeletes) != 0 {
		t.Fatalf("durable cleanup did not converge: values=%d pending=%d", len(secrets.values), len(owner.state.PendingSecretDeletes))
	}
}

func TestDefinitionStorePersistsFailedCreateCompensation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "structured-providers.json")
	owner, err := OpenDefinitionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	secrets := newTransactionSecretStore()
	secrets.deleteErr = secretstore.ErrPermission
	_, err = CommitDefinition(
		context.Background(), nil, Templates()[0].Definition,
		[]SecretBinding{{HeaderIndex: 0, Value: []byte("pending-cleanup")}}, secrets,
		&failingPublishOwner{DefinitionStore: owner},
	)
	if !errors.Is(err, ErrSecretRollback) || len(owner.state.PendingSecretDeletes) != 1 {
		t.Fatalf("failed compensation = %v, pending=%v", err, owner.state.PendingSecretDeletes)
	}
	if err = owner.Close(); err != nil {
		t.Fatal(err)
	}
	owner, err = OpenDefinitionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	secrets.deleteErr = nil
	if err = owner.RetryCleanup(context.Background(), secrets); err != nil {
		t.Fatal(err)
	}
}

func TestFailedVaultCreateClearsDurableIntent(t *testing.T) {
	owner, err := OpenDefinitionStore(filepath.Join(t.TempDir(), "structured-providers.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	secrets := newTransactionSecretStore()
	secrets.putErrorAfterStageAt = 1
	_, err = CommitDefinition(
		context.Background(), nil, Templates()[0].Definition,
		[]SecretBinding{{HeaderIndex: 0, Value: []byte("never-created")}}, secrets, owner,
	)
	if !errors.Is(err, secretstore.ErrPermission) || len(owner.state.PendingSecretDeletes) != 0 ||
		len(secrets.values) != 0 {
		t.Fatalf("failed create error=%v pending=%v values=%d", err, owner.state.PendingSecretDeletes, len(secrets.values))
	}
}

func TestDefinitionStoreRejectsUnstagedOrUnusedActivatedReferences(t *testing.T) {
	owner, err := OpenDefinitionStore(filepath.Join(t.TempDir(), "structured-providers.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	unstaged := secretstore.Reference("secret-11111111111111111111111111111111")
	draft := Templates()[0].Definition
	draft.Request.Headers[0].SecretReference = unstaged
	if _, err = CommitDefinition(
		context.Background(), nil, draft, nil, newTransactionSecretStore(), owner,
	); !errors.Is(err, ErrDefinitionCommit) {
		t.Fatalf("unstaged definition commit = %v", err)
	}

	normalized, err := normalizeConfig(Config{Definition: validDefinition()})
	if err != nil {
		t.Fatal(err)
	}
	unused := secretstore.Reference("secret-22222222222222222222222222222222")
	if err = owner.StageCleanup(context.Background(), []secretstore.Reference{unused}); err != nil {
		t.Fatal(err)
	}
	if committed, publishErr := owner.Publish(
		context.Background(), nil, normalized.Definition, []secretstore.Reference{unused},
	); committed || !errors.Is(publishErr, ErrDefinitionCommit) {
		t.Fatalf("unused activation committed=%v error=%v", committed, publishErr)
	}
	definitions, err := owner.Definitions(context.Background())
	if err != nil || len(definitions) != 0 {
		t.Fatalf("bypassed definitions = %#v, %v", definitions, err)
	}
}

type failingPublishOwner struct{ *DefinitionStore }

func (owner *failingPublishOwner) Publish(
	context.Context,
	*Definition,
	Definition,
	[]secretstore.Reference,
) (bool, error) {
	return false, ErrDefinitionCommit
}
