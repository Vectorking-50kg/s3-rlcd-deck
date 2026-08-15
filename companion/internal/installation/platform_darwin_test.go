//go:build darwin

package installation

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
)

func TestLaunchAgentDocumentEscapesPathsAndUsesExactLabel(t *testing.T) {
	document, err := launchAgentDocument(
		launchAgentLabelPrefix+".0123456789ab",
		launchSpec{Executable: `/Applications/S3 & Deck`, DataDirectory: `/Users/名字/Data`, DeviceHubAddress: "127.0.0.1:7780"},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range [][]byte{
		[]byte("com.vectorking.s3-rlcd-deck-companion.0123456789ab"),
		[]byte(`/Applications/S3 &amp; Deck`),
		[]byte(`/Users/名字/Data`),
	} {
		if !bytes.Contains(document, expected) {
			t.Fatalf("LaunchAgent document is missing %q", expected)
		}
	}
}

func TestLaunchAgentEnableDisableAndPersistentStatus(t *testing.T) {
	directory := t.TempDir()
	label := launchAgentLabelPrefix + ".0123456789ab"
	var commands [][]string
	disabledState := "enabled"
	adapter := &darwinAdapter{
		label: label, domain: "gui/501", service: "gui/501/" + label,
		templatePath: filepath.Join(directory, "template.plist"),
		plistPath:    filepath.Join(directory, "live.plist"),
		run: func(_ context.Context, name string, arguments ...string) ([]byte, error) {
			command := append([]string{name}, arguments...)
			commands = append(commands, command)
			if len(arguments) > 0 && arguments[0] == "print-disabled" {
				return []byte("disabled services = {\n\t\"" + label + "\" => " + disabledState + "\n}\n"), nil
			}
			return nil, nil
		},
	}
	if _, err := protectedfile.Replace(adapter.templatePath, []byte("private plist")); err != nil {
		t.Fatal(err)
	}
	if err := adapter.SetEnabled(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if want := []string{"/bin/launchctl", "enable", adapter.service}; !reflect.DeepEqual(commands[len(commands)-1], want) {
		t.Fatalf("enable command = %#v, want %#v", commands[len(commands)-1], want)
	}
	status, err := adapter.Status(context.Background())
	if err != nil || !status.Enabled {
		t.Fatalf("enabled status = %#v, %v", status, err)
	}
	disabledState = "disabled"
	status, err = adapter.Status(context.Background())
	if err != nil || status.Enabled {
		t.Fatalf("persistently disabled status = %#v, %v", status, err)
	}
	if err = adapter.SetEnabled(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if want := []string{"/bin/launchctl", "disable", adapter.service}; !reflect.DeepEqual(commands[len(commands)-1], want) {
		t.Fatalf("disable command = %#v, want %#v", commands[len(commands)-1], want)
	}
	if _, err = os.Lstat(adapter.plistPath); !os.IsNotExist(err) {
		t.Fatalf("disabled live plist remained: %v", err)
	}
}

func TestLaunchAgentDisabledParserIsFailClosed(t *testing.T) {
	label := launchAgentLabelPrefix + ".0123456789ab"
	for _, test := range []struct {
		document string
		disabled bool
		valid    bool
	}{
		{`"` + label + `" => disabled`, true, true},
		{`"` + label + `" => true`, true, true},
		{`"` + label + `" => enabled`, false, true},
		{`"` + label + `" => false`, false, true},
		{`"another.service" => disabled`, false, true},
		{`"` + label + `" => maybe`, false, false},
	} {
		disabled, err := launchAgentDisabled([]byte(test.document), label)
		if disabled != test.disabled || (err == nil) != test.valid {
			t.Fatalf("parse %q = %t, %v", test.document, disabled, err)
		}
	}
}
