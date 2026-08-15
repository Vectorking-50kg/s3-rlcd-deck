//go:build windows

package installation

import (
	"context"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
)

const scheduledTaskNamePrefix = `S3 RLCD Deck Companion`

type windowsAdapter struct {
	taskName   string
	markerPath string
}

func newPlatformAdapter(root string) platformAdapter {
	return &windowsAdapter{
		taskName:   scheduledTaskNamePrefix + " " + registrationIdentifier(root),
		markerPath: filepath.Join(root, "task-scheduler-registration"),
	}
}

func DefaultRootDirectory() (string, error) {
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		return "", ErrUnavailable
	}
	return filepath.Join(root, "S3 RLCD Deck Companion"), nil
}

func (*windowsAdapter) Name() string { return "task_scheduler" }

func (adapter *windowsAdapter) Configure(ctx context.Context, spec launchSpec) error {
	command := windowsCommandLine([]string{
		spec.Executable, "--data-directory", spec.DataDirectory,
		"--device-hub-address", spec.DeviceHubAddress,
	})
	_, err := runPlatformCommand(
		ctx, "schtasks.exe", "/Create", "/SC", "ONLOGON", "/TN", adapter.taskName,
		"/TR", command, "/RL", "LIMITED", "/IT", "/F",
	)
	if err == nil {
		_, err = runPlatformCommand(ctx, "schtasks.exe", "/Change", "/TN", adapter.taskName, "/DISABLE")
	}
	if err == nil {
		_, err = protectedfile.Replace(adapter.markerPath, []byte("v1\n"))
		if err != nil {
			_, _ = runPlatformCommand(ctx, "schtasks.exe", "/Delete", "/TN", adapter.taskName, "/F")
		}
	}
	return err
}

func (adapter *windowsAdapter) SetEnabled(ctx context.Context, enabled bool) error {
	action := "/DISABLE"
	if enabled {
		action = "/ENABLE"
	}
	_, err := runPlatformCommand(ctx, "schtasks.exe", "/Change", "/TN", adapter.taskName, action)
	return err
}

func (adapter *windowsAdapter) Remove(ctx context.Context) error {
	marker, markerErr := ordinaryWindowsMarker(adapter.markerPath)
	if markerErr != nil {
		return markerErr
	}
	_, _ = runPlatformCommand(ctx, "schtasks.exe", "/End", "/TN", adapter.taskName)
	_, err := runPlatformCommand(ctx, "schtasks.exe", "/Delete", "/TN", adapter.taskName, "/F")
	if err != nil {
		if marker {
			return err
		}
		// Without an owned marker, a successful query proves an orphan that
		// still needs deletion. A failed query is the idempotent absent case.
		if _, queryErr := runPlatformCommand(ctx, "schtasks.exe", "/Query", "/TN", adapter.taskName); queryErr == nil {
			return err
		}
	}
	if err = os.Remove(adapter.markerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (adapter *windowsAdapter) Status(ctx context.Context) (platformStatus, error) {
	marker, markerErr := ordinaryWindowsMarker(adapter.markerPath)
	if markerErr != nil {
		return platformStatus{}, markerErr
	}
	document, err := runPlatformCommand(
		ctx, "schtasks.exe", "/Query", "/TN", adapter.taskName, "/XML",
	)
	if err != nil {
		if marker {
			return platformStatus{}, err
		}
		return platformStatus{}, nil
	}
	enabled, parseErr := scheduledTaskEnabled(document)
	if parseErr != nil {
		return platformStatus{}, parseErr
	}
	return platformStatus{
		Installed: true,
		Enabled:   enabled,
	}, nil
}

func ordinaryWindowsMarker(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		protectedfile.VerifyPrivate(path) != nil {
		return false, ErrPlatform
	}
	return true, nil
}

func scheduledTaskEnabled(document []byte) (bool, error) {
	var task struct {
		Settings struct {
			Enabled *bool `xml:"Enabled"`
		} `xml:"Settings"`
	}
	if err := xml.Unmarshal(document, &task); err != nil || task.Settings.Enabled == nil {
		return false, ErrPlatform
	}
	return *task.Settings.Enabled, nil
}

func windowsCommandLine(arguments []string) string {
	quoted := make([]string, len(arguments))
	for index, argument := range arguments {
		quoted[index] = windowsQuoteArgument(argument)
	}
	return strings.Join(quoted, " ")
}

func windowsQuoteArgument(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\n\v\"") {
		return value
	}
	var result strings.Builder
	result.WriteByte('"')
	backslashes := 0
	for _, character := range value {
		if character == '\\' {
			backslashes++
			continue
		}
		if character == '"' {
			result.WriteString(strings.Repeat("\\", backslashes*2+1))
			result.WriteRune(character)
			backslashes = 0
			continue
		}
		result.WriteString(strings.Repeat("\\", backslashes))
		backslashes = 0
		result.WriteRune(character)
	}
	result.WriteString(strings.Repeat("\\", backslashes*2))
	result.WriteByte('"')
	return result.String()
}
