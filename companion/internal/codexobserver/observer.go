// Package codexobserver performs read-only, privacy-safe observation of local
// Codex processes and session JSONL files. It never starts, resumes, or takes
// ownership of a user session.
package codexobserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/sessionidentity"
)

const (
	defaultPollInterval = 2 * time.Second
	defaultRecentWindow = 5 * time.Minute
	defaultRetention    = 24 * time.Hour
	maximumSessions     = 16
)

var (
	ErrInvalidConfig  = errors.New("invalid Codex session observer configuration")
	ErrAlreadyRunning = errors.New("Codex session observer is already running")
)

type mappingStrength uint8

const (
	mappingWeak mappingStrength = iota
	mappingStrong
)

type processIdentity struct {
	pid             int
	startedUnixNano int64
}

type processObservation struct {
	identity  processIdentity
	openFiles []string
}

type fileCandidate struct {
	path      string
	size      int64
	modified  time.Time
	firstLine []byte
	info      os.FileInfo
}

type discoverySnapshot struct {
	strength           mappingStrength
	processes          []processObservation
	files              []fileCandidate
	candidatesOverflow bool
}

type snapshotSource interface {
	discover(context.Context) (discoverySnapshot, error)
}

// Config contains only observer policy. OS process details and session paths
// remain inside this package and never enter Runtime or the wire DTO.
type Config struct {
	SessionsRoot string
	PollInterval time.Duration
	RecentWindow time.Duration
	Retention    time.Duration
	Now          func() time.Time
}

// Publisher receives independently owned normalized inferred sessions.
type Publisher func(context.Context, []aisnapshot.Session) error

type priorObservation struct {
	path     string
	size     int64
	process  processIdentity
	hasOwner bool
	info     os.FileInfo
}

// Observer owns the inference cache. It is single-run and has no mutating
// operation for upstream Codex sessions.
type Observer struct {
	config   Config
	source   snapshotSource
	previous map[string]priorObservation
	mutex    sync.Mutex
	started  bool
}

func New(config Config) (*Observer, error) {
	if config.PollInterval < 0 || config.RecentWindow < 0 || config.Retention < 0 {
		return nil, ErrInvalidConfig
	}
	if config.PollInterval == 0 {
		config.PollInterval = defaultPollInterval
	}
	if config.RecentWindow == 0 {
		config.RecentWindow = defaultRecentWindow
	}
	if config.Retention == 0 {
		config.Retention = defaultRetention
	}
	if config.Retention < config.RecentWindow {
		return nil, ErrInvalidConfig
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	source, err := newSystemSource(config.SessionsRoot, config.Retention, config.Now)
	if err != nil {
		return nil, err
	}
	return &Observer{
		config:   config,
		source:   source,
		previous: make(map[string]priorObservation),
	}, nil
}

// Run publishes immediately and then polls until ctx is cancelled. Discovery
// errors publish an empty inferred set and are retried; they never suppress the
// independently collected official quota update.
func (observer *Observer) Run(ctx context.Context, publish Publisher) error {
	if publish == nil {
		return ErrInvalidConfig
	}
	observer.mutex.Lock()
	if observer.started {
		observer.mutex.Unlock()
		return ErrAlreadyRunning
	}
	observer.started = true
	observer.previous = make(map[string]priorObservation)
	observer.mutex.Unlock()
	defer func() {
		observer.mutex.Lock()
		observer.started = false
		observer.mutex.Unlock()
	}()
	publishOnce := func() error {
		sessions, err := observer.collect(ctx)
		if err != nil {
			observer.previous = make(map[string]priorObservation)
			sessions = []aisnapshot.Session{}
		}
		return publish(ctx, cloneSessions(sessions))
	}
	if err := publishOnce(); err != nil {
		return err
	}
	ticker := time.NewTicker(observer.config.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := publishOnce(); err != nil {
				return err
			}
		}
	}
}

