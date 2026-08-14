package codexobserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/sessionidentity"
)

var observerNow = time.Date(2026, 8, 14, 4, 0, 0, 0, time.UTC)

type fakeSnapshotSource struct {
	snapshots []discoverySnapshot
	errors    []error
	next      int
}

func (source *fakeSnapshotSource) discover(context.Context) (discoverySnapshot, error) {
	index := source.next
	if index >= len(source.snapshots) {
		index = len(source.snapshots) - 1
	}
	source.next++
	if index >= 0 && index < len(source.errors) && source.errors[index] != nil {
		return discoverySnapshot{}, source.errors[index]
	}
	return source.snapshots[index], nil
}

func sessionLine(identifier string) []byte {
	return []byte(`{"type":"session_meta","payload":{"id":"` + identifier + `","cwd":"/Users/private/secret"}}` + "\n")
}

func candidate(path, identifier string, size int64, modified time.Time) fileCandidate {
	return fileCandidate{
		path:      path,
		size:      size,
		modified:  modified,
		firstLine: sessionLine(identifier),
	}
}

func process(pid int, started time.Time, paths ...string) processObservation {
	return processObservation{
		identity:  processIdentity{pid: pid, startedUnixNano: started.UnixNano()},
		openFiles: append([]string(nil), paths...),
	}
}

func testObserver(source snapshotSource) *Observer {
	observer, err := New(Config{
		PollInterval: time.Millisecond,
		RecentWindow: 5 * time.Minute,
		Retention:    24 * time.Hour,
		Now:          func() time.Time { return observerNow },
	})
	if err != nil {
		panic(err)
	}
	observer.source = source
	return observer
}

func TestObserverRequiresSameUniqueProcessAndObservedGrowthForRunning(t *testing.T) {
	path := "/private/.codex/sessions/2026/08/14/session.jsonl"
	started := observerNow.Add(-time.Hour)
	source := &fakeSnapshotSource{snapshots: []discoverySnapshot{
		{
			strength:  mappingStrong,
			processes: []processObservation{process(41, started, path)},
			files:     []fileCandidate{candidate(path, "stable-session", 100, observerNow.Add(-time.Minute))},
		},
		{
			strength:  mappingStrong,
			processes: []processObservation{process(41, started, path)},
			files:     []fileCandidate{candidate(path, "stable-session", 140, observerNow.Add(-30*time.Second))},
		},
		{
			strength:  mappingStrong,
			processes: []processObservation{process(41, started, path)},
			files:     []fileCandidate{candidate(path, "stable-session", 140, observerNow.Add(-30*time.Second))},
		},
		{
			strength: mappingStrong,
			files:    []fileCandidate{candidate(path, "stable-session", 140, observerNow.Add(-10*time.Minute))},
		},
	}}
	observer := testObserver(source)
	want := []aisnapshot.SessionState{
		aisnapshot.SessionRecent,
		aisnapshot.SessionRunning,
		aisnapshot.SessionRecent,
		aisnapshot.SessionEnded,
	}
	for index, state := range want {
		sessions, err := observer.collect(context.Background())
		if err != nil || len(sessions) != 1 || sessions[0].State != state {
			t.Fatalf("collect[%d] = %+v, %v; want %s", index, sessions, err, state)
		}
		if sessions[0].Source != aisnapshot.SessionSourceProcessJSONL ||
			sessions[0].Confidence != aisnapshot.ConfidenceInferred ||
			sessions[0].DisplayName != nil {
			t.Fatalf("unsafe inferred session = %+v", sessions[0])
		}
	}
}

