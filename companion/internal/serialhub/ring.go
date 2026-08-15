package serialhub

import (
	"errors"
	"sync"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/serialprotocol"
)

const (
	DefaultCapacityBytes  = 8 << 20
	DefaultMaximumFrames  = 64 << 10
	DefaultMaximumReaders = 64
)

var (
	ErrClosed           = errors.New("Serial Hub is closed")
	ErrInvalidConfig    = errors.New("invalid Serial Hub configuration")
	ErrInvalidSession   = errors.New("invalid Serial Session")
	ErrWrongSession     = errors.New("wrong Serial Session")
	ErrOutOfOrder       = errors.New("out-of-order Serial frame")
	ErrRangeTooLarge    = errors.New("Serial range exceeds the request limit")
	ErrRangeUnavailable = errors.New("Serial range is no longer available")
	ErrObserverLimit    = errors.New("Serial observer limit reached")
	ErrObserverNotFound = errors.New("Serial observer not found")
)

type Config struct {
	CapacityBytes    int
	MaximumFrames    int
	MaximumObservers int
}

type record struct {
	ordinal uint64
	frame   serialprotocol.Frame
}

type observer struct {
	nextOrdinal      uint64
	overwrittenBytes uint64
}

type Ring struct {
	mu sync.Mutex

	capacityBytes     int
	maximumFrames     int
	maximumObservers  int
	records           []record
	head              int
	count             int
	bufferedBytes     int
	nextOrdinal       uint64
	nextObserverID    uint64
	observers         map[uint64]*observer
	sessionID         uint64
	lastSequence      uint64
	hasLastSequence   bool
	lastMonotonicMS   uint64
	hasLastMonotonic  bool
	overwrittenBytes  uint64
	overwrittenFrames uint64
	closed            bool
}

type Stats struct {
	SessionID         uint64
	BufferedBytes     int
	BufferedFrames    int
	OverwrittenBytes  uint64
	OverwrittenFrames uint64
	Observers         int
	OldestSequence    uint64
	NewestSequence    uint64
}

type ObserverRead struct {
	SessionID        uint64
	Frames           []serialprotocol.Frame
	OverwrittenBytes uint64
}

func NewRing(config Config) (*Ring, error) {
	if config.CapacityBytes == 0 {
		config.CapacityBytes = DefaultCapacityBytes
	}
	if config.MaximumFrames == 0 {
		config.MaximumFrames = DefaultMaximumFrames
	}
	if config.MaximumObservers == 0 {
		config.MaximumObservers = DefaultMaximumReaders
	}
	if config.CapacityBytes <= 0 || config.CapacityBytes > DefaultCapacityBytes ||
		config.MaximumFrames <= 0 || config.MaximumFrames > DefaultMaximumFrames ||
		config.MaximumObservers <= 0 || config.MaximumObservers > DefaultMaximumReaders {
		return nil, ErrInvalidConfig
	}
	return &Ring{
		capacityBytes:    config.CapacityBytes,
		maximumFrames:    config.MaximumFrames,
		maximumObservers: config.MaximumObservers,
		records:          make([]record, config.MaximumFrames),
		nextOrdinal:      1,
		nextObserverID:   1,
		observers:        make(map[uint64]*observer),
	}, nil
}

func (ring *Ring) Begin(sessionID uint64) error {
	ring.mu.Lock()
	defer ring.mu.Unlock()
	if ring.closed {
		return ErrClosed
	}
	if sessionID == 0 {
		return ErrInvalidSession
	}
	if ring.sessionID == sessionID {
		return nil
	}
	ring.clearSessionLocked()
	ring.sessionID = sessionID
	return nil
}

func (ring *Ring) End(sessionID uint64) error {
	ring.mu.Lock()
	defer ring.mu.Unlock()
	if ring.closed {
		return ErrClosed
	}
	if sessionID == 0 || ring.sessionID != sessionID {
		return ErrWrongSession
	}
	ring.clearSessionLocked()
	return nil
}

