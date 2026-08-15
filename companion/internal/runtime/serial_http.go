package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/devicelink"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/serialhub"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/serialprotocol"
	"github.com/coder/websocket"
)

const (
	serialObserverSubprotocol = "s3deck.serial.v1"
	serialObserverReadBytes   = 64 << 10
	serialDownloadMaxBytes    = 1 << 20
	serialObserverWriteLimit  = 2 * time.Second
)

type serialObserverControl struct {
	Type            string `json:"type"`
	ProtocolVersion int    `json:"protocol_version"`
	SerialSessionID uint64 `json:"serial_session_id"`
	LeaseID         uint64 `json:"lease_id"`
}

type serialObserverInput struct {
	messageType websocket.MessageType
	message     []byte
	err         error
}

type serialObserverRegistry struct {
	mu          sync.Mutex
	connections map[*websocket.Conn]struct{}
	closing     bool
	drained     chan struct{}
}

func (registry *serialObserverRegistry) register(connection *websocket.Conn) bool {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.closing {
		return false
	}
	if registry.connections == nil {
		registry.connections = make(map[*websocket.Conn]struct{})
	}
	registry.connections[connection] = struct{}{}
	return true
}

func (registry *serialObserverRegistry) unregister(connection *websocket.Conn) {
	registry.mu.Lock()
	if _, found := registry.connections[connection]; found {
		delete(registry.connections, connection)
		if registry.closing && len(registry.connections) == 0 &&
			registry.drained != nil {
			close(registry.drained)
			registry.drained = nil
		}
	}
	registry.mu.Unlock()
}

func (registry *serialObserverRegistry) closeAndWait(ctx context.Context) error {
	registry.mu.Lock()
	registry.closing = true
	if len(registry.connections) == 0 {
		registry.mu.Unlock()
		return nil
	}
	if registry.drained == nil {
		registry.drained = make(chan struct{})
	}
	drained := registry.drained
	connections := make([]*websocket.Conn, 0, len(registry.connections))
	for connection := range registry.connections {
		connections = append(connections, connection)
	}
	registry.mu.Unlock()
	for _, connection := range connections {
		_ = connection.CloseNow()
	}
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return errors.New("Serial observer shutdown timed out")
	}
}

type serialManagementStatus struct {
	DeviceID         string          `json:"device_id"`
	SerialState      serialhub.State `json:"serial_state"`
	SerialSessionID  uint64          `json:"serial_session_id"`
	BufferedBytes    int             `json:"buffered_bytes"`
	BufferedFrames   int             `json:"buffered_frames"`
	OverwrittenBytes uint64          `json:"overwritten_bytes"`
	Observers        int             `json:"observers"`
	LeaseOwner       serialhub.Owner `json:"lease_owner"`
}

func publicSerialStatus(status serialhub.ServiceStatus) serialManagementStatus {
	return serialManagementStatus{
		DeviceID: status.DeviceID, SerialState: status.State,
		SerialSessionID: status.SessionID, BufferedBytes: status.BufferedBytes,
		BufferedFrames: status.BufferedFrames, OverwrittenBytes: status.OverwrittenBytes,
		Observers: status.Observers, LeaseOwner: status.Lease.Owner,
	}
}

func (application *Runtime) handleSerialStatus(response http.ResponseWriter, _ *http.Request) {
	writeManagementJSON(response, publicSerialStatus(application.serialHub.Status()))
}

func (application *Runtime) handleSerialDownload(response http.ResponseWriter, request *http.Request) {
	sessionID, err := strconv.ParseUint(request.URL.Query().Get("session_id"), 10, 64)
	if err != nil || sessionID == 0 {
		http.Error(response, "invalid Serial Session", http.StatusBadRequest)
		return
	}
	fromSequence := uint64(0)
	if value := request.URL.Query().Get("from_sequence"); value != "" {
		fromSequence, err = strconv.ParseUint(value, 10, 64)
		if err != nil {
			http.Error(response, "invalid Serial range", http.StatusBadRequest)
			return
		}
	}
	maximumBytes := serialDownloadMaxBytes
	if value := request.URL.Query().Get("maximum_bytes"); value != "" {
		parsed, parseErr := strconv.Atoi(value)
		if parseErr != nil || parsed <= 0 || parsed > serialDownloadMaxBytes {
			http.Error(response, "invalid Serial download limit", http.StatusBadRequest)
			return
		}
		maximumBytes = parsed
	}
	document, err := application.serialHub.Ring().Download(sessionID, fromSequence, maximumBytes)
	if err != nil {
		switch {
		case errors.Is(err, serialhub.ErrWrongSession), errors.Is(err, serialhub.ErrRangeUnavailable):
			http.Error(response, "Serial range unavailable", http.StatusNotFound)
		case errors.Is(err, serialhub.ErrRangeTooLarge):
			http.Error(response, "Serial range exceeds download limit", http.StatusRequestEntityTooLarge)
		default:
			http.Error(response, "Serial Hub unavailable", http.StatusServiceUnavailable)
		}
		return
	}
	defer clear(document)
	response.Header().Set("Content-Type", "application/octet-stream")
	response.Header().Set("Content-Disposition", "attachment; filename=serial-session.bin")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(document)
}