func TestObserverDoesNotInheritRunningAcrossPIDReuseOrRotation(t *testing.T) {
	oldPath := "/private/.codex/sessions/old.jsonl"
	newPath := "/private/.codex/sessions/new.jsonl"
	oldStart := observerNow.Add(-time.Hour)
	source := &fakeSnapshotSource{snapshots: []discoverySnapshot{
		{
			strength:  mappingStrong,
			processes: []processObservation{process(7, oldStart, oldPath)},
			files:     []fileCandidate{candidate(oldPath, "rotated", 100, observerNow.Add(-time.Minute))},
		},
		{
			strength:  mappingStrong,
			processes: []processObservation{process(7, oldStart, oldPath)},
			files:     []fileCandidate{candidate(oldPath, "rotated", 120, observerNow.Add(-30*time.Second))},
		},
		{
			strength:  mappingStrong,
			processes: []processObservation{process(7, observerNow.Add(-time.Minute), oldPath)},
			files:     []fileCandidate{candidate(oldPath, "rotated", 140, observerNow.Add(-10*time.Second))},
		},
		{
			strength:  mappingStrong,
			processes: []processObservation{process(7, observerNow.Add(-time.Minute), newPath)},
			files:     []fileCandidate{candidate(newPath, "rotated", 20, observerNow.Add(-5*time.Second))},
		},
	}}
	observer := testObserver(source)
	for index, want := range []aisnapshot.SessionState{
		aisnapshot.SessionRecent,
		aisnapshot.SessionRunning,
		aisnapshot.SessionRecent,
		aisnapshot.SessionRecent,
	} {
		sessions, err := observer.collect(context.Background())
		if err != nil || sessions[0].State != want {
			t.Fatalf("collect[%d] = %+v, %v; want %s", index, sessions, err, want)
		}
		if sessions[0].ID != sessionidentity.Codex("rotated") {
			t.Fatalf("rotation changed anonymous id: %q", sessions[0].ID)
		}
	}
}