func (observer *Observer) collect(ctx context.Context) ([]aisnapshot.Session, error) {
	snapshot, err := observer.source.discover(ctx)
	if err != nil {
		return nil, err
	}
	now := observer.config.Now().UTC()
	filesByID := make(map[string][]fileCandidate)
	for index := range snapshot.files {
		candidate := snapshot.files[index]
		identifier, valid := parseSessionIdentifier(candidate.firstLine)
		clear(candidate.firstLine)
		if !valid || candidate.size <= 0 || candidate.modified.IsZero() ||
			now.Sub(candidate.modified) > observer.config.Retention {
			continue
		}
		anonymousID := sessionidentity.Codex(identifier)
		filesByID[anonymousID] = append(filesByID[anonymousID], candidate)
	}

	pathProcesses := make(map[string][]processIdentity)
	processCandidates := make(map[processIdentity]int)
	validPaths := make(map[string]struct{})
	for _, files := range filesByID {
		for _, candidate := range files {
			validPaths[candidate.path] = struct{}{}
		}
	}
	for _, process := range snapshot.processes {
		seen := make(map[string]struct{})
		for _, path := range process.openFiles {
			if _, valid := validPaths[path]; !valid {
				continue
			}
			if _, duplicate := seen[path]; duplicate {
				continue
			}
			seen[path] = struct{}{}
			pathProcesses[path] = append(pathProcesses[path], process.identity)
			processCandidates[process.identity]++
		}
	}

	type result struct {
		session aisnapshot.Session
		updated time.Time
	}
	results := make([]result, 0, len(filesByID))
	nextPrevious := make(map[string]priorObservation, len(filesByID))
	for anonymousID, files := range filesByID {
		sort.Slice(files, func(left, right int) bool {
			if !files[left].modified.Equal(files[right].modified) {
				return files[left].modified.After(files[right].modified)
			}
			return files[left].path < files[right].path
		})
		candidate := files[0]
		state := aisnapshot.SessionUnknown
		prior := observer.previous[anonymousID]
		current := priorObservation{path: candidate.path, size: candidate.size, info: candidate.info}
		if len(files) == 1 {
			state, current = observer.inferState(
				snapshot,
				candidate,
				pathProcesses,
				processCandidates,
				prior,
				now,
			)
		}
		nextPrevious[anonymousID] = current
		var lastActivity *string
		if !candidate.modified.After(now) {
			canonical := candidate.modified.UTC().Truncate(time.Second).Format(time.RFC3339)
			lastActivity = &canonical
		}
		results = append(results, result{
			session: aisnapshot.Session{
				SchemaVersion:  aisnapshot.SchemaVersion{Major: 1, Minor: 0},
				ID:             anonymousID,
				ProviderID:     "codex",
				State:          state,
				Source:         aisnapshot.SessionSourceProcessJSONL,
				Confidence:     aisnapshot.ConfidenceInferred,
				LastActivityAt: lastActivity,
			},
			updated: candidate.modified,
		})
	}
	observer.previous = nextPrevious
	sort.Slice(results, func(left, right int) bool {
		if !results[left].updated.Equal(results[right].updated) {
			return results[left].updated.After(results[right].updated)
		}
		return results[left].session.ID < results[right].session.ID
	})
	if len(results) > maximumSessions {
		results = results[:maximumSessions]
	}
	sessions := make([]aisnapshot.Session, len(results))
	for index := range results {
		sessions[index] = results[index].session
	}
	return sessions, nil
}

func (observer *Observer) inferState(
	snapshot discoverySnapshot,
	candidate fileCandidate,
	pathProcesses map[string][]processIdentity,
	processCandidates map[processIdentity]int,
	prior priorObservation,
	now time.Time,
) (aisnapshot.SessionState, priorObservation) {
	current := priorObservation{path: candidate.path, size: candidate.size, info: candidate.info}
	if candidate.modified.After(now) {
		return aisnapshot.SessionUnknown, current
	}
	if snapshot.candidatesOverflow {
		return aisnapshot.SessionUnknown, current
	}
	recent := now.Sub(candidate.modified) <= observer.config.RecentWindow
	if snapshot.strength == mappingWeak {
		if len(snapshot.processes) != 0 {
			return aisnapshot.SessionUnknown, current
		}
		if recent {
			return aisnapshot.SessionRecent, current
		}
		return aisnapshot.SessionEnded, current
	}
	owners := pathProcesses[candidate.path]
	if len(owners) == 1 && processCandidates[owners[0]] == 1 {
		current.process = owners[0]
		current.hasOwner = true
		sameFile := prior.path == current.path
		if prior.info != nil && candidate.info != nil {
			sameFile = os.SameFile(prior.info, candidate.info)
		}
		if prior.hasOwner && prior.process == current.process && sameFile &&
			candidate.size > prior.size {
			return aisnapshot.SessionRunning, current
		}
		if recent {
			return aisnapshot.SessionRecent, current
		}
		return aisnapshot.SessionUnknown, current
	}
	if len(owners) != 0 {
		return aisnapshot.SessionUnknown, current
	}
	if recent {
		return aisnapshot.SessionRecent, current
	}
	return aisnapshot.SessionEnded, current
}

func parseSessionIdentifier(document []byte) (string, bool) {
	newline := bytes.IndexByte(document, '\n')
	if newline < 0 {
		return "", false
	}
	document = bytes.TrimSpace(document[:newline])
	if len(document) == 0 || !utf8.Valid(document) {
		return "", false
	}
	var envelope struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(document, &envelope) != nil || envelope.Type != "session_meta" {
		return "", false
	}
	var payload struct {
		ID        string `json:"id"`
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(envelope.Payload, &payload) != nil {
		return "", false
	}
	identifier := payload.ID
	if identifier == "" {
		identifier = payload.SessionID
	}
	if payload.ID != "" && payload.SessionID != "" && payload.ID != payload.SessionID {
		return "", false
	}
	if len(identifier) == 0 || len(identifier) > 256 || !utf8.ValidString(identifier) ||
		strings.TrimSpace(identifier) != identifier {
		return "", false
	}
	for _, character := range identifier {
		if character < 0x20 || character == 0x7f {
			return "", false
		}
	}
	return identifier, true
}

func cloneSessions(sessions []aisnapshot.Session) []aisnapshot.Session {
	cloned := make([]aisnapshot.Session, len(sessions))
	copy(cloned, sessions)
	for index := range cloned {
		source := sessions[index]
		cloned[index].DisplayName = clonePointer(source.DisplayName)
		cloned[index].StartedAt = clonePointer(source.StartedAt)
		cloned[index].StartedAtUnixMS = clonePointer(source.StartedAtUnixMS)
		cloned[index].LastActivityAt = clonePointer(source.LastActivityAt)
		cloned[index].LastActivityAtUnixMS = clonePointer(source.LastActivityAtUnixMS)
		cloned[index].DurationSeconds = clonePointer(source.DurationSeconds)
		cloned[index].TurnTokens = clonePointer(source.TurnTokens)
		cloned[index].ContextUsedBasisPoints = clonePointer(source.ContextUsedBasisPoints)
	}
	return cloned
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
