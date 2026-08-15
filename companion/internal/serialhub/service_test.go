package serialhub

import (
	"errors"
	"testing"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/serialprotocol"
)

func TestServiceKeepsOneCurrentDeviceSessionAcrossReconnect(t *testing.T) {
	ring, err := NewRing(Config{CapacityBytes: 32, MaximumFrames: 8, MaximumObservers: 2})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceConfig{Ring: ring})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if err = service.Reconcile("deck-a", 10, StateUSBTX); err != nil {
		t.Fatal(err)
	}
	if err = service.Ingest("deck-a", serialFrame(10, 1, "one")); err != nil {
		t.Fatal(err)
	}
	// Reconciliation after a WSS reconnect is idempotent and preserves history.
	if err = service.Reconcile("deck-a", 10, StateUSBTX); err != nil {
		t.Fatal(err)
	}
	if got := service.Status(); got.DeviceID != "deck-a" || got.SessionID != 10 || got.BufferedBytes != 3 {
		t.Fatalf("status after reconnect = %#v", got)
	}
	if err = service.Reconcile("deck-b", 20, StateUSBTX); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("second device error = %v", err)
	}
	if err = service.Reconcile("deck-a", 0, StateDisarmed); err != nil {
		t.Fatal(err)
	}
	if got := service.Status(); got.DeviceID != "" || got.SessionID != 0 || got.BufferedBytes != 0 {
		t.Fatalf("status after disarm = %#v", got)
	}
}

func TestServiceRejectsWrongDeviceAndInvalidState(t *testing.T) {
	service, err := NewService(ServiceConfig{RingConfig: Config{CapacityBytes: 32, MaximumFrames: 8, MaximumObservers: 2}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if err = service.Reconcile("deck-a", 0, StateUSBTX); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("active zero Session error = %v", err)
	}
	if err = service.Reconcile("deck-a", 1, State("future")); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("unknown state error = %v", err)
	}
	if err = service.Reconcile("deck-a", 1, StateWebTX); err != nil {
		t.Fatal(err)
	}
	if err = service.Ingest("deck-b", serialFrame(1, 1, "x")); !errors.Is(err, ErrWrongSession) {
		t.Fatalf("wrong device ingest error = %v", err)
	}
}

func TestServiceBuildsWebFramesOnlyForTheCurrentLeaseHolder(t *testing.T) {
	service, err := NewService(ServiceConfig{RingConfig: Config{CapacityBytes: 32, MaximumFrames: 8, MaximumObservers: 2}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if err = service.Reconcile("deck-a", 5, StateWebTX); err != nil {
		t.Fatal(err)
	}
	request, err := service.Leases().Acquire("browser-a", 5)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Leases().ApplyOwnerResult(OwnerResult{
		SessionID: 5, RequestID: request.RequestID, Owner: OwnerWeb, LeaseID: request.RequestID,
	}); err != nil {
		t.Fatal(err)
	}
	deviceID, frame, err := service.BuildWebFrame("browser-a", request.RequestID, []byte{0, 0xff})
	if err != nil || deviceID != "deck-a" || frame.Channel != serialprotocol.ChannelWebTX ||
		frame.SessionID != 5 || frame.Sequence != 1 || len(frame.Payload) != 2 {
		t.Fatalf("BuildWebFrame() device=%q frame=%#v error=%v", deviceID, frame, err)
	}
	if _, _, err = service.BuildWebFrame("browser-b", request.RequestID, []byte("x")); !errors.Is(err, ErrLeaseNotActive) {
		t.Fatalf("non-holder BuildWebFrame() error=%v", err)
	}
}
