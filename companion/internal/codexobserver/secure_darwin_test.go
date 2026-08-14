//go:build darwin

package codexobserver

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDarwinSecureOpenRejectsFIFOWithoutBlocking(t *testing.T) {
	directoryPath := t.TempDir()
	parent, err := secureOpenRoot(directoryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	name := "replaced-session.jsonl"
	if err = unix.Mkfifo(filepath.Join(directoryPath, name), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		file, openErr := secureOpenChild(parent, name, false)
		if file != nil {
			_ = file.Close()
		}
		done <- openErr
	}()
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("FIFO was accepted as a regular session file")
		}
	case <-time.After(time.Second):
		t.Fatal("opening a FIFO blocked")
	}
}

func TestDarwinSecureOpenStillAcceptsRegularFileWithNonblockingFlag(t *testing.T) {
	directoryPath := t.TempDir()
	parent, err := secureOpenRoot(directoryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	name := "session.jsonl"
	if err = os.WriteFile(filepath.Join(directoryPath, name), sessionLine("regular"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := secureOpenChild(parent, name, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
}
