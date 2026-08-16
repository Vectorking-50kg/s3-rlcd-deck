package diagnostics

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
)

// Private file replacement performs platform-native permission checks for
// every rotated segment. Keep the test operation bounded while allowing the
// slower Windows and Intel macOS CI filesystems to exercise the full workload.
const testIOTimeout = 15 * time.Second

func TestRecordRejectsSecretShapedAndUnboundedFields(t *testing.T) {
	service := openTestService(t, Config{})
	canaries := []Event{
		{Level: LevelInfo, Module: ModuleRuntime, Code: CodeRuntimeReady, ErrorCode: "Authorization: Bearer secret"},
		{Level: LevelInfo, Module: Module("/Users/alice/private"), Code: CodeRuntimeReady},
		{Level: LevelInfo, Module: ModuleProvider, Code: CodeProviderRequest, SchemaVersion: "../secret"},
		{Level: LevelInfo, Module: ModuleProvider, Code: CodeProviderRequest, IdentifierHash: strings.Repeat("a", 64)},
		{Level: Level("panic: API_KEY=secret"), Module: ModuleRuntime, Code: CodePanicRecovered},
	}
	for index, event := range canaries {
		if service.Record(event) {
			t.Fatalf("secret-shaped event %d was accepted", index)
		}
	}
	valid := Event{
		Level: LevelWarning, Module: ModuleProvider, Code: CodeProviderRequest,
		HTTPStatus: 503, LatencyMS: 127, SchemaVersion: "v1.2",
		ErrorCode: ErrorUnavailable, IdentifierHash: HashIdentifier("provider-private-id"),
	}
	if !service.Record(valid) {
		t.Fatal("fixed redacted event was rejected")
	}
	flushTestService(t, service)
	documents := readAllSegments(t, service.directory)
	for _, canary := range []string{"Authorization", "secret", "/Users/", "provider-private-id"} {
		if bytes.Contains(documents, []byte(canary)) {
			t.Fatalf("persisted diagnostics contain canary %q", canary)
		}
	}
}

func TestProviderAdapterPersistsOnlyTheFixedDiagnosticProjection(t *testing.T) {
	service := openTestService(t, Config{})
	if !service.RecordProvider(ProviderDiagnostic{
		ProviderID: "private-provider-account", HTTPStatus: 429, LatencyMS: 88,
		SchemaVersion: "v1", ErrorCode: string(ErrorTimeout),
	}) {
		t.Fatal("provider diagnostic was rejected")
	}
	if service.RecordProvider(ProviderDiagnostic{
		ProviderID: "private-provider-account", HTTPStatus: 200, LatencyMS: 2,
		SchemaVersion: "v1", ErrorCode: "API_KEY=secret",
	}) {
		t.Fatal("arbitrary Provider error was accepted")
	}
	flushTestService(t, service)
	document := readAllSegments(t, service.directory)
	for _, forbidden := range []string{"private-provider-account", "API_KEY", "secret"} {
		if bytes.Contains(document, []byte(forbidden)) {
			t.Fatalf("Provider diagnostic leaked %q", forbidden)
		}
	}
	if !bytes.Contains(document, []byte(`"http_status":429`)) ||
		!bytes.Contains(document, []byte(`"latency_ms":88`)) ||
		!bytes.Contains(document, []byte(`"error_code":"timeout"`)) {
		t.Fatalf("Provider fixed facts missing: %s", document)
	}
}

