package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestVersionFlagPrintsBuildIdentity(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run([]string{"--version"}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("run() exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if stdout.String() != "s3deck-companion 0.1.0-dev (commit unknown)\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunFailsClosedWithMalformedPersistedManagementToken(t *testing.T) {
	t.Setenv(managementTokenEnvironment, "not-a-valid-token")
	directory := t.TempDir()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run([]string{"--headless", "--data-directory", directory}, &stdout, &stderr)
	if exitCode != 2 {
		t.Fatalf("run() exit code = %d, want 2", exitCode)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("management admin token")) {
		t.Fatalf("stderr = %q, want fail-closed management token error", stderr.String())
	}
}

func TestMalformedStructuredProviderConfigurationDoesNotDisableCompanion(t *testing.T) {
	t.Setenv(managementTokenEnvironment, "not-a-valid-token")
	directory := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(directory, "structured-providers.json"),
		[]byte(`{"schema_version":1,"secret":"PRIVATE_CONFIG_CANARY"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"--headless", "--data-directory", directory}, &stdout, &stderr)
	if exitCode != 2 || !bytes.Contains(stderr.Bytes(), []byte("management admin token")) {
		t.Fatalf("run() exit=%d stderr=%q", exitCode, stderr.String())
	}
	if !bytes.Contains(stderr.Bytes(), []byte("structured Provider configuration is unavailable")) ||
		bytes.Contains(stderr.Bytes(), []byte("PRIVATE_CONFIG_CANARY")) {
		t.Fatalf("structured Provider degradation was not isolated/redacted: %q", stderr.String())
	}
}

func TestCorruptProviderHistoryDoesNotDisableCompanionOrLeakContents(t *testing.T) {
	t.Setenv(managementTokenEnvironment, "not-a-valid-token")
	directory := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(directory, "provider-history.sqlite3"),
		[]byte("PRIVATE_HISTORY_CANARY is not sqlite"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"--headless", "--data-directory", directory}, &stdout, &stderr)
	if exitCode != 2 || !bytes.Contains(stderr.Bytes(), []byte("management admin token")) {
		t.Fatalf("run() exit=%d stderr=%q", exitCode, stderr.String())
	}
	if !bytes.Contains(stderr.Bytes(), []byte("Provider history is unavailable")) ||
		bytes.Contains(stderr.Bytes(), []byte("PRIVATE_HISTORY_CANARY")) {
		t.Fatalf("Provider history degradation was not isolated/redacted: %q", stderr.String())
	}
}
