package managementtoken

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

const secretService = "S3 RLCD Deck Companion"

var ErrSecretNotFound = errors.New("management secret not found")

type secretStore interface {
	Get(service string, account string) (string, error)
	Set(service string, account string, secret string) error
}

// LoadOrCreate returns the process-independent management secret from the
// platform credential vault. The data directory contributes only a stable,
// non-secret reference ID; the bearer token never enters a config file.
func LoadOrCreate(dataDirectory string) (string, error) {
	if dataDirectory == "" {
		return "", errors.New("management token data directory is required")
	}
	absolute, err := filepath.Abs(filepath.Clean(dataDirectory))
	if err != nil {
		return "", fmt.Errorf("resolve management token reference: %w", err)
	}
	digest := sha256.Sum256([]byte(absolute))
	account := "management-" + hex.EncodeToString(digest[:16])
	return loadOrCreate(platformSecretStore(), account, rand.Reader)
}

func loadOrCreate(store secretStore, account string, random io.Reader) (string, error) {
	if store == nil || account == "" || random == nil {
		return "", errors.New("management token store, account, and random source are required")
	}
	token, err := store.Get(secretService, account)
	if err == nil {
		if validate(token) != nil {
			return "", errors.New("management token in credential store is malformed")
		}
		return token, nil
	}
	if !errors.Is(err, ErrSecretNotFound) {
		return "", fmt.Errorf("read management token from credential store: %w", err)
	}
	contents := make([]byte, 32)
	if _, err = io.ReadFull(random, contents); err != nil {
		return "", fmt.Errorf("generate management token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(contents)
	if err = store.Set(secretService, account, token); err != nil {
		return "", fmt.Errorf("write management token to credential store: %w", err)
	}
	return token, nil
}

func validate(token string) error {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 || strings.ContainsAny(token, "\r\n") {
		return errors.New("invalid management token")
	}
	return nil
}
