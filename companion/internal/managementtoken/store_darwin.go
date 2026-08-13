//go:build darwin

package managementtoken

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type darwinSecretStore struct{}

func platformSecretStore() secretStore { return darwinSecretStore{} }

func (darwinSecretStore) Get(service string, account string) (string, error) {
	command := exec.Command(
		"/usr/bin/security", "find-generic-password",
		"-s", service, "-a", account, "-w",
	)
	output, err := command.Output()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 44 {
			return "", ErrSecretNotFound
		}
		return "", fmt.Errorf("read macOS Keychain item: %w", err)
	}
	return strings.TrimSuffix(string(output), "\n"), nil
}

func (darwinSecretStore) Set(service string, account string, secret string) error {
	command := exec.Command(
		"/usr/bin/security", "add-generic-password", "-U",
		"-s", service, "-a", account, "-w",
	)
	command.Stdin = strings.NewReader(secret + "\n" + secret + "\n")
	if output, err := command.CombinedOutput(); err != nil {
		_ = output // Keychain output is deliberately never returned or logged.
		return fmt.Errorf("write macOS Keychain item: %w", err)
	}
	return nil
}