func (application *Runtime) handleSerialObserve(response http.ResponseWriter, request *http.Request) {
	if _, valid := application.managementSession(request); !valid {
		http.Error(response, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !application.managementOriginValid(request) {
		http.Error(response, "forbidden", http.StatusForbidden)
		return
	}
	status := application.serialHub.Status()
	if status.SessionID == 0 {
		http.Error(response, "Serial Session unavailable", http.StatusConflict)
		return
	}
	observerID, err := application.serialHub.Ring().OpenObserver(status.SessionID)
	if err != nil {
		http.Error(response, "Serial observer unavailable", http.StatusServiceUnavailable)
		return
	}
	defer application.serialHub.Ring().CloseObserver(observerID)
	connection, err := websocket.Accept(response, request, &websocket.AcceptOptions{
		Subprotocols: []string{serialObserverSubprotocol},
	})
	if err != nil {
		return
	}
	defer connection.CloseNow()
	if connection.Subprotocol() != serialObserverSubprotocol {
		_ = connection.Close(websocket.StatusPolicyViolation, "unsupported Serial observer")
		return
	}
	if !application.serialObservers.register(connection) {
		_ = connection.Close(websocket.StatusGoingAway, "Companion is stopping")
		return
	}
	defer application.serialObservers.unregister(connection)
	connection.SetReadLimit(4 << 10)
	clientID, err := randomWebToken()
	if err != nil {
		return
	}
	readResult := make(chan serialObserverInput, 1)
	readerDone := make(chan struct{})
	defer close(readerDone)
	go readSerialObserver(connection, readResult, readerDone)
	defer application.revokeSerialObserverLease(clientID)
	if !writeSerialObserverStatus(connection, application.serialHub.Status(), clientID, 0) {
		return
	}
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	statusPoll := time.NewTicker(time.Second)
	defer statusPoll.Stop()
	var overwrittenBytes uint64
	for {
		select {
		case <-request.Context().Done():
			return
		case input := <-readResult:
			if input.err != nil || !application.handleSerialObserverInput(connection, clientID, input) {
				clear(input.message)
				return
			}
			clear(input.message)
		case <-statusPoll.C:
			application.retrySerialOwnerRequest()
			if !writeSerialObserverStatus(
				connection,
				application.serialHub.Status(),
				clientID,
				overwrittenBytes,
			) {
				return
			}
		case <-poll.C:
			if request, expired := application.serialHub.ExpireLease(); expired {
				application.sendSerialOwnerRequest(request)
			}
			batch, readErr := application.serialHub.Ring().ReadObserver(
				observerID,
				serialObserverReadBytes,
				256,
			)
			if readErr != nil {
				return
			}
			overwrittenBytes = batch.OverwrittenBytes
			if !writeSerialObserverBatch(connection, batch.Frames) {
				return
			}
		}
	}
}

func readSerialObserver(
	connection *websocket.Conn,
	result chan<- serialObserverInput,
	done <-chan struct{},
) {
	for {
		messageType, message, err := connection.Read(context.Background())
		if err != nil {
			select {
			case result <- serialObserverInput{err: err}:
			case <-done:
			}
			return
		}
		input := serialObserverInput{
			messageType: messageType,
			message:     append([]byte(nil), message...),
		}
		clear(message)
		select {
		case result <- input:
		case <-done:
			clear(input.message)
			return
		}
	}
}

func writeSerialObserverBatch(connection *websocket.Conn, frames []serialprotocol.Frame) bool {
	defer func() {
		for index := range frames {
			clear(frames[index].Payload)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), serialObserverWriteLimit)
	defer cancel()
	for _, frame := range frames {
		document, err := serialprotocol.Encode(frame)
		if err != nil {
			return false
		}
		err = connection.Write(ctx, websocket.MessageBinary, document)
		clear(document)
		if err != nil {
			return false
		}
	}
	return true
}

func (application *Runtime) handleSerialObserverInput(
	connection *websocket.Conn,
	clientID string,
	input serialObserverInput,
) bool {
	if input.messageType == websocket.MessageBinary {
		status := application.serialHub.Status()
		deviceID, frame, err := application.serialHub.BuildWebFrame(
			clientID,
			status.Lease.LeaseID,
			input.message,
		)
		if err != nil {
			return writeSerialObserverResult(connection, "serial.tx.result", false, 0)
		}
		defer clear(frame.Payload)
		ctx, cancel := context.WithTimeout(context.Background(), serialObserverWriteLimit)
		err = application.deviceLink.SendSerialWebFrame(ctx, deviceID, frame)
		cancel()
		return writeSerialObserverResult(connection, "serial.tx.result", err == nil, frame.Sequence)
	}
	if input.messageType != websocket.MessageText {
		return false
	}
	var control serialObserverControl
	if protocol.DecodeStrictDocumentLimit(input.message, 4<<10, &control) != nil ||
		control.ProtocolVersion != 1 {
		return false
	}
	switch control.Type {
	case "serial.lease.acquire":
		status := application.serialHub.Status()
		if control.SerialSessionID == 0 || control.SerialSessionID != status.SessionID ||
			control.LeaseID != 0 {
			return writeSerialObserverResult(connection, "serial.lease.result", false, 0)
		}
		request, err := application.serialHub.AcquireLease(clientID, control.SerialSessionID)
		if err != nil {
			return writeSerialObserverResult(connection, "serial.lease.result", false, 0)
		}
		// A transport error cannot prove that the Deck did not receive or apply
		// the request. Keep the exact request pending and let the supervisor
		// retry until a matching Deck result arrives.
		application.sendSerialOwnerRequest(request)
		return writeSerialObserverResult(connection, "serial.lease.result", true, request.RequestID)
	case "serial.lease.heartbeat":
		activity, err := application.serialHub.HeartbeatLease(clientID, control.LeaseID)
		if err != nil || control.SerialSessionID != activity.SessionID {
			return writeSerialObserverResult(connection, "serial.lease.heartbeat.result", false, 0)
		}
		status := application.serialHub.Status()
		ctx, cancel := context.WithTimeout(context.Background(), serialObserverWriteLimit)
		err = application.deviceLink.SendSerialOwnerActivity(ctx, status.DeviceID, devicelink.SerialOwnerActivity{
			SerialSessionID: activity.SessionID,
			LeaseID:         activity.LeaseID,
		})
		cancel()
		return writeSerialObserverResult(connection, "serial.lease.heartbeat.result", err == nil, activity.LeaseID)
	case "serial.lease.release":
		status := application.serialHub.Status()
		if control.SerialSessionID != status.SessionID || control.LeaseID == 0 ||
			control.LeaseID != status.Lease.LeaseID || status.Lease.ClientID != clientID {
			return writeSerialObserverResult(connection, "serial.lease.result", false, 0)
		}
		request, err := application.serialHub.DisconnectLease(clientID)
		if err != nil {
			return writeSerialObserverResult(connection, "serial.lease.result", false, 0)
		}
		return writeSerialObserverResult(
			connection,
			"serial.lease.result",
			application.sendSerialOwnerRequest(request),
			request.RequestID,
		)
	case "serial.observer.heartbeat":
		return writeSerialObserverResult(connection, "serial.observer.heartbeat.result", true, 0)
	default:
		return false
	}
}

func (application *Runtime) sendSerialOwnerRequest(request serialhub.OwnerRequest) bool {
	ctx, cancel := context.WithTimeout(context.Background(), serialObserverWriteLimit)
	defer cancel()
	return application.sendSerialOwnerRequestContext(ctx, request, time.Second)
}

func (application *Runtime) sendSerialOwnerRequestContext(
	ctx context.Context,
	request serialhub.OwnerRequest,
	minimumInterval time.Duration,
) bool {
	status := application.serialHub.Status()
	if status.DeviceID == "" || status.SessionID != request.SessionID {
		return false
	}
	if !application.serialHub.ClaimOwnerRequestAttempt(request.RequestID, minimumInterval) {
		return true
	}
	err := application.deviceLink.RequestSerialOwner(ctx, status.DeviceID, devicelink.SerialOwnerRequest{
		SerialSessionID: request.SessionID,
		RequestID:       request.RequestID,
		Enable:          request.Enable,
	})
	return err == nil
}

func (application *Runtime) retrySerialOwnerRequest() {
	request, _, pending := application.serialHub.PendingOwnerRequest()
	if pending {
		application.sendSerialOwnerRequest(request)
	}
}

func (application *Runtime) runSerialLeaseSupervisor(ctx context.Context) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if request, expired := application.serialHub.ExpireLease(); expired {
				application.sendSerialOwnerRequest(request)
			}
			application.retrySerialOwnerRequest()
		}
	}
}

