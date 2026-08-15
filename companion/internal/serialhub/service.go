package serialhub

import (
	"errors"
	"sync"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/serialprotocol"
)

var ErrSessionBusy = errors.New("another Deck owns the current Serial Session ring")

type State string

const (
	StateDisarmed State = "disarmed"
	StateUSBTX    State = "usb_tx"
	StateWebTX    State = "web_tx"
)

type ServiceConfig struct {
	Ring       *Ring
	RingConfig Config
	LeaseTTL   time.Duration
	Now        func() time.Time
}

type ServiceStatus struct {
	DeviceID         string
	State            State
	SessionID        uint64
	BufferedBytes    int
	BufferedFrames   int
	OverwrittenBytes uint64
	Observers        int
	Lease            LeaseStatus
}

type Service struct {
	mu sync.Mutex

	ring        *Ring
	leases      *LeaseManager
	deviceID    string
	state       State
	closed      bool
	webSequence uint64
	started     time.Time
}

func NewService(config ServiceConfig) (*Service, error) {
	ring := config.Ring
	if ring == nil {
		var err error
		ring, err = NewRing(config.RingConfig)
		if err != nil {
			return nil, err
		}
	}
	return &Service{
		ring:    ring,
		leases:  NewLeaseManager(config.LeaseTTL, config.Now),
		state:   StateDisarmed,
		started: time.Now(),
	}, nil
}

func (service *Service) Reconcile(deviceID string, sessionID uint64, state State) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed {
		return ErrClosed
	}
	if deviceID == "" {
		return ErrInvalidSession
	}
	if state == StateDisarmed {
		if sessionID != 0 {
			return ErrInvalidSession
		}
		if service.deviceID != deviceID {
			return nil
		}
		active := service.ring.Stats().SessionID
		if active != 0 {
			if err := service.ring.End(active); err != nil {
				return err
			}
			service.leases.EndSession(active)
		}
		service.deviceID = ""
		service.state = StateDisarmed
		service.webSequence = 0
		return nil
	}
	if state != StateUSBTX && state != StateWebTX || sessionID == 0 {
		return ErrInvalidSession
	}
	if service.deviceID != "" && service.deviceID != deviceID {
		return ErrSessionBusy
	}
	previousSessionID := service.ring.Stats().SessionID
	previousState := service.state
	if err := service.ring.Begin(sessionID); err != nil {
		return err
	}
	if previousSessionID != 0 && previousSessionID != sessionID {
		service.leases.EndSession(previousSessionID)
	}
	service.deviceID = deviceID
	service.state = state
	if previousSessionID != sessionID {
		service.webSequence = 0
	}
	if state == StateUSBTX {
		_, _, pending := service.leases.PendingRequest()
		if previousSessionID == sessionID && pending {
			service.state = previousState
		} else {
			// A current Deck state publication is authoritative confirmation that
			// no Web Lease remains after owner-side expiry or reconnect. It must
			// not consume a same-Session request still waiting for its exact result.
			service.leases.ConfirmUSB(sessionID)
		}
	}
	return nil
}

func (service *Service) Ingest(deviceID string, frame serialprotocol.Frame) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed {
		return ErrClosed
	}
	if deviceID == "" || deviceID != service.deviceID || service.state == StateDisarmed {
		return ErrWrongSession
	}
	return service.ring.Ingest(frame)
}

func (service *Service) BuildWebFrame(
	clientID string,
	leaseID uint64,
	payload []byte,
) (string, serialprotocol.Frame, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed {
		return "", serialprotocol.Frame{}, ErrClosed
	}
	lease := service.leases.Status()
	if service.deviceID == "" || service.state != StateWebTX ||
		lease.Owner != OwnerWeb || lease.ClientID != clientID ||
		lease.LeaseID == 0 || lease.LeaseID != leaseID ||
		len(payload) == 0 || len(payload) > serialprotocol.MaxPayloadBytes {
		return "", serialprotocol.Frame{}, ErrLeaseNotActive
	}
	service.webSequence++
	if service.webSequence == 0 {
		service.webSequence = 1
	}
	return service.deviceID, serialprotocol.Frame{
		Channel: serialprotocol.ChannelWebTX, SessionID: lease.SessionID,
		Sequence:    service.webSequence,
		MonotonicMS: uint64(time.Since(service.started) / time.Millisecond),
		Payload:     append([]byte(nil), payload...),
	}, nil
}

