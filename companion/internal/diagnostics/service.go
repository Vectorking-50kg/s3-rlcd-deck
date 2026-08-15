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
	stateMu             sync.RWMutex
	closed              bool
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
	if err := service.enforceRetention(config.Now().UTC(), 0); err != nil {
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
			return nil
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
	var sequence uint64
	persist := func() error {
		if len(batch) == 0 {
			return nil
		}
		var buffer bytes.Buffer
		encoder := json.NewEncoder(&buffer)
		encoder.SetEscapeHTML(false)
		for _, event := range batch {
			if err := encoder.Encode(event); err != nil {
				return err
			}
		}
		if buffer.Len() > service.maximumSegmentBytes {
			return errors.New("diagnostic segment exceeds bound")
		}
		now := service.now().UTC()
		if now.Year() < 0 || now.Year() > 9999 || now.UnixNano() < 0 {
			return errors.New("diagnostic clock is outside the supported range")
		}
		if err := service.enforceRetention(now, int64(buffer.Len())); err != nil {
			return err
		}
		path, nextErr := service.nextSegmentPath(now, &sequence)
		if nextErr != nil {
			return nextErr
		}
		if _, err := protectedfile.Replace(path, buffer.Bytes()); err != nil {
			return err
		}
		batch = batch[:0]
		batchBytes = 0
		return nil
	}
	commit := func() error {
		if count := service.dropped.Swap(0); count != 0 {
			droppedEvent := storedEvent{
				TimestampUTC: service.now().UTC().Format(time.RFC3339Nano),
				Event: Event{Level: LevelWarning, Module: ModuleDiagnostics,
					Code: CodeQueueOverflow, Count: count},
			}
			encoded, _ := json.Marshal(droppedEvent)
			if len(encoded)+1 > service.maximumSegmentBytes {
				service.dropped.Add(count)
				return errors.New("diagnostic overflow event exceeds segment bound")
			}
			if batchBytes+len(encoded)+1 > service.maximumSegmentBytes && len(batch) != 0 {
				if err := persist(); err != nil {
					service.dropped.Add(count)
					return err
				}
			}
			batch = append(batch, droppedEvent)
			batchBytes += len(encoded) + 1
		}
		return persist()
	}
	commitWithStatus := func() error {
		err := commit()
		service.storageFaulted.Store(err != nil)
		return err
	}
	for {
		select {
		case command := <-service.commands:
			if command.event != nil {
				stored := storedEvent{
					TimestampUTC: service.now().UTC().Format(time.RFC3339Nano),
					Event:        *command.event,
				}
				encoded, _ := json.Marshal(stored)
				if len(encoded)+1 > service.maximumSegmentBytes {
					service.dropped.Add(1)
					continue
				}
				if batchBytes+len(encoded)+1 > service.maximumSegmentBytes && len(batch) != 0 {
					if commitWithStatus() != nil {
						// Preserve the bounded batch for a later retry. The new
						// event is represented by the fixed queue-overflow count.
						service.dropped.Add(1)
						continue
					}
				}
				batch = append(batch, stored)
				batchBytes += len(encoded) + 1
			}
			if command.flush != nil {
				command.flush <- commitWithStatus()
			}
			if command.stop != nil {
				command.stop <- commitWithStatus()
				return
			}
		case <-ticker.C:
			_ = commitWithStatus()
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
		if len(parts[0]) != 19 || len(parts[1]) != 8 || parseErr != nil ||
			sequenceErr != nil || statErr != nil || !info.Mode().IsRegular() ||
			info.Size() < 0 || info.Size() > int64(service.maximumSegmentBytes) {
			return nil, errors.New("invalid diagnostic segment")
		}
		segments = append(segments, segmentMeta{name: name, createdNS: createdNS, size: info.Size()})
	}
	sort.Slice(segments, func(i, j int) bool { return segments[i].name < segments[j].name })
	return segments, nil
}

func (service *Service) enforceRetention(now time.Time, reserve int64) error {
	segments, err := service.segments()
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
		if segment.createdNS >= cutoff && total+reserve <= service.maximumBytes &&
			remaining < maximumSegmentEntries {
			continue
		}
		if removeErr := os.Remove(filepath.Join(service.directory, segment.name)); removeErr != nil {
			return removeErr
		}
		total -= segment.size
		remaining--
	}
	return nil
}

func (service *Service) snapshot(ctx context.Context, since time.Time, maximumBytes int) ([]byte, error) {
	segments, err := service.segments()
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	for _, segment := range segments {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if segment.createdNS < since.UnixNano() {
			continue
		}
		contents, readErr := protectedfile.Read(
			filepath.Join(service.directory, segment.name),
			service.maximumSegmentBytes,
		)
		if readErr != nil {
			return nil, readErr
		}
		scanner := bufio.NewScanner(bytes.NewReader(contents))
		scanner.Buffer(make([]byte, 1024), service.maximumSegmentBytes)
		for scanner.Scan() {
			var event storedEvent
			decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&event) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
				!validEvent(event.Event) {
				return nil, errors.New("invalid persisted diagnostic event")
			}
			parsed, parseErr := time.Parse(time.RFC3339Nano, event.TimestampUTC)
			if parseErr != nil || parsed.Location() != time.UTC ||
				parsed.Format(time.RFC3339Nano) != event.TimestampUTC {
				return nil, errors.New("invalid diagnostic event timestamp")
			}
			if parsed.Before(since) {
				continue
			}
			line, _ := json.Marshal(event)
			if output.Len()+len(line)+1 > maximumBytes {
				return output.Bytes(), nil
			}
			output.Write(line)
			output.WriteByte('\n')
		}
		if err = scanner.Err(); err != nil {
			return nil, err
		}
	}
	return output.Bytes(), nil
}