func TestSegmentsRotateConcurrentlyAndRespectRetentionBounds(t *testing.T) {
	base := time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	now := base
	service := openTestService(t, Config{
		Now:       func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now },
		Retention: 2 * time.Hour, MaximumBytes: 1_400, MaximumSegmentBytes: 320,
		QueueCapacity: 4096,
	})
	var producers sync.WaitGroup
	for producer := 0; producer < 8; producer++ {
		producers.Add(1)
		go func(producer int) {
			defer producers.Done()
			for index := 0; index < 40; index++ {
				service.Record(Event{
					Level: LevelInfo, Module: ModuleRuntime, Code: CodeRuntimeReady,
				})
			}
		}(producer)
	}
	producers.Wait()
	flushTestService(t, service)
	assertSegmentBudget(t, service.directory, 1_400)

	clockMu.Lock()
	now = base.Add(3 * time.Hour)
	clockMu.Unlock()
	if !service.Record(Event{Level: LevelInfo, Module: ModuleRuntime, Code: CodeRuntimeStopped}) {
		t.Fatal("record after clock advance failed")
	}
	flushTestService(t, service)
	entries, err := os.ReadDir(service.directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("retention left %d segments, want one current segment", len(entries))
	}
}

func TestLowVolumeFlushesShareHourlyActiveSegmentsAcrossSevenDays(t *testing.T) {
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	now := base
	service := openTestService(t, Config{
		Now: func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			return now
		},
		MaximumBytes: 4 << 20, MaximumSegmentBytes: 64 << 10,
	})
	for hour := 0; hour < 7*24; hour++ {
		clockMu.Lock()
		now = base.Add(time.Duration(hour) * time.Hour)
		clockMu.Unlock()
		if !service.Record(Event{
			Level: LevelInfo, Module: ModuleRuntime, Code: CodeRuntimeReady,
		}) {
			t.Fatalf("record hour %d failed", hour)
		}
		flushTestService(t, service)
	}
	entries, err := os.ReadDir(service.directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 7*24-1 || len(entries) > 7*24 {
		t.Fatalf("seven days of hourly logs used %d segments", len(entries))
	}
	assertSegmentBudget(t, service.directory, 4<<20)
}

func TestSnapshotTruncationKeepsTheNewestEvents(t *testing.T) {
	base := time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC)
	service := openTestService(t, Config{Now: func() time.Time { return base }})
	for latency := 1; latency <= 12; latency++ {
		if !service.RecordProvider(ProviderDiagnostic{
			ProviderID: "provider-a", HTTPStatus: 200,
			LatencyMS: int64(latency), SchemaVersion: "v1",
		}) {
			t.Fatalf("record latency %d failed", latency)
		}
	}
	flushTestService(t, service)
	all := readAllSegments(t, service.directory)
	maximumBytes := len(all) / 4
	if maximumBytes == 0 {
		t.Fatal("persisted diagnostics were empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	document, truncated, err := service.snapshot(
		ctx,
		base.Add(-time.Second),
		maximumBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Fatal("bounded snapshot did not report truncation")
	}
	var latencies []uint32
	scanner := bufio.NewScanner(bytes.NewReader(document))
	for scanner.Scan() {
		var event storedEvent
		if err = json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		latencies = append(latencies, event.LatencyMS)
	}
	if err = scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(latencies) == 0 || latencies[0] == 1 || latencies[len(latencies)-1] != 12 {
		t.Fatalf("snapshot did not retain the newest events: %v", latencies)
	}
}

