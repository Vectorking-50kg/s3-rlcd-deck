package serialhub

import (
	"bytes"
	"errors"
	"testing"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/serialprotocol"
)

func serialFrame(sessionID, sequence uint64, payload string) serialprotocol.Frame {
	return serialprotocol.Frame{
		Channel:     serialprotocol.ChannelTargetRX,
		SessionID:   sessionID,
		Sequence:    sequence,
		MonotonicMS: sequence * 10,
		Payload:     []byte(payload),
	}
}

func TestRingOwnsOnlyCurrentSessionAndRejectsInvalidOrder(t *testing.T) {
	ring, err := NewRing(Config{CapacityBytes: 64, MaximumFrames: 8, MaximumObservers: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer ring.Close()
	if err = ring.Begin(10); err != nil {
		t.Fatal(err)
	}
	if err = ring.Ingest(serialFrame(10, 8, "alpha")); err != nil {
		t.Fatal(err)
	}
	if err = ring.Ingest(serialFrame(10, 7, "old")); !errors.Is(err, ErrOutOfOrder) {
		t.Fatalf("old sequence error = %v", err)
	}
	regressingClock := serialFrame(10, 9, "clock")
	regressingClock.MonotonicMS = 1
	if err = ring.Ingest(regressingClock); !errors.Is(err, ErrOutOfOrder) {
		t.Fatalf("regressing device monotonic error = %v", err)
	}
	if err = ring.Ingest(serialFrame(11, 9, "wrong")); !errors.Is(err, ErrWrongSession) {
		t.Fatalf("wrong session error = %v", err)
	}
	if err = ring.Begin(11); err != nil {
		t.Fatal(err)
	}
	if got := ring.Stats(); got.SessionID != 11 || got.BufferedBytes != 0 || got.BufferedFrames != 0 {
		t.Fatalf("stats after new Session = %#v", got)
	}
	if err = ring.End(10); !errors.Is(err, ErrWrongSession) {
		t.Fatalf("End(old session) error = %v", err)
	}
	if err = ring.End(11); err != nil {
		t.Fatal(err)
	}
	if got := ring.Stats(); got.SessionID != 0 || got.BufferedBytes != 0 {
		t.Fatalf("stats after end = %#v", got)
	}
}

func TestRingAcceptsSequenceWrapAndObserversDoNotBlockIngest(t *testing.T) {
	ring, err := NewRing(Config{CapacityBytes: 6, MaximumFrames: 3, MaximumObservers: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer ring.Close()
	if err = ring.Begin(7); err != nil {
		t.Fatal(err)
	}
	observerA, err := ring.OpenObserver(7)
	if err != nil {
		t.Fatal(err)
	}
	observerB, err := ring.OpenObserver(7)
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range []serialprotocol.Frame{
		{Channel: serialprotocol.ChannelTargetRX, SessionID: 7, Sequence: ^uint64(0) - 1, MonotonicMS: 1, Payload: []byte("aa")},
		{Channel: serialprotocol.ChannelTargetRX, SessionID: 7, Sequence: ^uint64(0), MonotonicMS: 2, Payload: []byte("bb")},
		{Channel: serialprotocol.ChannelTargetRX, SessionID: 7, Sequence: 1, MonotonicMS: 3, Payload: []byte("cc")},
	} {
		if err = ring.Ingest(frame); err != nil {
			t.Fatal(err)
		}
	}
	first, err := ring.ReadObserver(observerA, 2, 1)
	if err != nil || len(first.Frames) != 1 || string(first.Frames[0].Payload) != "aa" {
		t.Fatalf("observer A first read = %#v, %v", first, err)
	}
	wrapped := serialFrame(7, 2, "dd")
	wrapped.MonotonicMS = 4
	if err = ring.Ingest(wrapped); err != nil {
		t.Fatal(err)
	}
	afterOverwrite, err := ring.ReadObserver(observerA, 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	if afterOverwrite.OverwrittenBytes != 0 || len(afterOverwrite.Frames) != 3 {
		t.Fatalf("observer A = %#v", afterOverwrite)
	}
	slow, err := ring.ReadObserver(observerB, 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	if slow.OverwrittenBytes != 2 || len(slow.Frames) != 3 || string(slow.Frames[0].Payload) != "bb" {
		t.Fatalf("slow observer = %#v", slow)
	}
}

func TestRingDownloadIsBoundedAndCurrentSessionOnly(t *testing.T) {
	ring, err := NewRing(Config{CapacityBytes: 32, MaximumFrames: 8, MaximumObservers: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer ring.Close()
	if err = ring.Begin(22); err != nil {
		t.Fatal(err)
	}
	for sequence, payload := range []string{"ab", "cd", "ef"} {
		if err = ring.Ingest(serialFrame(22, uint64(sequence+1), payload)); err != nil {
			t.Fatal(err)
		}
	}
	download, err := ring.Download(22, 2, 4)
	if err != nil || !bytes.Equal(download, []byte("cdef")) {
		t.Fatalf("Download() = %q, %v", download, err)
	}
	if _, err = ring.Download(22, 1, 3); !errors.Is(err, ErrRangeTooLarge) {
		t.Fatalf("bounded Download() error = %v", err)
	}
	if _, err = ring.Download(21, 0, 8); !errors.Is(err, ErrWrongSession) {
		t.Fatalf("wrong-session Download() error = %v", err)
	}
}

func TestRingCloseZeroesOwnedPayloadAndRejectsFurtherWork(t *testing.T) {
	ring, err := NewRing(Config{CapacityBytes: 32, MaximumFrames: 8, MaximumObservers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err = ring.Begin(1); err != nil {
		t.Fatal(err)
	}
	if err = ring.Ingest(serialFrame(1, 1, "PRIVATE_SERIAL_BYTES")); err != nil {
		t.Fatal(err)
	}
	ring.Close()
	if got := ring.Stats(); got.SessionID != 0 || got.BufferedBytes != 0 {
		t.Fatalf("closed stats = %#v", got)
	}
	if err = ring.Begin(2); !errors.Is(err, ErrClosed) {
		t.Fatalf("Begin after Close error = %v", err)
	}
}

func TestDefaultRingIsBoundedToEightMiBUnderSustainedIngest(t *testing.T) {
	ring, err := NewRing(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer ring.Close()
	if err = ring.Begin(44); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte{0xa5}, serialprotocol.MaxPayloadBytes)
	frameCount := DefaultCapacityBytes/serialprotocol.MaxPayloadBytes + 2
	for index := 1; index <= frameCount; index++ {
		if err = ring.Ingest(serialprotocol.Frame{
			Channel: serialprotocol.ChannelTargetRX, SessionID: 44,
			Sequence: uint64(index), MonotonicMS: uint64(index), Payload: payload,
		}); err != nil {
			t.Fatalf("Ingest(%d) error = %v", index, err)
		}
	}
	stats := ring.Stats()
	if stats.BufferedBytes != DefaultCapacityBytes ||
		stats.BufferedFrames != DefaultCapacityBytes/serialprotocol.MaxPayloadBytes ||
		stats.OverwrittenBytes != 2*serialprotocol.MaxPayloadBytes {
		t.Fatalf("8 MiB ring stats = %#v", stats)
	}
}

func TestRingRejectsConfigurationBeyondProductionBounds(t *testing.T) {
	for _, config := range []Config{
		{CapacityBytes: DefaultCapacityBytes + 1},
		{MaximumFrames: DefaultMaximumFrames + 1},
		{MaximumObservers: DefaultMaximumReaders + 1},
	} {
		if _, err := NewRing(config); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("NewRing(%#v) error = %v", config, err)
		}
	}
}
