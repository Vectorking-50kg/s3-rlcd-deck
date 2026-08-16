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

const (
	installationInstanceReleaseTimeout = 5 * time.Second
	installationInstanceRetryInterval  = 100 * time.Millisecond
)

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
		maintenance, instance, err = acquireInstallationFences(config.DataDirectory)
		if err != nil {
			fmt.Fprintln(stderr, "quit the running Companion before changing its installation")
			return 2
		}
	}
	defer func() {
		if instance != nil {
			_ = instance.Close()
		}
		if maintenance != nil {
			_ = maintenance.Close()
		}
	}()
	managerConfig := installation.Config{
		RootDirectory: root, DataDirectory: config.DataDirectory,
	}
	var manager *installation.Manager
	if config.Install {
		manager, err = installation.Open(managerConfig)
	} else {
		manager, err = installation.OpenWithoutRecovery(managerConfig)
		if errors.Is(err, installation.ErrRecoveryRequired) {
			// Recovery may restore the live database and configuration. Escalate
			// to both fences only for an actual interrupted transaction; normal
			// status and login-startup changes remain usable while the app runs.
			maintenance, instance, err = acquireInstallationFences(config.DataDirectory)
			if err == nil {
				manager, err = installation.Open(managerConfig)
			}
		}
	}
	if err != nil {
		if errors.Is(err, desktop.ErrAlreadyRunning) ||
			errors.Is(err, installation.ErrRecoveryRequired) {
			fmt.Fprintln(stderr, "quit the running Companion before recovering its installation")
			return 2
		}
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
			fmt.Fprintln(stderr, installationApplyFailureMessage(executableErr))
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
			fmt.Fprintln(stderr, installationStatusFailureMessage(statusErr))
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

func installationStatusFailureMessage(err error) string {
	switch {
	case errors.Is(err, installation.ErrPlatformQuery):
		return "Companion login startup status query failed"
	case errors.Is(err, installation.ErrPlatformDecode):
		return "Companion login startup status decode failed"
	case errors.Is(err, installation.ErrPlatformMarker):
		return "Companion login startup ownership marker is invalid"
	default:
		return "Companion installation status is unavailable"
	}
}

func installationApplyFailureMessage(err error) string {
	switch {
	case errors.Is(err, installation.ErrPlatformIdentity):
		return "Companion login startup current-user identity failed and prior state was restored"
	case errors.Is(err, installation.ErrPlatformDefinition):
		return "Companion login startup definition failed and prior state was restored"
	case errors.Is(err, installation.ErrPlatformRegister):
		return "Companion login startup platform registration failed and prior state was restored"
	case errors.Is(err, installation.ErrPlatformMarker):
		return "Companion login startup ownership marker failed and prior state was restored"
	case errors.Is(err, installation.ErrPlatformEnable):
		return "Companion login startup enablement failed and prior state was restored"
	case errors.Is(err, installation.ErrPlatform):
		return "Companion login startup registration failed and prior state was restored"
	case errors.Is(err, installation.ErrMigration):
		return "Companion data migration failed and prior state was restored"
	default:
		return "Companion installation failed and prior state was restored"
	}
}

func acquireInstallationFences(dataDirectory string) (
	*installation.Maintenance,
	*desktop.SingleInstance,
	error,
) {
	maintenance, err := installation.AcquireMaintenance(dataDirectory)
	if err != nil {
		return nil, nil, err
	}
	deadline := time.Now().Add(installationInstanceReleaseTimeout)
	for {
		instance, instanceErr := desktop.AcquireSingleInstance(dataDirectory)
		if instanceErr == nil {
			return maintenance, instance, nil
		}
		if !errors.Is(instanceErr, desktop.ErrAlreadyRunning) || !time.Now().Before(deadline) {
			_ = maintenance.Close()
			return nil, nil, instanceErr
		}
		time.Sleep(installationInstanceRetryInterval)
	}
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
