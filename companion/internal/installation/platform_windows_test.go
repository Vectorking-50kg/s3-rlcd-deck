//go:build windows

package installation

import (
	"encoding/binary"
	"encoding/xml"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestScheduledTaskEnabledXML(t *testing.T) {
	for _, test := range []struct {
		document string
		enabled  bool
		valid    bool
	}{
		{`<Task xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task"><Settings><Enabled>true</Enabled></Settings></Task>`, true, true},
		{`<Task><Settings><Enabled>false</Enabled></Settings></Task>`, false, true},
		{`<Task><Settings/></Task>`, false, false},
	} {
		enabled, err := scheduledTaskEnabled([]byte(test.document))
		if (err == nil) != test.valid || enabled != test.enabled {
			t.Fatalf("enabled=%v err=%v for %q", enabled, err, test.document)
		}
	}
}

func TestScheduledTaskEnabledRejectsMalformedUTF16(t *testing.T) {
	document := []byte{0xff, 0xfe, 0x00, 0xd8}
	if _, err := scheduledTaskEnabled(document); err == nil {
		t.Fatal("unpaired UTF-16 surrogate was accepted")
	}
}

func TestWindowsCommandLineQuotesUnicodeAndSpaces(t *testing.T) {
	result := windowsCommandLine([]string{`C:\Users\名字\S3 Deck\companion.exe`, `--data-directory`, `C:\Data Root\`})
	want := `"C:\Users\名字\S3 Deck\companion.exe" --data-directory "C:\Data Root\\"`
	if result != want {
		t.Fatalf("command line = %q, want %q", result, want)
	}
}

func TestScheduledTaskDocumentUsesPasswordlessCurrentUserPrincipal(t *testing.T) {
	document, err := scheduledTaskDocument(launchSpec{
		Executable:       `C:\Users\名字\S3 Deck\companion.exe`,
		DataDirectory:    `C:\Users\名字\Data & State`,
		DeviceHubAddress: `127.0.0.1:7780`,
	}, "S-1-5-21-1000")
	if err != nil {
		t.Fatal(err)
	}
	if len(document) < 2 || document[0] != 0xff || document[1] != 0xfe {
		t.Fatal("scheduled task XML is not UTF-16LE with a BOM")
	}
	units := make([]uint16, (len(document)-2)/2)
	for index := range units {
		units[index] = binary.LittleEndian.Uint16(document[2+index*2:])
	}
	decoded := string(utf16.Decode(units))
	parserDocument := strings.Replace(decoded, `encoding="UTF-16"`, `encoding="UTF-8"`, 1)
	var parsed struct {
		Principals struct {
			Principal struct {
				UserID    string `xml:"UserId"`
				LogonType string `xml:"LogonType"`
				RunLevel  string `xml:"RunLevel"`
			} `xml:"Principal"`
		} `xml:"Principals"`
		Settings struct {
			Enabled bool `xml:"Enabled"`
		} `xml:"Settings"`
		Actions struct {
			Exec struct {
				Command   string `xml:"Command"`
				Arguments string `xml:"Arguments"`
			} `xml:"Exec"`
		} `xml:"Actions"`
	}
	if err = xml.Unmarshal([]byte(parserDocument), &parsed); err != nil {
		t.Fatal(err)
	}
	principal := parsed.Principals.Principal
	if principal.UserID != "S-1-5-21-1000" || principal.LogonType != "InteractiveToken" ||
		principal.RunLevel != "LeastPrivilege" || parsed.Settings.Enabled {
		t.Fatalf("unsafe task principal/settings: %+v enabled=%v", principal, parsed.Settings.Enabled)
	}
	if parsed.Actions.Exec.Command != `C:\Users\名字\S3 Deck\companion.exe` ||
		!strings.Contains(parsed.Actions.Exec.Arguments, `"C:\Users\名字\Data & State"`) ||
		strings.Contains(decoded, "<Password>") || !strings.Contains(decoded, `<Task version="1.2"`) {
		t.Fatalf("unexpected task action: %+v", parsed.Actions.Exec)
	}
	enabled, err := scheduledTaskEnabled(document)
	if err != nil || enabled {
		t.Fatalf("UTF-16 scheduled task status = %v, %v", enabled, err)
	}
	enabled, err = scheduledTaskEnabled(document[2:])
	if err != nil || enabled {
		t.Fatalf("BOM-less UTF-16 scheduled task status = %v, %v", enabled, err)
	}
}
