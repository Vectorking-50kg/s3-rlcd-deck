package main

import (
	"bytes"
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