func (ring *Ring) Ingest(frame serialprotocol.Frame) error {
	ring.mu.Lock()
	defer ring.mu.Unlock()
	if ring.closed {
		return ErrClosed
	}
	if ring.sessionID == 0 || frame.SessionID != ring.sessionID {
		return ErrWrongSession
	}
	if frame.Channel != serialprotocol.ChannelTargetRX || frame.Sequence == 0 ||
		len(frame.Payload) == 0 || len(frame.Payload) > serialprotocol.MaxPayloadBytes {
		return ErrOutOfOrder
	}
	if len(frame.Payload) > ring.capacityBytes {
		return ErrRangeTooLarge
	}
	if ring.hasLastSequence && !sequenceAfter(frame.Sequence, ring.lastSequence) {
		return ErrOutOfOrder
	}
	if ring.hasLastMonotonic && frame.MonotonicMS < ring.lastMonotonicMS {
		return ErrOutOfOrder
	}
	for ring.count == ring.maximumFrames || ring.bufferedBytes+len(frame.Payload) > ring.capacityBytes {
		ring.evictOldestLocked()
	}
	owned := serialprotocol.Frame{
		Channel:     frame.Channel,
		SessionID:   frame.SessionID,
		Sequence:    frame.Sequence,
		MonotonicMS: frame.MonotonicMS,
		Payload:     append([]byte(nil), frame.Payload...),
	}
	index := (ring.head + ring.count) % len(ring.records)
	ring.records[index] = record{ordinal: ring.nextOrdinal, frame: owned}
	ring.count++
	ring.bufferedBytes += len(owned.Payload)
	ring.nextOrdinal++
	if ring.nextOrdinal == 0 {
		ring.nextOrdinal = 1
	}
	ring.lastSequence = frame.Sequence
	ring.hasLastSequence = true
	ring.lastMonotonicMS = frame.MonotonicMS
	ring.hasLastMonotonic = true
	return nil
}

func sequenceAfter(candidate, previous uint64) bool {
	return candidate != 0 && previous != 0 && candidate != previous && candidate-previous < 1<<63
}

func (ring *Ring) OpenObserver(sessionID uint64) (uint64, error) {
	ring.mu.Lock()
	defer ring.mu.Unlock()
	if ring.closed {
		return 0, ErrClosed
	}
	if sessionID == 0 || sessionID != ring.sessionID {
		return 0, ErrWrongSession
	}
	if len(ring.observers) >= ring.maximumObservers {
		return 0, ErrObserverLimit
	}
	id := ring.nextObserverID
	ring.nextObserverID++
	if ring.nextObserverID == 0 {
		ring.nextObserverID = 1
	}
	next := ring.nextOrdinal
	if ring.count != 0 {
		next = ring.records[ring.head].ordinal
	}
	ring.observers[id] = &observer{nextOrdinal: next}
	return id, nil
}

func (ring *Ring) CloseObserver(observerID uint64) {
	ring.mu.Lock()
	delete(ring.observers, observerID)
	ring.mu.Unlock()
}

func (ring *Ring) ReadObserver(observerID uint64, maximumBytes, maximumFrames int) (ObserverRead, error) {
	ring.mu.Lock()
	defer ring.mu.Unlock()
	if ring.closed {
		return ObserverRead{}, ErrClosed
	}
	reader, exists := ring.observers[observerID]
	if !exists {
		return ObserverRead{}, ErrObserverNotFound
	}
	if maximumBytes <= 0 || maximumFrames <= 0 {
		return ObserverRead{}, ErrRangeTooLarge
	}
	result := ObserverRead{
		SessionID:        ring.sessionID,
		OverwrittenBytes: reader.overwrittenBytes,
		Frames:           make([]serialprotocol.Frame, 0, maximumFrames),
	}
	used := 0
	for offset := 0; offset < ring.count && len(result.Frames) < maximumFrames; offset++ {
		stored := ring.records[(ring.head+offset)%len(ring.records)]
		if stored.ordinal < reader.nextOrdinal {
			continue
		}
		if used+len(stored.frame.Payload) > maximumBytes {
			if len(result.Frames) == 0 {
				return ObserverRead{}, ErrRangeTooLarge
			}
			break
		}
		result.Frames = append(result.Frames, cloneFrame(stored.frame))
		used += len(stored.frame.Payload)
		reader.nextOrdinal = stored.ordinal + 1
	}
	return result, nil
}

