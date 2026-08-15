package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/desktop"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/installation"
)

type installationCommandConfig struct {
	Install          bool
	Uninstall        bool
	Enable           bool
	Disable          bool
	Status           bool
	Root             string
	DataDirectory    string
	DeviceHubAddress string
	Version          string
	Commit           string
	ExplicitFlags    map[string]bool
}

func lifecycleCommandRequested(values ...bool) bool {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count != 0
}

func installedDeviceHubAddress(value string, explicit map[string]bool) string {
	if explicit["device-hub-address"] {
		return value
	}
	return "127.0.0.1:7780"
}

func runInstallationCommand(
	config installationCommandConfig,
	stdout io.Writer,
	stderr io.Writer,
) int {
	if exactlyOneLifecycleCommand(config) == false || !onlyInstallationFlags(config) ||
		(config.Install && config.Commit == "unknown") {
		fmt.Fprintln(stderr, "installation commands are exclusive and accept only installation options")
		return 2
	}
	root := config.Root
	if root == "" {
		var err error
		root, err = installation.DefaultRootDirectory()
		if err != nil {
			fmt.Fprintln(stderr, "per-user installation is unavailable")
			return 2
		}
	}
	var maintenance *installation.Maintenance
	var instance *desktop.SingleInstance
	var err error
	if config.Install {
		// Hold both fences through activation. A new login process recognizes the
		// maintenance fence and waits; an unrelated running process makes this
		// command fail without being killed.
		maintenance, err = installation.AcquireMaintenance(config.DataDirectory)
		if err == nil {
			instance, err = desktop.AcquireSingleInstance(config.DataDirectory)
		}
		if err != nil {
			if maintenance != nil {
				_ = maintenance.Close()
			}
			fmt.Fprintln(stderr, "quit the running Companion before changing its installation")
			return 2
		}
		defer maintenance.Close()
		defer instance.Close()
	}
	manager, err := installation.Open(installation.Config{
		RootDirectory: root, DataDirectory: config.DataDirectory,
	})
	if err != nil {
		fmt.Fprintln(stderr, "cannot open the per-user installation")
		return 2
	}
	defer manager.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	switch {
	case config.Install:
		executable, executableErr := os.Executable()
		if executableErr == nil {
			_, executableErr = manager.Apply(ctx, installation.Request{
				SourceExecutable: executable, Version: config.Version, Commit: config.Commit,
				DeviceHubAddress: config.DeviceHubAddress,
			})
		}
		if executableErr != nil {
			fmt.Fprintln(stderr, "Companion installation failed and prior state was restored")
			return 1
		}
		fmt.Fprintln(stdout, "Companion installed for login startup")
	case config.Uninstall:
		if err = manager.Uninstall(ctx); err != nil {
			fmt.Fprintln(stderr, "Companion uninstall failed")
			return 1
		}
		fmt.Fprintln(stdout, "Companion login startup removed; user data retained")
	case config.Enable:
		if err = manager.SetEnabled(ctx, true); err != nil {
			fmt.Fprintln(stderr, "Companion login startup could not be enabled")
			return 1
		}
		fmt.Fprintln(stdout, "Companion login startup enabled")
	case config.Disable:
		if err = manager.SetEnabled(ctx, false); err != nil {
			fmt.Fprintln(stderr, "Companion login startup could not be disabled")
			return 1
		}
		fmt.Fprintln(stdout, "Companion login startup disabled")
	case config.Status:
		status, statusErr := manager.Status(ctx)
		if statusErr != nil {
			fmt.Fprintln(stderr, "Companion installation status is unavailable")
			return 1
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		if err = encoder.Encode(struct {
			Installed       bool   `json:"installed"`
			Enabled         bool   `json:"enabled"`
			Version         string `json:"version,omitempty"`
			Commit          string `json:"commit,omitempty"`
			PreviousVersion string `json:"previous_version,omitempty"`
			Platform        string `json:"platform"`
		}{
			status.Installed, status.Enabled, status.Version, status.Commit,
			status.PreviousVersion, status.Platform,
		}); err != nil {
			return 1
		}
	}
	return 0
}

func exactlyOneLifecycleCommand(config installationCommandConfig) bool {
	count := 0
	for _, value := range []bool{config.Install, config.Uninstall, config.Enable, config.Disable, config.Status} {
		if value {
			count++
		}
	}
	return count == 1
}

func onlyInstallationFlags(config installationCommandConfig) bool {
	for name := range config.ExplicitFlags {
		switch name {
		case "install", "uninstall", "enable-login", "disable-login", "installation-status",
			"installation-root", "data-directory", "device-hub-address":
		default:
			return false
		}
	}
	return config.Install || !config.ExplicitFlags["device-hub-address"]
}

func acquireRuntimeInstance(dataDirectory string) (*desktop.SingleInstance, error) {
	instance, err := desktop.AcquireSingleInstance(dataDirectory)
	if err == nil || !errors.Is(err, desktop.ErrAlreadyRunning) {
		return instance, err
	}
	active, probeErr := installation.MaintenanceActive(dataDirectory)
	if probeErr != nil || !active {
		return nil, errors.Join(err, probeErr)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		instance, err = desktop.AcquireSingleInstance(dataDirectory)
		if err == nil {
			return instance, nil
		}
		if !errors.Is(err, desktop.ErrAlreadyRunning) {
			return nil, err
		}
		active, probeErr = installation.MaintenanceActive(dataDirectory)
		if probeErr != nil || !active {
			return nil, errors.Join(err, probeErr)
		}
	}
	return nil, fmt.Errorf("installation maintenance did not finish: %w", desktop.ErrAlreadyRunning)
}
