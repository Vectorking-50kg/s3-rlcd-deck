package codexappserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"
)

func TestAppServerEnvironmentUsesAnExplicitNonSecretAllowlist(t *testing.T) {
	environment := appServerEnvironment([]string{
		"HOME=/Users/example",
		"PATH=/usr/bin:/bin",
		"S3DECK_MANAGEMENT_TOKEN=must-not-cross",
		"OTHER_PROVIDER_API_KEY=must-not-cross",
		"DATABASE_PASSWORD=must-not-cross",
		"TMPDIR=/tmp/example",
	})
	sort.Strings(environment)
	want := []string{
		"HOME=/Users/example",
		"PATH=/usr/bin:/bin",
		"TMPDIR=/tmp/example",
	}
	if len(environment) != len(want) {
		t.Fatalf("sanitized environment = %v, want %v", environment, want)
	}
	for index := range want {
		if environment[index] != want[index] {
			t.Fatalf("sanitized environment = %v, want %v", environment, want)
		}
	}
}

func TestProcessConnectorDoesNotExposeCompanionOrProviderSecrets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the allowlist contract is platform-neutral; this helper executable uses POSIX sh")
	}
	directory := t.TempDir()
	helper := filepath.Join(directory, "app-server-environment-helper")
	content := []byte(`#!/bin/sh
if [ -n "${S3DECK_MANAGEMENT_TOKEN+x}" ]; then s3deck=true; else s3deck=false; fi
if [ -n "${OTHER_PROVIDER_API_KEY+x}" ]; then provider=true; else provider=false; fi
printf '{"s3deck":%s,"provider":%s}\n' "$s3deck" "$provider"
IFS= read -r ignored || true
`)
	if err := os.WriteFile(helper, content, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("S3DECK_MANAGEMENT_TOKEN", "management-secret-must-not-cross")
	t.Setenv("OTHER_PROVIDER_API_KEY", "provider-secret-must-not-cross")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, err := (ProcessConnector{Binary: helper}).Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = connection.Close()
		}
	}()
	document, err := connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var observed struct {
		S3Deck   bool `json:"s3deck"`
		Provider bool `json:"provider"`
	}
	if err = json.Unmarshal(document, &observed); err != nil {
		t.Fatal(err)
	}
	if observed.S3Deck || observed.Provider {
		t.Fatalf("App Server inherited a private environment variable: %+v", observed)
	}
	if err = connection.Close(); err != nil {
		t.Fatalf("close helper App Server: %v", err)
	}
	closed = true
}