func (service *Service) Ring() *Ring {
	return service.ring
}

func (service *Service) AcquireLease(clientID string, sessionID uint64) (OwnerRequest, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed || service.deviceID == "" || service.state == StateDisarmed ||
		service.ring.Stats().SessionID != sessionID {
		return OwnerRequest{}, ErrLeaseNotActive
	}
	return service.leases.Acquire(clientID, sessionID)
}

func (service *Service) DisconnectLease(clientID string) (OwnerRequest, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed {
		return OwnerRequest{}, ErrClosed
	}
	return service.leases.Disconnect(clientID)
}

func (service *Service) HeartbeatLease(clientID string, leaseID uint64) (OwnerActivity, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed || service.state != StateWebTX {
		return OwnerActivity{}, ErrLeaseNotActive
	}
	return service.leases.Heartbeat(clientID, leaseID)
}

func (service *Service) ExpireLease() (OwnerRequest, bool) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed {
		return OwnerRequest{}, false
	}
	return service.leases.Expire()
}

func (service *Service) ApplyOwnerResult(
	deviceID string,
	result OwnerResult,
	accepted bool,
) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed || deviceID == "" || deviceID != service.deviceID ||
		result.SessionID == 0 || result.SessionID != service.ring.Stats().SessionID {
		return ErrStaleOwnerResult
	}
	var err error
	if accepted {
		err = service.leases.ApplyOwnerResult(result)
	} else {
		err = service.leases.ApplyOwnerRejection(result)
	}
	if err != nil {
		return err
	}
	switch result.Owner {
	case OwnerUSB:
		service.state = StateUSBTX
	case OwnerWeb:
		service.state = StateWebTX
	default:
		// A rejected result can prove neither owner. Preserve the current Deck
		// state while the Lease itself remains unavailable.
	}
	return nil
}

func (service *Service) PendingOwnerRequest() (OwnerRequest, string, bool) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed {
		return OwnerRequest{}, "", false
	}
	return service.leases.PendingRequest()
}

func (service *Service) ClaimOwnerRequestAttempt(requestID uint64, minimumInterval time.Duration) bool {
	service.mu.Lock()
	defer service.mu.Unlock()
	return !service.closed && service.leases.ClaimRequestAttempt(requestID, minimumInterval)
}

func (service *Service) RequireOwnerRevocation(deviceID string) (OwnerRequest, bool) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed || deviceID == "" || deviceID != service.deviceID {
		return OwnerRequest{}, false
	}
	sessionID := service.ring.Stats().SessionID
	lease := service.leases.Status()
	if sessionID == 0 || (service.state != StateWebTX &&
		lease.Owner != OwnerWeb && lease.Owner != OwnerTransitioning) {
		return OwnerRequest{}, false
	}
	request, err := service.leases.RequireRevocation(sessionID)
	return request, err == nil
}

func (service *Service) MarkOwnerUnavailable() {
	service.mu.Lock()
	if !service.closed {
		service.leases.FailClosed(service.ring.Stats().SessionID)
	}
	service.mu.Unlock()
}

func (service *Service) Status() ServiceStatus {
	service.mu.Lock()
	deviceID := service.deviceID
	state := service.state
	ring := service.ring.Stats()
	lease := service.leases.Status()
	service.mu.Unlock()
	return ServiceStatus{
		DeviceID:         deviceID,
		State:            state,
		SessionID:        ring.SessionID,
		BufferedBytes:    ring.BufferedBytes,
		BufferedFrames:   ring.BufferedFrames,
		OverwrittenBytes: ring.OverwrittenBytes,
		Observers:        ring.Observers,
		Lease:            lease,
	}
}

func (service *Service) Close() {
	service.mu.Lock()
	if !service.closed {
		service.closed = true
		activeSessionID := service.ring.Stats().SessionID
		if activeSessionID != 0 {
			service.leases.EndSession(activeSessionID)
		}
		service.deviceID = ""
		service.state = StateDisarmed
		service.webSequence = 0
		service.ring.Close()
	}
	service.mu.Unlock()
}