func (application *Runtime) revokeSerialObserverLease(clientID string) {
	request, err := application.serialHub.DisconnectLease(clientID)
	if err == nil {
		application.sendSerialOwnerRequest(request)
	}
}

func (application *Runtime) closeSerialObservers(ctx context.Context) error {
	return application.serialObservers.closeAndWait(ctx)
}

func (application *Runtime) revokeSerialOwnerForShutdown(ctx context.Context) error {
	status := application.serialHub.Status()
	request, required := application.serialHub.RequireOwnerRevocation(status.DeviceID)
	if !required {
		return nil
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		application.sendSerialOwnerRequestContext(ctx, request, 100*time.Millisecond)
		status = application.serialHub.Status()
		if status.SessionID != request.SessionID ||
			(status.State == serialhub.StateUSBTX && status.Lease.Owner == serialhub.OwnerUSB) {
			return nil
		}
		select {
		case <-ctx.Done():
			application.serialHub.MarkOwnerUnavailable()
			return errors.New("Serial owner revoke was not confirmed before shutdown")
		case <-ticker.C:
		}
	}
}

func writeSerialObserverResult(
	connection *websocket.Conn,
	messageType string,
	accepted bool,
	referenceID uint64,
) bool {
	return writeSerialObserverJSON(connection, struct {
		Type            string `json:"type"`
		ProtocolVersion int    `json:"protocol_version"`
		Accepted        bool   `json:"accepted"`
		ReferenceID     uint64 `json:"reference_id"`
	}{messageType, 1, accepted, referenceID})
}

