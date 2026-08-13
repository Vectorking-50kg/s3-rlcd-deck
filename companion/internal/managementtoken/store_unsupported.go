//go:build !darwin && !windows

package managementtoken

import "errors"

type unsupportedSecretStore struct{}

func platformSecretStore() secretStore { return unsupportedSecretStore{} }

func (unsupportedSecretStore) Get(string, string) (string, error) {
	return "", errors.New("platform credential store is unsupported")
}

func (unsupportedSecretStore) Set(string, string, string) error {
	return errors.New("platform credential store is unsupported")
}