func (ring *Ring) Download(sessionID, fromSequence uint64, maximumBytes int) ([]byte, error) {
	ring.mu.Lock()
	defer ring.mu.Unlock()
	if ring.closed {
		return nil, ErrClosed
	}
	if sessionID == 0 || sessionID != ring.sessionID {
		return nil, ErrWrongSession
	}
	if maximumBytes <= 0 || maximumBytes > ring.capacityBytes {
		return nil, ErrRangeTooLarge
	}
	start := fromSequence == 0
	total := 0
	for offset := 0; offset < ring.count; offset++ {
		stored := ring.records[(ring.head+offset)%len(ring.records)]
		if !start && stored.frame.Sequence == fromSequence {
			start = true
		}
		if start {
			total += len(stored.frame.Payload)
			if total > maximumBytes {
				return nil, ErrRangeTooLarge
			}
		}
	}
	if !start {
		return nil, ErrRangeUnavailable
	}
	result := make([]byte, 0, total)
	start = fromSequence == 0
	for offset := 0; offset < ring.count; offset++ {
		stored := ring.records[(ring.head+offset)%len(ring.records)]
		if !start && stored.frame.Sequence == fromSequence {
			start = true
		}
		if start {
			result = append(result, stored.frame.Payload...)
		}
	}
	return result, nil
}

func (ring *Ring) Stats() Stats {
	ring.mu.Lock()
	defer ring.mu.Unlock()
	stats := Stats{
		SessionID:         ring.sessionID,
		BufferedBytes:     ring.bufferedBytes,
		BufferedFrames:    ring.count,
		OverwrittenBytes:  ring.overwrittenBytes,
		OverwrittenFrames: ring.overwrittenFrames,
		Observers:         len(ring.observers),
	}
	if ring.count != 0 {
		stats.OldestSequence = ring.records[ring.head].frame.Sequence
		stats.NewestSequence = ring.records[(ring.head+ring.count-1)%len(ring.records)].frame.Sequence
	}
	return stats
}

func (ring *Ring) Close() {
	ring.mu.Lock()
	if !ring.closed {
		ring.clearSessionLocked()
		ring.closed = true
	}
	ring.mu.Unlock()
}

func (ring *Ring) evictOldestLocked() {
	if ring.count == 0 {
		return
	}
	stored := &ring.records[ring.head]
	length := len(stored.frame.Payload)
	for _, reader := range ring.observers {
		if reader.nextOrdinal <= stored.ordinal {
			reader.overwrittenBytes += uint64(length)
			reader.nextOrdinal = stored.ordinal + 1
		}
	}
	ring.bufferedBytes -= length
	ring.overwrittenBytes += uint64(length)
	ring.overwrittenFrames++
	clear(stored.frame.Payload)
	*stored = record{}
	ring.head = (ring.head + 1) % len(ring.records)
	ring.count--
}

func (ring *Ring) clearSessionLocked() {
	for ring.count != 0 {
		stored := &ring.records[ring.head]
		clear(stored.frame.Payload)
		*stored = record{}
		ring.head = (ring.head + 1) % len(ring.records)
		ring.count--
	}
	ring.head = 0
	ring.bufferedBytes = 0
	ring.sessionID = 0
	ring.lastSequence = 0
	ring.hasLastSequence = false
	ring.lastMonotonicMS = 0
	ring.hasLastMonotonic = false
	ring.overwrittenBytes = 0
	ring.overwrittenFrames = 0
	clear(ring.observers)
}

func cloneFrame(frame serialprotocol.Frame) serialprotocol.Frame {
	frame.Payload = append([]byte(nil), frame.Payload...)
	return frame
}