func TestIdleTickerDoesNotRewriteAndStillExpiresSealedSegments(t *testing.T) {
	base := time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	now := base
	service := openTestService(t, Config{
		Now: func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			return now
		},
		Retention: 2 * time.Hour, FlushInterval: 2 * time.Millisecond,
	})
	if !service.Record(Event{
		Level: LevelInfo, Module: ModuleRuntime, Code: CodeRuntimeReady,
	}) {
		t.Fatal("record failed")
	}
	flushTestService(t, service)
	entries, err := os.ReadDir(service.directory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("segments = %d, %v", len(entries), err)
	}
	before, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	entries, err = os.ReadDir(service.directory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("idle segments = %d, %v", len(entries), err)
	}
	after, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("idle ticker rewrote an unchanged active segment")
	}

	clockMu.Lock()
	now = base.Add(4 * time.Hour)
	clockMu.Unlock()
	deadline := time.Now().Add(time.Second)
	for {
		entries, err = os.ReadDir(service.directory)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle sealed segment exceeded its retention window")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestConcurrentRotationStatusAndSnapshotUseAConsistentDiskView(t *testing.T) {
	service := openTestService(t, Config{
		MaximumBytes: 1 << 20, MaximumSegmentBytes: 320,
		QueueCapacity: 4096, FlushInterval: time.Millisecond,
	})
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		for latency := 1; latency <= 100; latency++ {
			service.RecordProvider(ProviderDiagnostic{
				ProviderID: "provider-a", HTTPStatus: 200,
				LatencyMS: int64(latency), SchemaVersion: "v1",
			})
			time.Sleep(50 * time.Microsecond)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for iteration := 0; iteration < 50; iteration++ {
		if !service.Status().Available {
			t.Fatal("status observed a transient replacement file")
		}
		if _, _, err := service.snapshot(ctx, time.Time{}, 1<<20); err != nil {
			t.Fatalf("snapshot observed a transient disk state: %v", err)
		}
		select {
		case <-producerDone:
			iteration = 50
		default:
		}
	}
	<-producerDone
	flushTestService(t, service)
}

func TestRestartAtTheSameClockValueDoesNotOverwriteAnImmutableSegment(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "diagnostics")
	fixed := time.Date(2026, 8, 15, 8, 0, 0, 123, time.UTC)
	open := func() *Service {
		service, err := Open(Config{Directory: directory, Now: func() time.Time { return fixed }})
		if err != nil {
			t.Fatal(err)
		}
		return service
	}
	first := open()
	if !first.Record(Event{Level: LevelInfo, Module: ModuleRuntime, Code: CodeRuntimeReady}) {
		t.Fatal("first record failed")
	}
	flushTestService(t, first)
	closeTestService(t, first)

	second := open()
	if !second.Record(Event{Level: LevelInfo, Module: ModuleRuntime, Code: CodeRuntimeStopped}) {
		t.Fatal("second record failed")
	}
	flushTestService(t, second)
	closeTestService(t, second)

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("restart left %d immutable segments, want 2", len(entries))
	}
	document := readAllSegments(t, directory)
	if !bytes.Contains(document, []byte(`"code":"runtime_ready"`)) ||
		!bytes.Contains(document, []byte(`"code":"runtime_stopped"`)) {
		t.Fatalf("restart overwrote a segment: %s", document)
	}
}

func TestExportRejectsTrailingOrUnexpectedPersistedContent(t *testing.T) {
	service := openTestService(t, Config{})
	if !service.Record(Event{Level: LevelInfo, Module: ModuleRuntime, Code: CodeRuntimeReady}) {
		t.Fatal("record failed")
	}
	flushTestService(t, service)
	entries, err := os.ReadDir(service.directory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("segments = %d, %v", len(entries), err)
	}
	closeTestService(t, service)
	path := filepath.Join(service.directory, entries[0].Name())
	contents := readAllSegments(t, service.directory)
	contents = bytes.TrimSuffix(contents, []byte("\n"))
	contents = append(contents, []byte(` {"Authorization":"Bearer secret"}`)...)
	contents = append(contents, '\n')
	if _, err = protectedfile.Replace(path, contents); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(Config{Directory: service.directory})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTestService(t, reopened) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err = reopened.Export(ctx, BundleInput{
		BuildVersion: "v1", BuildCommit: "unknown",
	}); err == nil {
		t.Fatal("bundle accepted trailing persisted content")
	}
}

