package serialhub

import (
	"errors"
	"sync"
	"time"
)

var (
	ErrLeaseHeld        = errors.New("Web TX Lease is already held")
	ErrLeaseNotActive   = errors.New("Web TX Lease is not active")
	ErrStaleOwnerResult = errors.New("stale Serial owner result")
)

type Owner string

const (
	OwnerUnavailable   Owner = "unavailable"
	OwnerTransitioning Owner = "transitioning"
	OwnerUSB           Owner = "usb"
	OwnerWeb           Owner = "web"
)

type OwnerRequest struct {
	SessionID uint64
	RequestID uint64
	Enable    bool
}

type OwnerActivity struct {
	SessionID uint64
	LeaseID   uint64
}

type OwnerResult struct {
	SessionID uint64
	RequestID uint64
	Owner     Owner
	LeaseID   uint64
}

type LeaseStatus struct {
	SessionID uint64
	ClientID  string
	Owner     Owner
	LeaseID   uint64
	ExpiresAt time.Time
}

type LeaseManager struct {
	mu sync.Mutex

	ttl           time.Duration
	now           func() time.Time
	nextID        uint64
	status        LeaseStatus
	pendingID     uint64
	pendingEnable bool
	lastAttempt   time.Time
}

func NewLeaseManager(ttl time.Duration, now func() time.Time) *LeaseManager {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if now == nil {
		now = time.Now
	}
	return &LeaseManager{ttl: ttl, now: now, nextID: 1, status: LeaseStatus{Owner: OwnerUnavailable}}
}

func (manager *LeaseManager) Acquire(clientID string, sessionID uint64) (OwnerRequest, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if clientID == "" || sessionID == 0 {
		return OwnerRequest{}, ErrLeaseNotActive
	}
	if manager.status.ClientID != "" || manager.pendingID != 0 || manager.status.Owner == OwnerWeb {
		return OwnerRequest{}, ErrLeaseHeld
	}
	request := manager.nextRequestLocked(sessionID, true)
	manager.status = LeaseStatus{SessionID: sessionID, ClientID: clientID, Owner: OwnerTransitioning}
	return request, nil
}

func (manager *LeaseManager) Disconnect(clientID string) (OwnerRequest, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if clientID == "" || manager.status.ClientID != clientID || manager.status.SessionID == 0 {
		return OwnerRequest{}, ErrLeaseNotActive
	}
	if manager.pendingID != 0 && !manager.pendingEnable {
		return OwnerRequest{
			SessionID: manager.status.SessionID,
			RequestID: manager.pendingID,
			Enable:    false,
		}, nil
	}
	request := manager.nextRequestLocked(manager.status.SessionID, false)
	manager.status.Owner = OwnerTransitioning
	manager.status.ExpiresAt = time.Time{}
	return request, nil
}

func (manager *LeaseManager) Heartbeat(clientID string, leaseID uint64) (OwnerActivity, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	now := manager.now()
	if manager.status.Owner != OwnerWeb || manager.status.ClientID != clientID ||
		manager.status.LeaseID == 0 || manager.status.LeaseID != leaseID ||
		!now.Before(manager.status.ExpiresAt) {
		return OwnerActivity{}, ErrLeaseNotActive
	}
	manager.status.ExpiresAt = now.Add(manager.ttl)
	return OwnerActivity{SessionID: manager.status.SessionID, LeaseID: manager.status.LeaseID}, nil
}

func (manager *LeaseManager) Expire() (OwnerRequest, bool) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.status.Owner != OwnerWeb || manager.status.ExpiresAt.IsZero() || manager.now().Before(manager.status.ExpiresAt) {
		return OwnerRequest{}, false
	}
	request := manager.nextRequestLocked(manager.status.SessionID, false)
	manager.status.Owner = OwnerTransitioning
	manager.status.ExpiresAt = time.Time{}
	return request, true
}

