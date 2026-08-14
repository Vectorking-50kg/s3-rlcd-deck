package secretstore

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
)

type memoryVault struct {
	values        map[string][]byte
	createError   error
	updateError   error
	getError      error
	deleteError   error
	listError     error
	updateEntered chan struct{}
	updateRelease chan struct{}
}

func newMemoryVault() *memoryVault {
	return &memoryVault{values: make(map[string][]byte)}
}

func (vault *memoryVault) Create(_ context.Context, account string, secret []byte) error {
	if vault.createError != nil {
		return vault.createError
	}
	if _, exists := vault.values[account]; exists {
		return ErrDuplicate
	}
	vault.values[account] = append([]byte(nil), secret...)
	return nil
}

func (vault *memoryVault) Update(_ context.Context, account string, secret []byte) error {
	if vault.updateEntered != nil {
		vault.updateEntered <- struct{}{}
		<-vault.updateRelease
	}
	if vault.updateError != nil {
		return vault.updateError
	}
	if _, exists := vault.values[account]; !exists {
		return ErrNotFound
	}
	vault.values[account] = append([]byte(nil), secret...)
	return nil
}

func (vault *memoryVault) Get(_ context.Context, account string) ([]byte, error) {
	if vault.getError != nil {
		return nil, vault.getError
	}
	secret, exists := vault.values[account]
	if !exists {
		return nil, ErrNotFound
	}
	return append([]byte(nil), secret...), nil
}

func (vault *memoryVault) Delete(_ context.Context, account string) error {
	if vault.deleteError != nil {
		return vault.deleteError
	}
	if _, exists := vault.values[account]; !exists {
		return ErrNotFound
	}
	delete(vault.values, account)
	return nil
}

func (vault *memoryVault) List(context.Context) ([]string, error) {
	if vault.listError != nil {
		return nil, vault.listError
	}
	accounts := make([]string, 0, len(vault.values))
	for account := range vault.values {
		accounts = append(accounts, account)
	}
	sort.Strings(accounts)
	return accounts, nil
}

