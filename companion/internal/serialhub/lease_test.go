package serialhub

import (
	"errors"
	"testing"
	"time"
)

func TestLeaseIsExclusiveAndReportsUSBOnlyAfterDeckAcknowledgesRevoke(t *testing.T) {
	now := time.Unix(100, 0)
	leases := NewLeaseManager(10*time.Minute, func() time.Time { return now })
	acquire, err := leases.Acquire("browser-a", 9)
	if err != nil || !acquire.Enable || acquire.SessionID != 9 || acquire.RequestID == 0 {
		t.Fatalf("Acquire() = %#v, %v", acquire, err)
	}
	if _, err = leases.Acquire("browser-b", 9); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("contending Acquire() error = %v", err)
	}
	if got := leases.Status(); got.Owner != OwnerTransitioning || got.ClientID != "browser-a" {
		t.Fatalf("pending status = %#v", got)
	}
	if err = leases.ApplyOwnerResult(OwnerResult{SessionID: 9, RequestID: acquire.RequestID, Owner: OwnerWeb, LeaseID: acquire.RequestID}); err != nil {
		t.Fatal(err)
	}
	if got := leases.Status(); got.Owner != OwnerWeb || got.LeaseID != acquire.RequestID {
		t.Fatalf("active status = %#v", got)
	}
	revoke, err := leases.Disconnect("browser-a")
	if err != nil || revoke.Enable {
		t.Fatalf("Disconnect() = %#v, %v", revoke, err)
	}
	if got := leases.Status(); got.Owner != OwnerTransitioning {
		t.Fatalf("status reported USB before Deck revoke: %#v", got)
	}
	repeated, err := leases.Disconnect("browser-a")
	if err != nil || repeated != revoke {
		t.Fatalf("idempotent Disconnect() = %#v, %v; want %#v", repeated, err, revoke)
	}
	if err = leases.ApplyOwnerResult(OwnerResult{SessionID: 9, RequestID: revoke.RequestID, Owner: OwnerUSB}); err != nil {
		t.Fatal(err)
	}
	if got := leases.Status(); got.Owner != OwnerUSB || got.ClientID != "" || got.LeaseID != 0 {
		t.Fatalf("revoked status = %#v", got)
	}
}

func TestLeaseHeartbeatAndTimeout(t *testing.T) {
	now := time.Unix(100, 0)
	leases := NewLeaseManager(10*time.Minute, func() time.Time { return now })
	request, err := leases.Acquire("browser-a", 3)
	if err != nil {
		t.Fatal(err)
	}
	if err = leases.ApplyOwnerResult(OwnerResult{SessionID: 3, RequestID: request.RequestID, Owner: OwnerWeb, LeaseID: request.RequestID}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(9 * time.Minute)
	activity, err := leases.Heartbeat("browser-a", request.RequestID)
	if err != nil || activity.SessionID != 3 || activity.LeaseID != request.RequestID {
		t.Fatalf("Heartbeat() = %#v, %v", activity, err)
	}
	now = now.Add(9 * time.Minute)
	if _, expired := leases.Expire(); expired {
		t.Fatal("renewed Lease expired early")
	}
	now = now.Add(2 * time.Minute)
	revoke, expired := leases.Expire()
	if !expired || revoke.Enable {
		t.Fatalf("Expire() = %#v, %t", revoke, expired)
	}
	if _, err = leases.Heartbeat("browser-a", request.RequestID); !errors.Is(err, ErrLeaseNotActive) {
		t.Fatalf("Heartbeat after expiry error = %v", err)
	}
}

func TestLeaseRejectsStaleResultsAndSessionEndClearsState(t *testing.T) {
	leasing := NewLeaseManager(time.Minute, time.Now)
	request, err := leasing.Acquire("browser", 6)
	if err != nil {
		t.Fatal(err)
	}
	if err = leasing.ApplyOwnerResult(OwnerResult{SessionID: 5, RequestID: request.RequestID, Owner: OwnerWeb}); !errors.Is(err, ErrStaleOwnerResult) {
		t.Fatalf("stale result error = %v", err)
	}
	leasing.EndSession(6)
	if got := leasing.Status(); got.Owner != OwnerUnavailable || got.SessionID != 0 {
		t.Fatalf("status after end = %#v", got)
	}
}

func TestPendingOwnerRequestRetryIsClaimedByOnlyOneObserverPerInterval(t *testing.T) {
	now := time.Unix(100, 0)
	leasing := NewLeaseManager(time.Minute, func() time.Time { return now })
	request, err := leasing.Acquire("browser", 6)
	if err != nil {
		t.Fatal(err)
	}
	if !leasing.ClaimRequestAttempt(request.RequestID, time.Second) {
		t.Fatal("initial request attempt was not claimed")
	}
	if leasing.ClaimRequestAttempt(request.RequestID, time.Second) {
		t.Fatal("duplicate observer claimed the same retry interval")
	}
	now = now.Add(time.Second)
	if !leasing.ClaimRequestAttempt(request.RequestID, time.Second) {
		t.Fatal("pending request did not become retryable")
	}
	if _, _, pending := leasing.PendingRequest(); !pending {
		t.Fatal("retry claim consumed the pending request")
	}
}

func TestRejectedOwnerResultNeverReportsUSBWhileDeckStillReportsWeb(t *testing.T) {
	leasing := NewLeaseManager(time.Minute, time.Now)
	request, err := leasing.Acquire("browser", 6)
	if err != nil {
		t.Fatal(err)
	}
	if err = leasing.ApplyOwnerRejection(OwnerResult{
		SessionID: 6, RequestID: request.RequestID, Owner: OwnerWeb, LeaseID: 88,
	}); err != nil {
		t.Fatal(err)
	}
	if got := leasing.Status(); got.Owner != OwnerUnavailable || got.ClientID != "" || got.LeaseID != 0 {
		t.Fatalf("rejected Web owner status = %#v", got)
	}
}
