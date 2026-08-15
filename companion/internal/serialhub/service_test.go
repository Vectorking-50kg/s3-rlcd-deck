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
	request, err := service.AcquireLease("browser-a", 5)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ApplyOwnerResult("deck-a", OwnerResult{
		SessionID: 5, RequestID: request.RequestID, Owner: OwnerWeb, LeaseID: request.RequestID,
	}, true); err != nil {
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

func TestServiceEndsTheOldLeaseWhenDeckStartsANewSession(t *testing.T) {
	service, err := NewService(ServiceConfig{RingConfig: Config{
		CapacityBytes: 32, MaximumFrames: 8, MaximumObservers: 2,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if err = service.Reconcile("deck-a", 5, StateUSBTX); err != nil {
		t.Fatal(err)
	}
	request, err := service.AcquireLease("browser-a", 5)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ApplyOwnerResult("deck-a", OwnerResult{
		SessionID: 5, RequestID: request.RequestID, Owner: OwnerWeb, LeaseID: request.RequestID,
	}, true); err != nil {
		t.Fatal(err)
	}

	// A Deck restart or a direct A -> B Serial Session switch is an
	// authoritative USB state for B. It must not inherit A's browser Lease.
	if err = service.Reconcile("deck-a", 6, StateUSBTX); err != nil {
		t.Fatal(err)
	}
	status := service.Status()
	if status.SessionID != 6 || status.State != StateUSBTX ||
		status.Lease.SessionID != 6 || status.Lease.Owner != OwnerUSB ||
		status.Lease.ClientID != "" || status.Lease.LeaseID != 0 {
		t.Fatalf("new Session inherited old Lease: %#v", status)
	}
	if _, err = service.AcquireLease("browser-b", 6); err != nil {
		t.Fatalf("new Session could not acquire a fresh Lease: %v", err)
	}
}

func TestSameSessionStateCannotConsumeAPendingExactOwnerRequest(t *testing.T) {
	service, err := NewService(ServiceConfig{RingConfig: Config{
		CapacityBytes: 32, MaximumFrames: 8, MaximumObservers: 2,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if err = service.Reconcile("deck-a", 7, StateUSBTX); err != nil {
		t.Fatal(err)
	}
	request, err := service.AcquireLease("browser", 7)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Reconcile("deck-a", 7, StateUSBTX); err != nil {
		t.Fatal(err)
	}
	pending, clientID, exists := service.PendingOwnerRequest()
	status := service.Status()
	if !exists || pending != request || clientID != "browser" ||
		status.Lease.Owner != OwnerTransitioning {
		t.Fatalf("same-Session state consumed pending request: status=%#v pending=%#v", status, pending)
	}
}

func TestSameSessionUSBStateDoesNotPublishUSBWhileRevokeResultIsPending(t *testing.T) {
	service, err := NewService(ServiceConfig{RingConfig: Config{
		CapacityBytes: 32, MaximumFrames: 8, MaximumObservers: 2,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if err = service.Reconcile("deck-a", 8, StateUSBTX); err != nil {
		t.Fatal(err)
	}
	acquire, err := service.AcquireLease("browser", 8)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ApplyOwnerResult("deck-a", OwnerResult{
		SessionID: 8, RequestID: acquire.RequestID, Owner: OwnerWeb,
		LeaseID: acquire.RequestID,
	}, true); err != nil {
		t.Fatal(err)
	}
	revoke, err := service.DisconnectLease("browser")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Reconcile("deck-a", 8, StateUSBTX); err != nil {
		t.Fatal(err)
	}
	pending, _, exists := service.PendingOwnerRequest()
	status := service.Status()
	if !exists || pending != revoke || status.State != StateWebTX ||
		status.Lease.Owner != OwnerTransitioning {
		t.Fatalf("USB was published before exact revoke result: status=%#v pending=%#v", status, pending)
	}
}