func TestObserverDoesNotInheritRunningWhenFileIsReplacedAtSamePath(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "session.jsonl")
	if err := os.WriteFile(path, sessionLine("same-path"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldFile, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	oldInfo, err := oldFile.Stat()
	if err != nil {
		_ = oldFile.Close()
		t.Fatal(err)
	}
	if err = oldFile.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(path, filepath.Join(directory, "old.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, sessionLine("same-path"), 0o600); err != nil {
		t.Fatal(err)
	}
	newFile, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	newInfo, err := newFile.Stat()
	if err != nil {
		_ = newFile.Close()
		t.Fatal(err)
	}
	if err = newFile.Close(); err != nil {
		t.Fatal(err)
	}
	started := observerNow.Add(-time.Hour)
	first := candidate(path, "same-path", 10, observerNow.Add(-time.Minute))
	first.info = oldInfo
	second := candidate(path, "same-path", 20, observerNow.Add(-30*time.Second))
	second.info = oldInfo
	replaced := candidate(path, "same-path", 30, observerNow.Add(-10*time.Second))
	replaced.info = newInfo
	source := &fakeSnapshotSource{snapshots: []discoverySnapshot{
		{strength: mappingStrong, processes: []processObservation{process(8, started, path)}, files: []fileCandidate{first}},
		{strength: mappingStrong, processes: []processObservation{process(8, started, path)}, files: []fileCandidate{second}},
		{strength: mappingStrong, processes: []processObservation{process(8, started, path)}, files: []fileCandidate{replaced}},
	}}
	observer := testObserver(source)
	for index, want := range []aisnapshot.SessionState{
		aisnapshot.SessionRecent,
		aisnapshot.SessionRunning,
		aisnapshot.SessionRecent,
	} {
		sessions, collectErr := observer.collect(context.Background())
		if collectErr != nil || sessions[0].State != want {
			t.Fatalf("collect[%d] = %+v, %v; want %s", index, sessions, collectErr, want)
		}
	}
}

func TestObserverFailsAmbiguousAndWindowsWeakMappingsClosed(t *testing.T) {
	first := "/private/.codex/sessions/one.jsonl"
	second := "/private/.codex/sessions/two.jsonl"
	source := &fakeSnapshotSource{snapshots: []discoverySnapshot{
		{
			strength: mappingStrong,
			processes: []processObservation{
				process(10, observerNow.Add(-time.Hour), first, second),
			},
			files: []fileCandidate{
				candidate(first, "one", 10, observerNow.Add(-time.Minute)),
				candidate(second, "two", 10, observerNow.Add(-time.Minute)),
			},
		},
		{
			strength:  mappingWeak,
			processes: []processObservation{process(11, observerNow.Add(-time.Hour))},
			files:     []fileCandidate{candidate(first, "one", 20, observerNow.Add(-time.Minute))},
		},
	}}
	observer := testObserver(source)
	for index := 0; index < 2; index++ {
		sessions, err := observer.collect(context.Background())
		if err != nil || len(sessions) == 0 {
			t.Fatalf("collect[%d] = %+v, %v", index, sessions, err)
		}
		for _, session := range sessions {
			if session.State != aisnapshot.SessionUnknown {
				t.Fatalf("ambiguous session = %+v", session)
			}
		}
	}
}

func TestTruncatedCandidateSetCannotProduceRunning(t *testing.T) {
	path := "/private/.codex/sessions/visible.jsonl"
	started := observerNow.Add(-time.Hour)
	source := &fakeSnapshotSource{snapshots: []discoverySnapshot{
		{
			strength:           mappingStrong,
			candidatesOverflow: true,
			processes:          []processObservation{process(20, started, path)},
			files:              []fileCandidate{candidate(path, "visible", 10, observerNow.Add(-time.Minute))},
		},
		{
			strength:           mappingStrong,
			candidatesOverflow: true,
			files:              []fileCandidate{candidate(path, "visible", 20, observerNow.Add(-time.Second))},
		},
	}}
	observer := testObserver(source)
	for index := 0; index < 2; index++ {
		sessions, err := observer.collect(context.Background())
		if err != nil || len(sessions) != 1 || sessions[0].State != aisnapshot.SessionUnknown {
			t.Fatalf("overflow collect[%d] = %+v, %v", index, sessions, err)
		}
	}
}

func TestUnrelatedStrongProcessDoesNotPoisonFileOnlyRecency(t *testing.T) {
	source := &fakeSnapshotSource{snapshots: []discoverySnapshot{{
		strength:  mappingStrong,
		processes: []processObservation{process(99, observerNow.Add(-time.Hour))},
		files: []fileCandidate{
			candidate("/private/recent.jsonl", "recent", 10, observerNow.Add(-time.Minute)),
			candidate("/private/ended.jsonl", "ended", 10, observerNow.Add(-time.Hour)),
		},
	}}}
	sessions, err := testObserver(source).collect(context.Background())
	if err != nil || len(sessions) != 2 {
		t.Fatalf("sessions = %+v, %v", sessions, err)
	}
	if sessions[0].State != aisnapshot.SessionRecent || sessions[1].State != aisnapshot.SessionEnded {
		t.Fatalf("unrelated process changed file-only states: %+v", sessions)
	}
}

func TestObserverRejectsDuplicateSessionFilesAndNeverEmitsWaitingStates(t *testing.T) {
	source := &fakeSnapshotSource{snapshots: []discoverySnapshot{{
		strength: mappingStrong,
		files: []fileCandidate{
			candidate("/private/a.jsonl", "duplicate", 10, observerNow.Add(-time.Minute)),
			candidate("/private/b.jsonl", "duplicate", 12, observerNow.Add(-30*time.Second)),
		},
	}}}
	sessions, err := testObserver(source).collect(context.Background())
	if err != nil || len(sessions) != 1 || sessions[0].State != aisnapshot.SessionUnknown {
		t.Fatalf("duplicate result = %+v, %v", sessions, err)
	}
	if sessions[0].State == aisnapshot.SessionWaitingApproval ||
		sessions[0].State == aisnapshot.SessionWaitingInput {
		t.Fatalf("observer inferred a forbidden waiting state: %+v", sessions[0])
	}
}

func TestFutureDatedEvidenceFailsClosedEvenWhenItGrows(t *testing.T) {
	path := "/private/future.jsonl"
	started := observerNow.Add(-time.Hour)
	source := &fakeSnapshotSource{snapshots: []discoverySnapshot{
		{
			strength:  mappingStrong,
			processes: []processObservation{process(12, started, path)},
			files:     []fileCandidate{candidate(path, "future", 10, observerNow.Add(time.Hour))},
		},
		{
			strength:  mappingStrong,
			processes: []processObservation{process(12, started, path)},
			files:     []fileCandidate{candidate(path, "future", 20, observerNow.Add(time.Hour))},
		},
	}}
	observer := testObserver(source)
	for index := 0; index < 2; index++ {
		sessions, err := observer.collect(context.Background())
		if err != nil || len(sessions) != 1 || sessions[0].State != aisnapshot.SessionUnknown ||
			sessions[0].LastActivityAt != nil {
			t.Fatalf("future collect[%d] = %+v, %v", index, sessions, err)
		}
	}
}

func TestObserverPrivacyCanaryAndMalformedPrefixFailSafe(t *testing.T) {
	document, err := os.ReadFile("testdata/session-meta-privacy.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	source := &fakeSnapshotSource{snapshots: []discoverySnapshot{{
		strength: mappingStrong,
		files: []fileCandidate{
			{path: "/private/privacy.jsonl", size: int64(len(document)), modified: observerNow.Add(-time.Minute), firstLine: document},
			{path: "/private/partial.jsonl", size: 50, modified: observerNow, firstLine: []byte(`{"type":"session_meta","payload":{"id":"partial"}`)},
			{path: "/private/invalid.jsonl", size: 50, modified: observerNow, firstLine: []byte("{not-json}\n")},
		},
	}}}
	sessions, err := testObserver(source).collect(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("privacy result = %+v, %v", sessions, err)
	}
	encoded, err := json.Marshal(sessions)
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{
		"PRIVATE_TITLE_CANARY", "PRIVATE_PROMPT_CANARY", "/Users/alice/private-project",
		"PRIVATE_COMMAND_CANARY", "PRIVATE_TOOL_ARGUMENT_CANARY", "privacy-upstream-id",
	} {
		if strings.Contains(string(encoded), canary) {
			t.Fatalf("normalized output leaked %q: %s", canary, encoded)
		}
	}
	if sessions[0].ID != sessionidentity.Codex("privacy-upstream-id") {
		t.Fatalf("anonymous identifier = %q", sessions[0].ID)
	}
}

