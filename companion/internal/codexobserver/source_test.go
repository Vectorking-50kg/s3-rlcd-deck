package codexobserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type emptyPlatform struct{}

func (emptyPlatform) discover(
	context.Context,
	[]string,
) (mappingStrength, []processObservation, error) {
	return mappingStrong, []processObservation{}, nil
}

func TestLiveCodexObserverEmitsOnlyNormalizedDTOs(t *testing.T) {
	if os.Getenv("S3DECK_TEST_CODEX_OBSERVER") != "1" {
		t.Skip("set S3DECK_TEST_CODEX_OBSERVER=1 for a local read-only smoke test")
	}
	observer, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := observer.collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	second, err := observer.collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"/Users/", "\\Users\\", ".codex", "prompt", "command", "tool_args"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("live DTO leaked forbidden content %q", forbidden)
		}
	}
	states := make([]string, len(second))
	for index := range second {
		states[index] = string(second[index].State)
	}
	t.Logf("read-only observer normalized %d then %d sessions; second states=%v", len(first), len(second), states)
}

func TestSystemSourceReadsOnlyBoundedFirstLineAndSkipsSymlinks(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	day := filepath.Join(root, "2026", "08", "14")
	if err := os.MkdirAll(day, 0o700); err != nil {
		t.Fatal(err)
	}
	valid := filepath.Join(day, "valid.jsonl")
	privateTail := "PRIVATE_PROMPT_MUST_NOT_BE_READ"
	if err := os.WriteFile(valid, append(sessionLine("valid-id"), []byte(privateTail)...), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(day, "target.jsonl")
	if err := os.WriteFile(target, sessionLine("target-id"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkCreated := true
	if err := os.Symlink(target, filepath.Join(day, "link.jsonl")); err != nil {
		if runtime.GOOS != "windows" {
			t.Fatal(err)
		}
		linkCreated = false
	}
	source := &systemSource{root: root, platform: emptyPlatform{}}
	snapshot, err := source.discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.files) != 2 {
		t.Fatalf("files = %+v, want two regular files and no symlink", snapshot.files)
	}
	for _, candidate := range snapshot.files {
		if strings.Contains(string(candidate.firstLine), privateTail) {
			t.Fatalf("source read private tail for %q", candidate.path)
		}
		if linkCreated && candidate.path == filepath.Join(day, "link.jsonl") {
			t.Fatal("source followed a symlink")
		}
	}
}

func TestSystemSourceFailsClosedOnUnreadableOrEscapingRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	source := &systemSource{root: missing, platform: emptyPlatform{}}
	snapshot, err := source.discover(context.Background())
	if err != nil || len(snapshot.files) != 0 {
		t.Fatalf("missing root = %+v, %v", snapshot, err)
	}

	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "sessions")
	if err = os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("Windows runner does not permit unprivileged symlink creation")
		}
		t.Fatal(err)
	}
	source.root = link
	if _, err = source.discover(context.Background()); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("symlink root error = %v, want %v", err, ErrInvalidConfig)
	}
}

func TestSystemSourceMarksCandidateTruncationInsteadOfClaimingCompleteness(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	day := filepath.Join(root, "2026", "08", "14")
	if err := os.MkdirAll(day, 0o700); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < maximumCandidates+1; index++ {
		name := filepath.Join(day, fmt.Sprintf("session-%03d.jsonl", index))
		if err := os.WriteFile(name, sessionLine(fmt.Sprintf("session-%03d", index)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	source := &systemSource{root: root, platform: emptyPlatform{}}
	snapshot, err := source.discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.candidatesOverflow || len(snapshot.files) != maximumCandidates {
		t.Fatalf("snapshot has overflow=%v, files=%d", snapshot.candidatesOverflow, len(snapshot.files))
	}
}

func TestBoundedReaderRejectsOversizedOrPartialFirstLine(t *testing.T) {
	directory := t.TempDir()
	oversized := filepath.Join(directory, "oversized.jsonl")
	if err := os.WriteFile(oversized, []byte(strings.Repeat("x", maximumFirstLine+1)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(oversized)
	if err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	line, _, err := readBoundedLine(file, info)
	_ = file.Close()
	if err != nil || len(line) != 0 {
		t.Fatalf("oversized line = %d bytes, %v", len(line), err)
	}
	partial := filepath.Join(directory, "partial.jsonl")
	if err = os.WriteFile(partial, []byte(`{"type":"session_meta"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err = os.Open(partial)
	if err != nil {
		t.Fatal(err)
	}
	info, err = file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	line, _, err = readBoundedLine(file, info)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, valid := parseSessionIdentifier(line); valid {
		t.Fatal("partial first line was accepted")
	}
}

func TestCodexProcessNameFilterIsNarrowAndCrossPlatform(t *testing.T) {
	for _, name := range []string{"codex", "Codex.exe", "/usr/local/bin/codex-cli"} {
		if !isCodexProcessName(name) {
			t.Fatalf("%q was not recognized", name)
		}
	}
	for _, name := range []string{"codex-helper", "my-codex", "code", "codex-agent.exe.bak"} {
		if isCodexProcessName(name) {
			t.Fatalf("%q was overmatched", name)
		}
	}
}
