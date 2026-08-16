//go:build windows

package installation

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
	"golang.org/x/sys/windows"
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
	sid, err := currentWindowsUserSID()
	if err != nil {
		return errors.Join(ErrPlatformIdentity, err)
	}
	document, err := scheduledTaskDocument(spec, sid)
	if err != nil {
		return errors.Join(ErrPlatformDefinition, err)
	}
	definitionPath := filepath.Join(filepath.Dir(adapter.markerPath), "task-scheduler-definition.xml")
	if _, err = protectedfile.Replace(definitionPath, document); err != nil {
		return errors.Join(ErrPlatformDefinition, err)
	}
	clear(document)
	_, configureErr := runPlatformCommand(
		ctx, "schtasks.exe", "/Create", "/TN", adapter.taskName,
		"/XML", definitionPath, "/F",
	)
	removeErr := os.Remove(definitionPath)
	if configureErr != nil {
		return errors.Join(ErrPlatformRegister, configureErr, removeErr)
	}
	if removeErr != nil {
		_, _ = runPlatformCommand(ctx, "schtasks.exe", "/Delete", "/TN", adapter.taskName, "/F")
		return errors.Join(ErrPlatformDefinition, removeErr)
	}
	_, err = protectedfile.Replace(adapter.markerPath, []byte("v1\n"))
	if err != nil {
		_, _ = runPlatformCommand(ctx, "schtasks.exe", "/Delete", "/TN", adapter.taskName, "/F")
		return errors.Join(ErrPlatformMarker, err)
	}
	return nil
}

func currentWindowsUserSID() (string, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return "", ErrPlatform
	}
	return user.User.Sid.String(), nil
}

func scheduledTaskDocument(spec launchSpec, sid string) ([]byte, error) {
	if spec.Executable == "" || spec.DataDirectory == "" || spec.DeviceHubAddress == "" || sid == "" {
		return nil, ErrPlatform
	}
	arguments := windowsCommandLine([]string{
		"--data-directory", spec.DataDirectory,
		"--device-hub-address", spec.DeviceHubAddress,
	})
	var document bytes.Buffer
	document.WriteString(`<?xml version="1.0" encoding="UTF-16"?>`)
	document.WriteString(`<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">`)
	document.WriteString(`<RegistrationInfo><Description>S3 RLCD Deck Companion per-user startup</Description></RegistrationInfo>`)
	document.WriteString(`<Triggers><LogonTrigger><Enabled>true</Enabled></LogonTrigger></Triggers>`)
	document.WriteString(`<Principals><Principal id="CurrentUser"><UserId>`)
	if err := xml.EscapeText(&document, []byte(sid)); err != nil {
		return nil, err
	}
	document.WriteString(`</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>`)
	document.WriteString(`<Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>`)
	document.WriteString(`<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>`)
	document.WriteString(`<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>`)
	document.WriteString(`<AllowHardTerminate>true</AllowHardTerminate><StartWhenAvailable>true</StartWhenAvailable>`)
	document.WriteString(`<RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>`)
	document.WriteString(`<IdleSettings><StopOnIdleEnd>false</StopOnIdleEnd><RestartOnIdle>false</RestartOnIdle></IdleSettings>`)
	document.WriteString(`<AllowStartOnDemand>true</AllowStartOnDemand><Enabled>false</Enabled>`)
	document.WriteString(`<Hidden>false</Hidden><RunOnlyIfIdle>false</RunOnlyIfIdle><WakeToRun>false</WakeToRun>`)
	document.WriteString(`<ExecutionTimeLimit>PT0S</ExecutionTimeLimit><Priority>7</Priority></Settings>`)
	document.WriteString(`<Actions Context="CurrentUser"><Exec><Command>`)
	if err := xml.EscapeText(&document, []byte(spec.Executable)); err != nil {
		return nil, err
	}
	document.WriteString(`</Command><Arguments>`)
	if err := xml.EscapeText(&document, []byte(arguments)); err != nil {
		return nil, err
	}
	document.WriteString(`</Arguments></Exec></Actions></Task>`)
	return utf16LEDocument(document.String()), nil
}

func utf16LEDocument(value string) []byte {
	encoded := utf16.Encode([]rune(value))
	document := make([]byte, 2+len(encoded)*2)
	document[0] = 0xff
	document[1] = 0xfe
	for index, unit := range encoded {
		binary.LittleEndian.PutUint16(document[2+index*2:], unit)
	}
	return document
}

func (adapter *windowsAdapter) SetEnabled(ctx context.Context, enabled bool) error {
	action := "/DISABLE"
	if enabled {
		action = "/ENABLE"
	}
	_, err := runPlatformCommand(ctx, "schtasks.exe", "/Change", "/TN", adapter.taskName, action)
	if err != nil {
		return errors.Join(ErrPlatformEnable, err)
	}
	return nil
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
