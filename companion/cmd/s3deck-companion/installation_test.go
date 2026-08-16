package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/desktop"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/installation"
)

func TestRuntimeWaitsForBoundedInstallationMaintenance(t *testing.T) {
	directory := t.TempDir()
	maintenance, err := installation.AcquireMaintenance(directory)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := desktop.AcquireSingleInstance(directory)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = instance.Close()
		_ = maintenance.Close()
		close(released)
	}()
	acquired, err := acquireRuntimeInstance(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer acquired.Close()
	<-released
}

func TestRuntimeDoesNotWaitForRealSecondInstance(t *testing.T) {
	directory := t.TempDir()
	instance, err := desktop.AcquireSingleInstance(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	started := time.Now()
	if _, err = acquireRuntimeInstance(directory); err == nil {
		t.Fatal("second runtime instance was accepted")
	}
	if time.Since(started) > time.Second {
		t.Fatal("real second instance was mistaken for installation maintenance")
	}
}

func TestInstallationWaitsForStoppingRuntime(t *testing.T) {
	directory := t.TempDir()
	instance, err := desktop.AcquireSingleInstance(directory)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = instance.Close()
		close(released)
	}()
	maintenance, acquired, err := acquireInstallationFences(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer maintenance.Close()
	defer acquired.Close()
	<-released
}

func TestInstallationFailureMessagesAreStableAndSpecific(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{installation.ErrPlatformIdentity, "current-user identity failed"},
		{installation.ErrPlatformDefinition, "startup definition failed"},
		{installation.ErrPlatformRegister, "platform registration failed"},
		{installation.ErrPlatformMarker, "ownership marker failed"},
		{installation.ErrPlatformEnable, "startup enablement failed"},
		{installation.ErrPlatform, "login startup registration failed"},
		{installation.ErrMigration, "data migration failed"},
		{os.ErrPermission, "installation failed"},
	} {
		message := installationApplyFailureMessage(test.err)
		if !strings.Contains(message, test.want) || !strings.Contains(message, "prior state was restored") {
			t.Fatalf("failure message for %v = %q", test.err, message)
		}
	}
}

func TestDeviceHubOverrideBelongsOnlyToInstall(t *testing.T) {
	if !onlyInstallationFlags(installationCommandConfig{
		Install: true, ExplicitFlags: map[string]bool{
			"install": true, "device-hub-address": true,
		},
	}) {
		t.Fatal("install rejected its explicit Device Hub listener")
	}
	if onlyInstallationFlags(installationCommandConfig{
		Status: true, ExplicitFlags: map[string]bool{
			"installation-status": true, "device-hub-address": true,
		},
	}) {
		t.Fatal("status accepted an unrelated Device Hub listener")
	}
}

func TestInterruptedRecoveryNeverMutatesARunningCompanion(t *testing.T) {
	directory := t.TempDir()
	root := filepath.Join(directory, "installation")
	data := filepath.Join(directory, "data")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(root, "installation-journal.json")
	if err := os.WriteFile(journal, []byte("durable interrupted transaction"), 0o600); err != nil {
		t.Fatal(err)
	}
	instance, err := desktop.AcquireSingleInstance(data)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	result := runInstallationCommand(installationCommandConfig{
		Status: true, Root: root, DataDirectory: data,
		ExplicitFlags: map[string]bool{"installation-status": true},
	}, &stdout, &stderr)
	if result != 2 || !strings.Contains(stderr.String(), "quit the running Companion") {
		t.Fatalf("status during interrupted live transaction = %d, %q", result, stderr.String())
	}
	contents, err := os.ReadFile(journal)
	if err != nil || string(contents) != "durable interrupted transaction" {
		t.Fatalf("live journal was mutated: %q, %v", contents, err)
	}
}
