package main

import (
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
