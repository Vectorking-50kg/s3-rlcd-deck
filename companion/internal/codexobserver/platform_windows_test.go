//go:build windows

package codexobserver

import (
	"context"
	"testing"
)

func TestWindowsAdapterNeverClaimsStrongProcessFileMapping(t *testing.T) {
	strength, _, err := (windowsPlatform{}).discover(context.Background(), []string{
		`C:\Users\private\.codex\sessions\2026\08\14\session.jsonl`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strength != mappingWeak {
		t.Fatalf("Windows mapping strength = %v, want weak", strength)
	}
}
