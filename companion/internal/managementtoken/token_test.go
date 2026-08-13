package managementtoken

import (
	"bytes"
	"errors"
	"testing"
)

type memoryStore struct {
	secret string
	gets   int
	sets   int
	err    error
}

func (store *memoryStore) Get(string, string) (string, error) {
	store.gets++
	if store.err != nil {
		return "", store.err
	}
	if store.secret == "" {
		return "", ErrSecretNotFound
	}
	return store.secret, nil
}

func (store *memoryStore) Set(_ string, _ string, secret string) error {
	store.sets++
	store.secret = secret
	return nil
}

func TestLoadOrCreateStoresAndReusesCredentialVaultSecret(t *testing.T) {
	store := &memoryStore{}
	random := bytes.NewReader(bytes.Repeat([]byte{0x42}, 64))
	first, err := loadOrCreate(store, "reference-id", random)
	if err != nil {
		t.Fatalf("loadOrCreate(first) error = %v", err)
	}
	second, err := loadOrCreate(store, "reference-id", random)
	if err != nil {
		t.Fatalf("loadOrCreate(second) error = %v", err)
	}
	if first != second || store.sets != 1 || store.gets != 2 {
		t.Fatalf("first=%q second=%q gets=%d sets=%d", first, second, store.gets, store.sets)
	}
	if validate(first) != nil {
		t.Fatalf("generated token is malformed: %q", first)
	}
}

func TestLoadOrCreateFailsClosedOnStoreAndMalformedSecret(t *testing.T) {
	store := &memoryStore{err: errors.New("vault unavailable")}
	if _, err := loadOrCreate(store, "reference-id", bytes.NewReader(make([]byte, 32))); err == nil {
		t.Fatal("loadOrCreate accepted unavailable credential vault")
	}
	store.err = nil
	store.secret = "not-a-token"
	if _, err := loadOrCreate(store, "reference-id", bytes.NewReader(make([]byte, 32))); err == nil {
		t.Fatal("loadOrCreate accepted malformed credential")
	}
}