func TestPutGetUpdateDeleteAndListMetadata(t *testing.T) {
	vault := newMemoryVault()
	store, err := newStore(vault, bytes.NewReader(bytes.Repeat([]byte{0x42}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	reference, err := store.Put(context.Background(), "", []byte("first-secret"))
	if err != nil || !validReference(reference) {
		t.Fatalf("Put(create) = %q, %v", reference, err)
	}
	secret, err := store.Get(context.Background(), reference)
	if err != nil || string(secret) != "first-secret" {
		t.Fatalf("Get() = %q, %v", secret, err)
	}
	overwrite(secret)
	updated, err := store.Put(context.Background(), reference, []byte("second-secret"))
	if err != nil || updated != reference {
		t.Fatalf("Put(update) = %q, %v", updated, err)
	}
	metadata, err := store.ListMetadata(context.Background())
	if err != nil || len(metadata) != 1 || metadata[0].Reference != reference {
		t.Fatalf("ListMetadata() = %+v, %v", metadata, err)
	}
	if err = store.Delete(context.Background(), reference); err != nil {
		t.Fatal(err)
	}
	if err = store.Delete(context.Background(), reference); err != nil {
		t.Fatalf("idempotent Delete() error = %v", err)
	}
	if _, err = store.Get(context.Background(), reference); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(deleted) error = %v", err)
	}
}

func TestPutNewPersistsReferenceIntentBeforeVaultMutation(t *testing.T) {
	vault := newMemoryVault()
	store, err := newStore(vault, bytes.NewReader(bytes.Repeat([]byte{0x52}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	var staged Reference
	reference, err := store.PutNew(
		context.Background(),
		[]byte("transactional-secret"),
		func(reference Reference) error {
			if len(vault.values) != 0 {
				t.Fatal("vault mutation happened before cleanup intent")
			}
			staged = reference
			return nil
		},
	)
	if err != nil || reference != staged || len(vault.values) != 1 {
		t.Fatalf("PutNew() = %q staged=%q values=%d err=%v", reference, staged, len(vault.values), err)
	}

	vault = newMemoryVault()
	store, _ = newStore(vault, bytes.NewReader(bytes.Repeat([]byte{0x53}, 64)))
	if _, err = store.PutNew(
		context.Background(),
		[]byte("must-not-exist"),
		func(Reference) error { return context.Canceled },
	); !errors.Is(err, context.Canceled) || len(vault.values) != 0 {
		t.Fatalf("PutNew(failed intent) error=%v values=%d", err, len(vault.values))
	}
}

func TestFailedUpdateAndDeletePreserveOldSecret(t *testing.T) {
	vault := newMemoryVault()
	store, _ := newStore(vault, bytes.NewReader(bytes.Repeat([]byte{0x43}, 64)))
	reference, err := store.Put(context.Background(), "", []byte("old-secret"))
	if err != nil {
		t.Fatal(err)
	}
	vault.updateError = ErrPermission
	if _, err = store.Put(context.Background(), reference, []byte("new-secret")); !errors.Is(err, ErrPermission) {
		t.Fatalf("Put(failed update) error = %v", err)
	}
	vault.updateError = nil
	secret, err := store.Get(context.Background(), reference)
	if err != nil || string(secret) != "old-secret" {
		t.Fatalf("old secret = %q, %v", secret, err)
	}
	overwrite(secret)
	vault.deleteError = ErrCanceled
	if err = store.Delete(context.Background(), reference); !errors.Is(err, ErrCanceled) {
		t.Fatalf("Delete(failed) error = %v", err)
	}
	vault.deleteError = nil
	secret, err = store.Get(context.Background(), reference)
	if err != nil || string(secret) != "old-secret" {
		t.Fatalf("secret after failed delete = %q, %v", secret, err)
	}
	overwrite(secret)
}

func TestDuplicateReferenceRetriesAndConcurrentUpdatesSerialize(t *testing.T) {
	vault := newMemoryVault()
	firstReference := Reference(referencePrefix + stringsRepeat("44", referenceRandomBytes))
	vault.values[accountName(firstReference)] = []byte("collision")
	random := append(bytes.Repeat([]byte{0x44}, referenceRandomBytes), bytes.Repeat([]byte{0x45}, referenceRandomBytes)...)
	store, _ := newStore(vault, bytes.NewReader(random))
	reference, err := store.Put(context.Background(), "", []byte("created"))
	if err != nil || reference == firstReference {
		t.Fatalf("Put(collision) = %q, %v", reference, err)
	}

	vault.updateEntered = make(chan struct{}, 1)
	vault.updateRelease = make(chan struct{})
	var waitGroup sync.WaitGroup
	results := make(chan error, 2)
	for _, value := range []string{"one", "two"} {
		waitGroup.Add(1)
		go func(secret string) {
			defer waitGroup.Done()
			_, updateErr := store.Put(context.Background(), reference, []byte(secret))
			results <- updateErr
		}(value)
	}
	<-vault.updateEntered
	select {
	case <-vault.updateEntered:
		t.Fatal("concurrent update entered vault before the first completed")
	default:
	}
	close(vault.updateRelease)
	waitGroup.Wait()
	close(results)
	for result := range results {
		if result != nil {
			t.Fatal(result)
		}
	}
}

func TestRejectsInvalidReferencesSecretsMetadataAndContext(t *testing.T) {
	vault := newMemoryVault()
	store, _ := newStore(vault, bytes.NewReader(bytes.Repeat([]byte{0x46}, 64)))
	if _, err := store.Put(context.Background(), "", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Put(empty) error = %v", err)
	}
	if _, err := store.Put(
		context.Background(),
		"",
		bytes.Repeat([]byte{'x'}, maximumSecretBytes+1),
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Put(oversized) error = %v", err)
	}
	if _, err := store.Put(context.Background(), Reference("upstream-refresh-token"), []byte("x")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Put(owned auth reference) error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Put(canceled, "", []byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put(canceled) error = %v", err)
	}
	vault.values["malformed-account"] = []byte("secret")
	if _, err := store.ListMetadata(context.Background()); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("ListMetadata(malformed) error = %v", err)
	}
}

func TestPlatformFailuresAreStableAndNeverExposeSecretBytes(t *testing.T) {
	canary := "PRIVATE_SECRET_ERROR_CANARY"
	knownFailures := []error{ErrLocked, ErrPermission, ErrCanceled, ErrUnavailable}
	for _, failure := range knownFailures {
		vault := newMemoryVault()
		store, _ := newStore(vault, bytes.NewReader(bytes.Repeat([]byte{0x47}, 64)))
		reference, err := store.Put(context.Background(), "", []byte("old-secret"))
		if err != nil {
			t.Fatal(err)
		}
		vault.updateError = failure
		if _, err = store.Put(context.Background(), reference, []byte(canary)); !errors.Is(err, failure) || strings.Contains(err.Error(), canary) {
			t.Fatalf("Put(%v) error = %v", failure, err)
		}
	}
	vault := newMemoryVault()
	store, _ := newStore(vault, bytes.NewReader(bytes.Repeat([]byte{0x48}, 64)))
	reference, err := store.Put(context.Background(), "", []byte("old-secret"))
	if err != nil {
		t.Fatal(err)
	}
	vault.updateError = errors.New(canary)
	if _, err = store.Put(context.Background(), reference, []byte(canary)); !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), canary) {
		t.Fatalf("Put(unknown platform error) = %v", err)
	}
}

func TestCreateFailureAndExhaustedDuplicateIDsLeaveNoOrphanReference(t *testing.T) {
	vault := newMemoryVault()
	vault.createError = ErrPermission
	store, _ := newStore(vault, bytes.NewReader(bytes.Repeat([]byte{0x49}, 64)))
	if _, err := store.Put(context.Background(), "", []byte("secret")); !errors.Is(err, ErrPermission) || len(vault.values) != 0 {
		t.Fatalf("Put(failed create) error = %v, values = %d", err, len(vault.values))
	}

	vault = newMemoryVault()
	collision := Reference(referencePrefix + strings.Repeat("4a", referenceRandomBytes))
	vault.values[accountName(collision)] = []byte("existing")
	store, _ = newStore(
		vault,
		bytes.NewReader(bytes.Repeat([]byte{0x4a}, referenceRandomBytes*maximumCreateAttempts)),
	)
	if _, err := store.Put(context.Background(), "", []byte("secret")); !errors.Is(err, ErrUnavailable) || len(vault.values) != 1 {
		t.Fatalf("Put(exhausted duplicate IDs) error = %v, values = %d", err, len(vault.values))
	}
}

type retainingVault struct {
	memoryVault
	retained []byte
}

func (vault *retainingVault) Create(ctx context.Context, account string, secret []byte) error {
	vault.retained = secret
	return vault.memoryVault.Create(ctx, account, secret)
}

func TestPutDoesNotRetainOrModifyCallerSecret(t *testing.T) {
	vault := &retainingVault{memoryVault: *newMemoryVault()}
	store, _ := newStore(vault, bytes.NewReader(bytes.Repeat([]byte{0x4b}, 64)))
	input := []byte("caller-owned-secret")
	want := append([]byte(nil), input...)
	if _, err := store.Put(context.Background(), "", input); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(input, want) {
		t.Fatal("Put modified caller-owned secret bytes")
	}
	for _, value := range vault.retained {
		if value != 0 {
			t.Fatal("Store retained an uncleared temporary secret copy")
		}
	}
}

func TestListThenDeleteSupportsIdempotentUninstallCleanup(t *testing.T) {
	vault := newMemoryVault()
	random := append(bytes.Repeat([]byte{0x4c}, 16), bytes.Repeat([]byte{0x4d}, 16)...)
	random = append(random, bytes.Repeat([]byte{0x4e}, 16)...)
	store, _ := newStore(vault, bytes.NewReader(random))
	for index := range 3 {
		secret := []byte{'s', byte('0' + index)}
		if _, err := store.Put(context.Background(), "", secret); err != nil {
			t.Fatal(err)
		}
	}
	metadata, err := store.ListMetadata(context.Background())
	if err != nil || len(metadata) != 3 {
		t.Fatalf("ListMetadata() = %+v, %v", metadata, err)
	}
	for _, item := range metadata {
		if err = store.Delete(context.Background(), item.Reference); err != nil {
			t.Fatal(err)
		}
		if err = store.Delete(context.Background(), item.Reference); err != nil {
			t.Fatal(err)
		}
	}
	metadata, err = store.ListMetadata(context.Background())
	if err != nil || len(metadata) != 0 {
		t.Fatalf("metadata after cleanup = %+v, %v", metadata, err)
	}
}

func stringsRepeat(value string, count int) string {
	return strings.Repeat(value, count)
}