func TestObserverRunClearsOnlyInferredSessionsAfterDiscoveryFailure(t *testing.T) {
	source := &fakeSnapshotSource{
		snapshots: []discoverySnapshot{
			{strength: mappingStrong, files: []fileCandidate{candidate("/a.jsonl", "one", 10, observerNow)}},
			{},
		},
		errors: []error{nil, errors.New("permission denied")},
	}
	observer := testObserver(source)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := make(chan []aisnapshot.Session, 4)
	done := make(chan error, 1)
	published := 0
	go func() {
		done <- observer.Run(ctx, func(_ context.Context, sessions []aisnapshot.Session) error {
			updates <- sessions
			published++
			if published == 2 {
				cancel()
			}
			return nil
		})
	}()
	first := <-updates
	second := <-updates
	if len(first) != 1 || len(second) != 0 {
		t.Fatalf("updates = %+v then %+v", first, second)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestDiscoveryFailureBreaksRunningContinuity(t *testing.T) {
	path := "/private/session.jsonl"
	started := observerNow.Add(-time.Hour)
	source := &fakeSnapshotSource{
		snapshots: []discoverySnapshot{
			{
				strength:  mappingStrong,
				processes: []processObservation{process(55, started, path)},
				files:     []fileCandidate{candidate(path, "failure-gap", 10, observerNow.Add(-time.Minute))},
			},
			{},
			{
				strength:  mappingStrong,
				processes: []processObservation{process(55, started, path)},
				files:     []fileCandidate{candidate(path, "failure-gap", 20, observerNow.Add(-time.Second))},
			},
		},
		errors: []error{nil, errors.New("permission denied"), nil},
	}
	observer := testObserver(source)
	ctx, cancel := context.WithCancel(context.Background())
	updates := make(chan []aisnapshot.Session, 3)
	done := make(chan error, 1)
	published := 0
	go func() {
		done <- observer.Run(ctx, func(_ context.Context, sessions []aisnapshot.Session) error {
			updates <- sessions
			published++
			if published == 3 {
				cancel()
			}
			return nil
		})
	}()
	first := <-updates
	failed := <-updates
	recovered := <-updates
	if len(first) != 1 || first[0].State != aisnapshot.SessionRecent || len(failed) != 0 ||
		len(recovered) != 1 || recovered[0].State != aisnapshot.SessionRecent {
		t.Fatalf("updates = %+v, %+v, %+v", first, failed, recovered)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop")
	}
}