func TestExportBundleIsBoundedHashedAndTraversalSafe(t *testing.T) {
	service := openTestService(t, Config{MaximumExportBytes: 1 << 20})
	if !service.Record(Event{
		Level: LevelInfo, Module: ModuleProvider, Code: CodeProviderRequest,
		HTTPStatus: 200, LatencyMS: 34, SchemaVersion: "v1",
		IdentifierHash: HashIdentifier("provider-a"),
	}) {
		t.Fatal("record failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	bundle, manifest, err := service.Export(ctx, BundleInput{
		BuildVersion: "0.3.1-dev", BuildCommit: strings.Repeat("a", 40),
		ConfigurationSchemaKeys: []string{"history.enabled", "providers[].mapping"},
		DeckRings: []DeckRing{{
			DeviceIDHash: HashIdentifier("deck-private-id"), Dropped: 2,
			Events: []DeckEvent{{
				MonotonicMS: 42, Level: DeckLevelWarning,
				Component: DeckComponentWiFi, Code: DeckCodeUnavailable, Value: 7,
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle) > 1<<20 || len(manifest.Files) != 3 {
		t.Fatalf("unexpected bundle size/files: %d/%d", len(bundle), len(manifest.Files))
	}
	if manifest.EventWindowHours != 24 || manifest.EventsTruncated {
		t.Fatalf("unexpected event window metadata: %#v", manifest)
	}
	reader, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatal(err)
	}
	contents := make(map[string][]byte)
	for _, file := range reader.File {
		if !safeArchivePath(file.Name) || strings.Contains(file.Name, "\\") {
			t.Fatalf("unsafe archive path %q", file.Name)
		}
		opened, openErr := file.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		contents[file.Name], openErr = io.ReadAll(opened)
		_ = opened.Close()
		if openErr != nil {
			t.Fatal(openErr)
		}
	}
	if len(contents) != 4 || contents["manifest.json"] == nil {
		t.Fatalf("unexpected archive files: %#v", contents)
	}
	for _, entry := range manifest.Files {
		digest := sha256.Sum256(contents[entry.Path])
		if entry.SHA256 != hex.EncodeToString(digest[:]) || entry.Size != len(contents[entry.Path]) {
			t.Fatalf("manifest mismatch for %s", entry.Path)
		}
	}
	all := bytes.Join([][]byte{
		contents["manifest.json"], contents["companion/events.jsonl"],
		contents["deck/ring.json"], contents["configuration/schema-keys.json"],
	}, nil)
	for _, canary := range []string{"deck-private-id", "provider-a", "Authorization", "Cookie", "/Users/"} {
		if bytes.Contains(all, []byte(canary)) {
			t.Fatalf("bundle contains canary %q", canary)
		}
	}
}

func TestExportRejectsMalformedDeckRingAndSchemaKeys(t *testing.T) {
	service := openTestService(t, Config{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, input := range []BundleInput{
		{BuildVersion: "v1", BuildCommit: "unknown", ConfigurationSchemaKeys: []string{"../secret"}},
		{BuildVersion: "v1", BuildCommit: "unknown", DeckRings: []DeckRing{{DeviceIDHash: "raw-device-id"}}},
		{BuildVersion: "v1", BuildCommit: "unknown", DeckRings: []DeckRing{{
			DeviceIDHash: HashIdentifier("deck"),
			Events:       []DeckEvent{{Level: DeckLevel("token"), Component: DeckComponentWiFi, Code: DeckCodeReady}},
		}}},
	} {
		if _, _, err := service.Export(ctx, input); err == nil {
			t.Fatalf("malformed bundle input accepted: %#v", input)
		}
	}
}

func TestArchivePathsAndDiagnosticDirectoryEntriesFailClosed(t *testing.T) {
	for _, value := range []string{
		"", ".", "../escape", "deck/../../escape", "/absolute", `deck\escape`,
		"deck//ring.json", "deck/ring.json/", "deck/secret\n.json", "Deck/ring.json",
	} {
		if safeArchivePath(value) {
			t.Fatalf("unsafe archive path accepted: %q", value)
		}
	}
	for _, value := range []string{
		"manifest.json", "companion/events.jsonl", "deck/ring.json",
		"configuration/schema-keys.json",
	} {
		if !safeArchivePath(value) {
			t.Fatalf("fixed archive path rejected: %q", value)
		}
	}

	service := openTestService(t, Config{})
	if err := os.WriteFile(filepath.Join(service.directory, "../diagnostics-escape"), []byte("canary"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A foreign entry inside the private segment directory cannot become an
	// archive path or be interpreted as a log segment.
	if err := os.WriteFile(filepath.Join(service.directory, "foreign.jsonl"), []byte("canary"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := service.Export(ctx, BundleInput{
		BuildVersion: "v1", BuildCommit: "unknown",
	}); err == nil {
		t.Fatal("foreign diagnostic directory entry was accepted")
	}
}

func TestRecoverRecordsOnlyFixedPanicEvent(t *testing.T) {
	service := openTestService(t, Config{})
	func() {
		defer service.Recover(ModuleRuntime, CodePanicRecovered)
		panic("Authorization: Bearer super-secret")
	}()
	flushTestService(t, service)
	documents := readAllSegments(t, service.directory)
	if !bytes.Contains(documents, []byte(`"code":"panic_recovered"`)) ||
		bytes.Contains(documents, []byte("super-secret")) {
		t.Fatalf("panic record is not fixed and redacted: %s", documents)
	}
}

func openTestService(t *testing.T, config Config) *Service {
	t.Helper()
	config.Directory = filepath.Join(t.TempDir(), "diagnostics")
	service, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), testIOTimeout)
		defer cancel()
		if err := service.Close(ctx); err != nil {
			t.Errorf("close diagnostics: %v", err)
		}
	})
	return service
}

func flushTestService(t *testing.T, service *Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testIOTimeout)
	defer cancel()
	if err := service.Flush(ctx); err != nil {
		t.Fatal(err)
	}
}

func closeTestService(t *testing.T, service *Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testIOTimeout)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func readAllSegments(t *testing.T, directory string) []byte {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	var result []byte
	for _, entry := range entries {
		contents, readErr := protectedfile.Read(filepath.Join(directory, entry.Name()), 1<<20)
		if readErr != nil {
			t.Fatal(readErr)
		}
		result = append(result, contents...)
	}
	return result
}

func assertSegmentBudget(t *testing.T, directory string, maximum int64) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, entry := range entries {
		info, statErr := entry.Info()
		if statErr != nil {
			t.Fatal(statErr)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("non-regular segment %s", entry.Name())
		}
		total += info.Size()
		if err = protectedfile.VerifyPrivate(filepath.Join(directory, entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
	if total > maximum {
		t.Fatalf("segment bytes %d exceed %d", total, maximum)
	}
}

func FuzzEventValidationNeverPersistsArbitraryStrings(f *testing.F) {
	f.Add("Authorization: Bearer secret", "../private", "prompt")
	f.Add("runtime", "runtime_ready", "")
	f.Fuzz(func(t *testing.T, module, code, errorCode string) {
		event := Event{Level: LevelInfo, Module: Module(module), Code: Code(code), ErrorCode: ErrorCode(errorCode)}
		accepted := validEvent(event)
		if accepted {
			document, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"Authorization", "Bearer ", "../", "/Users/", "prompt"} {
				if bytes.Contains(document, []byte(forbidden)) {
					t.Fatalf("accepted event contains %q", forbidden)
				}
			}
		}
	})
}

func FuzzArchivePathNeverEscapesFixedRelativeNamespace(f *testing.F) {
	f.Add("manifest.json")
	f.Add("../escape")
	f.Add("deck/ring.json")
	f.Fuzz(func(t *testing.T, value string) {
		if !safeArchivePath(value) {
			return
		}
		if path.Clean(value) != value || path.IsAbs(value) || strings.Contains(value, "..") ||
			strings.ContainsAny(value, "\\\r\n\x00") {
			t.Fatalf("accepted archive path escaped the fixed namespace: %q", value)
		}
	})
}
