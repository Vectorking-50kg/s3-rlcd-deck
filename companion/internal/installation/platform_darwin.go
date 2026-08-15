//go:build darwin

package installation

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
)

const launchAgentLabelPrefix = "com.vectorking.s3-rlcd-deck-companion"

type darwinAdapter struct {
	label        string
	plistPath    string
	templatePath string
	service      string
}

func newPlatformAdapter(root string) platformAdapter {
	home, _ := os.UserHomeDir()
	domain := "gui/" + strconv.Itoa(os.Getuid())
	label := launchAgentLabelPrefix + "." + registrationIdentifier(root)
	return &darwinAdapter{
		label: label, plistPath: filepath.Join(home, "Library", "LaunchAgents", label+".plist"),
		templatePath: filepath.Join(root, "launchagent.plist"),
		service:      domain + "/" + label,
	}
}

func DefaultRootDirectory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", ErrUnavailable
	}
	return filepath.Join(home, "Library", "Application Support", "S3 RLCD Deck Companion"), nil
}

func (adapter *darwinAdapter) Name() string { return "launchagent" }

func (adapter *darwinAdapter) Configure(ctx context.Context, spec launchSpec) error {
	if adapter == nil || adapter.plistPath == "" {
		return ErrPlatform
	}
	parent := filepath.Dir(adapter.plistPath)
	if err := ensureOrdinaryDirectory(parent); err != nil {
		return err
	}
	document, err := launchAgentDocument(adapter.label, spec)
	if err != nil {
		return err
	}
	_, _ = runPlatformCommand(ctx, "/bin/launchctl", "bootout", adapter.service)
	if err = os.Remove(adapter.plistPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err = protectedfile.Replace(adapter.templatePath, document); err != nil {
		return err
	}
	return nil
}

func (adapter *darwinAdapter) SetEnabled(ctx context.Context, enabled bool) error {
	_ = ctx
	if enabled {
		document, err := protectedfile.Read(adapter.templatePath, 64<<10)
		if err != nil {
			return err
		}
		defer clear(document)
		if _, err = protectedfile.ReplaceFile(adapter.plistPath, document); err != nil {
			return err
		}
		return nil
	}
	if err := os.Remove(adapter.plistPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (adapter *darwinAdapter) Remove(ctx context.Context) error {
	_, _ = runPlatformCommand(ctx, "/bin/launchctl", "bootout", adapter.service)
	if err := os.Remove(adapter.plistPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(adapter.templatePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (adapter *darwinAdapter) Status(ctx context.Context) (platformStatus, error) {
	_ = ctx
	templateExists, err := ordinaryRegularFile(adapter.templatePath)
	if err != nil {
		return platformStatus{}, err
	}
	liveExists, err := ordinaryRegularFile(adapter.plistPath)
	if err != nil {
		return platformStatus{}, err
	}
	if !templateExists && !liveExists {
		return platformStatus{}, nil
	}
	if !templateExists && liveExists {
		return platformStatus{}, ErrPlatform
	}
	if liveExists {
		template, readErr := protectedfile.Read(adapter.templatePath, 64<<10)
		if readErr != nil {
			return platformStatus{}, readErr
		}
		defer clear(template)
		live, readErr := protectedfile.Read(adapter.plistPath, 64<<10)
		if readErr != nil {
			return platformStatus{}, readErr
		}
		defer clear(live)
		if !bytes.Equal(template, live) {
			return platformStatus{}, ErrPlatform
		}
	}
	return platformStatus{Installed: true, Enabled: liveExists}, nil
}

func ordinaryRegularFile(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		protectedfile.VerifyPrivate(path) != nil {
		return false, ErrPlatform
	}
	return true, nil
}

func launchAgentDocument(label string, spec launchSpec) ([]byte, error) {
	values := []string{spec.Executable, "--data-directory", spec.DataDirectory,
		"--device-hub-address", spec.DeviceHubAddress}
	var arguments bytes.Buffer
	for _, value := range values {
		arguments.WriteString("<string>")
		if err := xml.EscapeText(&arguments, []byte(value)); err != nil {
			return nil, err
		}
		arguments.WriteString("</string>")
	}
	document := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array>%s</array>
<key>RunAtLoad</key><true/>
<key>ProcessType</key><string>Interactive</string>
</dict></plist>
`, label, arguments.String())
	return []byte(document), nil
}

func ensureOrdinaryDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err = os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("LaunchAgents directory is unavailable")
	}
	return nil
}
