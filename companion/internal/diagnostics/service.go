package diagnostics

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
)

const (
	defaultRetention           = 7 * 24 * time.Hour
	defaultMaximumBytes        = 50 << 20
	defaultMaximumSegmentBytes = 256 << 10
	defaultMaximumExportBytes  = 8 << 20
	defaultQueueCapacity       = 1024
	maximumSegmentAge          = time.Hour
	maximumSegmentEntries      = 4096
)

var ErrUnavailable = errors.New("diagnostics unavailable")

type Config struct {
	Directory           string
	Retention           time.Duration
	MaximumBytes        int64
	MaximumSegmentBytes int
	MaximumExportBytes  int
	QueueCapacity       int
	Now                 func() time.Time
	FlushInterval       time.Duration
}

type workerCommand struct {
	event *Event
	flush chan error
	stop  chan error
}

type Service struct {
	directory           string
	retention           time.Duration
	maximumBytes        int64
	maximumSegmentBytes int
	maximumExportBytes  int
	now                 func() time.Time
	commands            chan workerCommand
	done                chan struct{}
	dropped             atomic.Uint32
	storageFaulted      atomic.Bool
	diskMu              sync.Mutex
	stateMu             sync.RWMutex
	closed              bool
	terminalErr         error
}

type Status struct {
	Available      bool   `json:"available"`
	RetentionDays  int    `json:"retention_days"`
	MaximumBytes   int64  `json:"maximum_bytes"`
	StoredBytes    int64  `json:"stored_bytes"`
	Segments       int    `json:"segments"`
	PendingDropped uint32 `json:"pending_dropped"`
}

