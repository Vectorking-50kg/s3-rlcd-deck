//go:build windows

package installation

import "testing"

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

func TestWindowsCommandLineQuotesUnicodeAndSpaces(t *testing.T) {
	result := windowsCommandLine([]string{`C:\Users\名字\S3 Deck\companion.exe`, `--data-directory`, `C:\Data Root\`})
	want := `"C:\Users\名字\S3 Deck\companion.exe" --data-directory "C:\Data Root\\"`
	if result != want {
		t.Fatalf("command line = %q, want %q", result, want)
	}
}