func writeSerialObserverStatus(
	connection *websocket.Conn,
	status serialhub.ServiceStatus,
	clientID string,
	overwrittenBytes uint64,
) bool {
	document := struct {
		Type             string          `json:"type"`
		ProtocolVersion  int             `json:"protocol_version"`
		DeviceID         string          `json:"device_id"`
		SerialState      serialhub.State `json:"serial_state"`
		SerialSessionID  uint64          `json:"serial_session_id"`
		BufferedBytes    int             `json:"buffered_bytes"`
		OverwrittenBytes uint64          `json:"overwritten_bytes"`
		LeaseOwner       serialhub.Owner `json:"lease_owner"`
		LeaseHeld        bool            `json:"lease_held_by_this_observer"`
		LeaseID          uint64          `json:"lease_id,omitempty"`
	}{
		Type: "serial.observer.state", ProtocolVersion: 1,
		DeviceID: status.DeviceID, SerialState: status.State,
		SerialSessionID: status.SessionID, BufferedBytes: status.BufferedBytes,
		OverwrittenBytes: overwrittenBytes, LeaseOwner: status.Lease.Owner,
		LeaseHeld: status.Lease.ClientID == clientID && status.Lease.Owner == serialhub.OwnerWeb,
	}
	if document.LeaseHeld {
		document.LeaseID = status.Lease.LeaseID
	}
	return writeSerialObserverJSON(connection, document)
}

func writeSerialObserverJSON(connection *websocket.Conn, document any) bool {
	ctx, cancel := context.WithTimeout(context.Background(), serialObserverWriteLimit)
	defer cancel()
	encoded, err := json.Marshal(document)
	if err != nil {
		return false
	}
	defer clear(encoded)
	return connection.Write(ctx, websocket.MessageText, encoded) == nil
}