func Open(config Config) (*Service, error) {
	if config.Directory == "" {
		return nil, errors.New("diagnostic directory is required")
	}
	if config.Retention == 0 {
		config.Retention = defaultRetention
	}
	if config.MaximumBytes == 0 {
		config.MaximumBytes = defaultMaximumBytes
	}
	if config.MaximumSegmentBytes == 0 {
		config.MaximumSegmentBytes = defaultMaximumSegmentBytes
	}
	if config.MaximumExportBytes == 0 {
		config.MaximumExportBytes = defaultMaximumExportBytes
	}
	if config.QueueCapacity == 0 {
		config.QueueCapacity = defaultQueueCapacity
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.FlushInterval == 0 {
		config.FlushInterval = time.Second
	}
	if config.Retention <= 0 || config.MaximumBytes < 512 ||
		config.MaximumSegmentBytes < 128 ||
		int64(config.MaximumSegmentBytes) > config.MaximumBytes ||
		config.MaximumExportBytes < 1024 || config.QueueCapacity < 1 ||
		config.FlushInterval <= 0 {
		return nil, errors.New("invalid diagnostic bounds")
	}
	directory := filepath.Clean(config.Directory)
	if err := protectedfile.EnsurePrivateDirectory(directory); err != nil {
		return nil, err
	}
	service := &Service{
		directory: directory, retention: config.Retention,
		maximumBytes:        config.MaximumBytes,
		maximumSegmentBytes: config.MaximumSegmentBytes,
		maximumExportBytes:  config.MaximumExportBytes, now: config.Now,
		commands: make(chan workerCommand, config.QueueCapacity), done: make(chan struct{}),
	}
	if err := service.enforceRetention(config.Now().UTC(), 0, ""); err != nil {
		return nil, err
	}
	go service.run(config.FlushInterval)
	return service, nil
}

func (service *Service) Record(event Event) bool {
	if service == nil || !validEvent(event) {
		return false
	}
	owned := event
	service.stateMu.RLock()
	defer service.stateMu.RUnlock()
	if service.closed {
		return false
	}
	select {
	case service.commands <- workerCommand{event: &owned}:
		return true
	default:
		service.dropped.Add(1)
		return false
	}
}

func (service *Service) Status() Status {
	if service == nil {
		return Status{}
	}
	status := Status{
		Available:      !service.storageFaulted.Load(),
		RetentionDays:  int(service.retention / (24 * time.Hour)),
		MaximumBytes:   service.maximumBytes,
		PendingDropped: service.dropped.Load(),
	}
	segments, err := service.segments()
	if err != nil {
		status.Available = false
		return status
	}
	status.Segments = len(segments)
	for _, segment := range segments {
		status.StoredBytes += segment.size
	}
	return status
}

func (service *Service) Recover(module Module, code Code) {
	if recover() != nil && service != nil {
		service.Record(Event{Level: LevelError, Module: module, Code: code})
	}
}

func (service *Service) Flush(ctx context.Context) error {
	if service == nil || ctx == nil {
		return ErrUnavailable
	}
	response := make(chan error, 1)
	service.stateMu.RLock()
	if service.closed {
		service.stateMu.RUnlock()
		return ErrUnavailable
	}
	select {
	case service.commands <- workerCommand{flush: response}:
		service.stateMu.RUnlock()
	case <-ctx.Done():
		service.stateMu.RUnlock()
		return ctx.Err()
	}
	select {
	case err := <-response:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (service *Service) Close(ctx context.Context) error {
	if service == nil || ctx == nil {
		return nil
	}
	service.stateMu.Lock()
	if service.closed {
		service.stateMu.Unlock()
		select {
		case <-service.done:
			service.stateMu.RLock()
			err := service.terminalErr
			service.stateMu.RUnlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	service.closed = true
	response := make(chan error, 1)
	select {
	case service.commands <- workerCommand{stop: response}:
		service.stateMu.Unlock()
	case <-ctx.Done():
		service.closed = false
		service.stateMu.Unlock()
		return ctx.Err()
	}
	select {
	case err := <-response:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (service *Service) run(flushInterval time.Duration) {
	defer close(service.done)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	batch := make([]storedEvent, 0, 64)
	batchBytes := 0
	activeCreated := time.Time{}
	activePath := ""
	activePersistedBytes := 0
	dirty := false
	lastSegmentNS := int64(-1)
	lastRetentionCheck := service.now().UTC()
	var sequence uint64
	persist := func(seal bool) (bool, error) {
		if len(batch) == 0 {
			return false, nil
		}
		if !dirty {
			if seal {
				batch = batch[:0]
				batchBytes = 0
				activeCreated = time.Time{}
				activePath = ""
				activePersistedBytes = 0
			}
			return false, nil
		}
		var buffer bytes.Buffer
		encoder := json.NewEncoder(&buffer)
		encoder.SetEscapeHTML(false)
		for _, event := range batch {
			if err := encoder.Encode(event); err != nil {
				return true, err
			}
		}
		if buffer.Len() > service.maximumSegmentBytes {
			return true, errors.New("diagnostic segment exceeds bound")
		}
		now := service.now().UTC()
		if now.Year() < 0 || now.Year() > 9999 || now.UnixNano() < 0 {
			return true, errors.New("diagnostic clock is outside the supported range")
		}
		if activeCreated.IsZero() {
			activeCreated = now
		}
		reserve := int64(buffer.Len() - activePersistedBytes)
		if reserve < 0 {
			reserve = 0
		}
		service.diskMu.Lock()
		defer service.diskMu.Unlock()
		if err := service.enforceRetentionLocked(now, reserve, activePath); err != nil {
			return true, err
		}
		if activePath == "" {
			if createdNS := activeCreated.UnixNano(); createdNS != lastSegmentNS {
				lastSegmentNS = createdNS
				sequence = 0
			}
			path, nextErr := service.nextSegmentPath(activeCreated, &sequence)
			if nextErr != nil {
				return true, nextErr
			}
			activePath = path
		}
		if _, err := protectedfile.Replace(activePath, buffer.Bytes()); err != nil {
			return true, err
		}
		activePersistedBytes = buffer.Len()
		dirty = false
		if seal {
			batch = batch[:0]
			batchBytes = 0
			activeCreated = time.Time{}
			activePath = ""
			activePersistedBytes = 0
		}
		return true, nil
	}
	commit := func(seal bool) (bool, error) {
		if count := service.dropped.Swap(0); count != 0 {
			now := service.now().UTC()
			droppedEvent := storedEvent{
				TimestampUTC: now.Format(time.RFC3339Nano),
				Event: Event{Level: LevelWarning, Module: ModuleDiagnostics,
					Code: CodeQueueOverflow, Count: count},
			}
			encoded, _ := json.Marshal(droppedEvent)
			if len(encoded)+1 > service.maximumSegmentBytes {
				service.dropped.Add(count)
				return true, errors.New("diagnostic overflow event exceeds segment bound")
			}
			if batchBytes+len(encoded)+1 > service.maximumSegmentBytes && len(batch) != 0 {
				if _, err := persist(true); err != nil {
					service.dropped.Add(count)
					return true, err
				}
			}
			if len(batch) == 0 {
				activeCreated = now
			}
			batch = append(batch, droppedEvent)
			batchBytes += len(encoded) + 1
			dirty = true
		}
		return persist(seal)
	}
	commitWithStatus := func(seal bool) error {
		attempted, err := commit(seal)
		if err != nil {
			service.storageFaulted.Store(true)
		} else if attempted {
			service.storageFaulted.Store(false)
		}
		return err
	}
	for {
		select {
		case command := <-service.commands:
			if command.event != nil {
				now := service.now().UTC()
				if len(batch) != 0 && !activeCreated.IsZero() &&
					now.Sub(activeCreated) >= maximumSegmentAge {
					if commitWithStatus(true) != nil {
						service.dropped.Add(1)
						continue
					}
				}
				stored := storedEvent{
					TimestampUTC: now.Format(time.RFC3339Nano),
					Event:        *command.event,
				}
				encoded, _ := json.Marshal(stored)
				if len(encoded)+1 > service.maximumSegmentBytes {
					service.dropped.Add(1)
					continue
				}
				if batchBytes+len(encoded)+1 > service.maximumSegmentBytes && len(batch) != 0 {
					if commitWithStatus(true) != nil {
						// Preserve the bounded batch for a later retry. The new
						// event is represented by the fixed queue-overflow count.
						service.dropped.Add(1)
						continue
					}
				}
				if len(batch) == 0 {
					activeCreated = now
				}
				batch = append(batch, stored)
				batchBytes += len(encoded) + 1
				dirty = true
			}
			if command.flush != nil {
				command.flush <- commitWithStatus(false)
			}
			if command.stop != nil {
				err := commitWithStatus(true)
				service.stateMu.Lock()
				service.terminalErr = err
				service.stateMu.Unlock()
				command.stop <- err
				return
			}
		case <-ticker.C:
			now := service.now().UTC()
			seal := len(batch) != 0 && !activeCreated.IsZero() &&
				now.Sub(activeCreated) >= maximumSegmentAge
			commitErr := commitWithStatus(seal)
			retentionInterval := min(service.retention, maximumSegmentAge)
			if commitErr == nil &&
				(now.Before(lastRetentionCheck) || now.Sub(lastRetentionCheck) >= retentionInterval) {
				if err := service.enforceRetention(now, 0, activePath); err != nil {
					service.storageFaulted.Store(true)
				} else {
					service.storageFaulted.Store(false)
					lastRetentionCheck = now
				}
			}
		}
	}
}

func (service *Service) nextSegmentPath(now time.Time, sequence *uint64) (string, error) {
	for range maximumSegmentEntries {
		(*sequence)++
		name := fmt.Sprintf("segment-%019d-%08d.jsonl", now.UnixNano(), *sequence)
		candidate := filepath.Join(service.directory, name)
		_, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("cannot allocate an immutable diagnostic segment name")
}

type segmentMeta struct {
	name      string
	createdNS int64
	size      int64
}

func (service *Service) segments() ([]segmentMeta, error) {
	service.diskMu.Lock()
	defer service.diskMu.Unlock()
	return service.segmentsLocked()
}

func (service *Service) segmentsLocked() ([]segmentMeta, error) {
	entries, err := os.ReadDir(service.directory)
	if err != nil {
		return nil, err
	}
	if len(entries) > maximumSegmentEntries {
		return nil, errors.New("too many diagnostic segments")
	}
	segments := make([]segmentMeta, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 ||
			!strings.HasPrefix(name, "segment-") ||
			!strings.HasSuffix(name, ".jsonl") {
			return nil, errors.New("unexpected diagnostic directory entry")
		}
		parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(name, "segment-"), ".jsonl"), "-")
		if len(parts) != 2 {
			return nil, errors.New("invalid diagnostic segment name")
		}
		createdNS, parseErr := strconv.ParseInt(parts[0], 10, 64)
		_, sequenceErr := strconv.ParseUint(parts[1], 10, 64)
		info, statErr := entry.Info()
		protectionErr := protectedfile.VerifyPrivate(filepath.Join(service.directory, name))
		if len(parts[0]) != 19 || len(parts[1]) != 8 || parseErr != nil ||
			sequenceErr != nil || statErr != nil || protectionErr != nil ||
			!info.Mode().IsRegular() ||
			info.Size() < 0 || info.Size() > int64(service.maximumSegmentBytes) {
			return nil, errors.New("invalid diagnostic segment")
		}
		segments = append(segments, segmentMeta{name: name, createdNS: createdNS, size: info.Size()})
	}
	sort.Slice(segments, func(i, j int) bool { return segments[i].name < segments[j].name })
	return segments, nil
}

func (service *Service) enforceRetention(
	now time.Time,
	reserve int64,
	preservePath string,
) error {
	service.diskMu.Lock()
	defer service.diskMu.Unlock()
	return service.enforceRetentionLocked(now, reserve, preservePath)
}

func (service *Service) enforceRetentionLocked(
	now time.Time,
	reserve int64,
	preservePath string,
) error {
	segments, err := service.segmentsLocked()
	if err != nil {
		return err
	}
	cutoff := now.Add(-service.retention).UnixNano()
	var total int64
	for _, segment := range segments {
		total += segment.size
	}
	remaining := len(segments)
	for _, segment := range segments {
		if preservePath != "" && segment.name == filepath.Base(preservePath) {
			continue
		}
		if segment.createdNS+maximumSegmentAge.Nanoseconds() > cutoff &&
			total+reserve <= service.maximumBytes {
			continue
		}
		if removeErr := os.Remove(filepath.Join(service.directory, segment.name)); removeErr != nil {
			return removeErr
		}
		total -= segment.size
		remaining--
	}
	if remaining >= maximumSegmentEntries {
		return errors.New("diagnostic segment count reached its safety bound")
	}
	return nil
}

func (service *Service) snapshot(
	ctx context.Context,
	since time.Time,
	maximumBytes int,
) ([]byte, bool, error) {
	if maximumBytes <= 0 {
		return nil, false, errors.New("diagnostic snapshot size must be positive")
	}
	service.diskMu.Lock()
	defer service.diskMu.Unlock()
	segments, err := service.segmentsLocked()
	if err != nil {
		return nil, false, err
	}
	lines := make([][]byte, 0, 128)
	selectedBytes := 0
	truncated := false
	for _, segment := range segments {
		if err = ctx.Err(); err != nil {
			return nil, false, err
		}
		contents, readErr := protectedfile.Read(
			filepath.Join(service.directory, segment.name),
			service.maximumSegmentBytes,
		)
		if readErr != nil {
			return nil, false, readErr
		}
		scanner := bufio.NewScanner(bytes.NewReader(contents))
		scanner.Buffer(make([]byte, 1024), service.maximumSegmentBytes)
		for scanner.Scan() {
			var event storedEvent
			decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&event) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
				!validEvent(event.Event) {
				return nil, false, errors.New("invalid persisted diagnostic event")
			}
			parsed, parseErr := time.Parse(time.RFC3339Nano, event.TimestampUTC)
			if parseErr != nil || parsed.Location() != time.UTC ||
				parsed.Format(time.RFC3339Nano) != event.TimestampUTC {
				return nil, false, errors.New("invalid diagnostic event timestamp")
			}
			if parsed.Before(since) {
				continue
			}
			line, _ := json.Marshal(event)
			if len(line)+1 > maximumBytes {
				truncated = true
				continue
			}
			owned := append([]byte(nil), line...)
			lines = append(lines, owned)
			selectedBytes += len(owned) + 1
			for selectedBytes > maximumBytes {
				selectedBytes -= len(lines[0]) + 1
				clear(lines[0])
				lines[0] = nil
				lines = lines[1:]
				truncated = true
			}
		}
		if err = scanner.Err(); err != nil {
			return nil, false, err
		}
	}
	var output bytes.Buffer
	output.Grow(selectedBytes)
	for _, line := range lines {
		output.Write(line)
		output.WriteByte('\n')
	}
	return output.Bytes(), truncated, nil
}