func (manager *LeaseManager) ApplyOwnerResult(result OwnerResult) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.pendingID == 0 || result.SessionID != manager.status.SessionID || result.RequestID != manager.pendingID {
		return ErrStaleOwnerResult
	}
	if manager.pendingEnable {
		if result.Owner != OwnerWeb || result.LeaseID == 0 {
			return ErrStaleOwnerResult
		}
		manager.status.Owner = OwnerWeb
		manager.status.LeaseID = result.LeaseID
		manager.status.ExpiresAt = manager.now().Add(manager.ttl)
	} else {
		if result.Owner != OwnerUSB || result.LeaseID != 0 {
			return ErrStaleOwnerResult
		}
		manager.status = LeaseStatus{SessionID: result.SessionID, Owner: OwnerUSB}
	}
	manager.pendingID = 0
	manager.pendingEnable = false
	manager.lastAttempt = time.Time{}
	return nil
}

// ApplyOwnerRejection consumes an exact negative Deck result without claiming
// that the UI owns Web TX. USB is reported only when the Deck result itself
// confirms USB with no Lease; every other state fails closed as unavailable.
func (manager *LeaseManager) ApplyOwnerRejection(result OwnerResult) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.pendingID == 0 || result.SessionID != manager.status.SessionID ||
		result.RequestID != manager.pendingID {
		return ErrStaleOwnerResult
	}
	if result.Owner == OwnerUSB && result.LeaseID == 0 {
		manager.status = LeaseStatus{SessionID: result.SessionID, Owner: OwnerUSB}
	} else {
		manager.status = LeaseStatus{SessionID: result.SessionID, Owner: OwnerUnavailable}
	}
	manager.pendingID = 0
	manager.pendingEnable = false
	manager.lastAttempt = time.Time{}
	return nil
}

func (manager *LeaseManager) AbortAcquire(clientID string, requestID uint64) {
	manager.mu.Lock()
	if manager.pendingEnable && manager.pendingID == requestID &&
		manager.status.ClientID == clientID {
		manager.status = LeaseStatus{SessionID: manager.status.SessionID, Owner: OwnerUSB}
		manager.pendingID = 0
		manager.pendingEnable = false
		manager.lastAttempt = time.Time{}
	}
	manager.mu.Unlock()
}

func (manager *LeaseManager) PendingRequest() (OwnerRequest, string, bool) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.pendingID == 0 {
		return OwnerRequest{}, "", false
	}
	return OwnerRequest{
		SessionID: manager.status.SessionID,
		RequestID: manager.pendingID,
		Enable:    manager.pendingEnable,
	}, manager.status.ClientID, true
}

// ClaimRequestAttempt gives one caller permission to send or resend an exact
// pending request. It prevents multiple observers from amplifying retries.
func (manager *LeaseManager) ClaimRequestAttempt(requestID uint64, minimumInterval time.Duration) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if requestID == 0 || requestID != manager.pendingID {
		return false
	}
	now := manager.now()
	if minimumInterval > 0 && !manager.lastAttempt.IsZero() &&
		now.Sub(manager.lastAttempt) < minimumInterval {
		return false
	}
	manager.lastAttempt = now
	return true
}

func (manager *LeaseManager) EndSession(sessionID uint64) {
	manager.mu.Lock()
	if manager.status.SessionID == sessionID {
		manager.status = LeaseStatus{Owner: OwnerUnavailable}
		manager.pendingID = 0
		manager.pendingEnable = false
		manager.lastAttempt = time.Time{}
	}
	manager.mu.Unlock()
}

func (manager *LeaseManager) Status() LeaseStatus {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.status
}

func (manager *LeaseManager) nextRequestLocked(sessionID uint64, enable bool) OwnerRequest {
	requestID := manager.nextID
	manager.nextID++
	if manager.nextID == 0 {
		manager.nextID = 1
	}
	manager.pendingID = requestID
	manager.pendingEnable = enable
	manager.lastAttempt = time.Time{}
	return OwnerRequest{SessionID: sessionID, RequestID: requestID, Enable: enable}
}
