package devicelink

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/configmodel"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairing"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/serialprotocol"
	"github.com/coder/websocket"
)

const (
	defaultHeartbeatInterval = 10 * time.Second
	defaultHeartbeatTimeout  = 30 * time.Second
	MaxSnapshotMessageBytes  = 16 << 10
)

var ErrInvalidSnapshot = errors.New("invalid AI Snapshot publication")
var ErrDeviceUnavailable = errors.New("Deck Device Link is unavailable")

type Authenticator interface {
	Verify(context.Context, pairing.Authentication) (bool, error)
}

type Config struct {
	Authenticator       Authenticator
	HeartbeatInterval   time.Duration
	HeartbeatTimeout    time.Duration
	Now                 func() time.Time
	Elapsed             func() time.Duration
	OnDeviceProfile     func(configmodel.DeviceProfile)
	OnSerialState       func(deviceID string, sessionID uint64, state string) error
	OnSerialFrame       func(deviceID string, frame serialprotocol.Frame) error
	OnDisconnect        func(deviceID string)
	OnSerialOwnerResult func(deviceID string, result SerialOwnerResult) error
	SerialHistoryCursor func(deviceID string, sessionID uint64) (uint64, bool)
}

type Hub struct {
	authenticator       Authenticator
	heartbeatInterval   time.Duration
	heartbeatTimeout    time.Duration
	now                 func() time.Time
	elapsed             func() time.Duration
	onDeviceProfile     func(configmodel.DeviceProfile)
	onSerialState       func(deviceID string, sessionID uint64, state string) error
	onSerialFrame       func(deviceID string, frame serialprotocol.Frame) error
	onDisconnect        func(deviceID string)
	onSerialOwnerResult func(deviceID string, result SerialOwnerResult) error
	serialHistoryCursor func(deviceID string, sessionID uint64) (uint64, bool)
	done                chan struct{}
	closeOnce           sync.Once

	mu                 sync.Mutex
	closed             bool
	connections        map[*websocket.Conn]struct{}
	sessions           map[string]*websocket.Conn
	disconnecting      map[string]*websocket.Conn
	snapshotSignals    map[*websocket.Conn]chan struct{}
	latestSnapshot     []byte
	snapshotGeneration uint64
}

type authenticatedDeck struct {
	deviceID       string
	token          string
	deviceIdentity string
	protocol       int
}

type receivedFrame struct {
	messageType websocket.MessageType
	message     []byte
	err         error
}

func New(config Config) (*Hub, error) {
	if config.Authenticator == nil {
		return nil, errors.New("Device Link authenticator is required")
	}
	if config.HeartbeatInterval == 0 {
		config.HeartbeatInterval = defaultHeartbeatInterval
	}
	if config.HeartbeatTimeout == 0 {
		config.HeartbeatTimeout = defaultHeartbeatTimeout
	}
	if config.HeartbeatInterval < 0 || config.HeartbeatTimeout <= config.HeartbeatInterval {
		return nil, errors.New("Device Link heartbeat timing is invalid")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Elapsed == nil {
		started := time.Now()
		config.Elapsed = func() time.Duration { return time.Since(started) }
	}
	return &Hub{
		authenticator:       config.Authenticator,
		heartbeatInterval:   config.HeartbeatInterval,
		heartbeatTimeout:    config.HeartbeatTimeout,
		now:                 config.Now,
		elapsed:             config.Elapsed,
		onDeviceProfile:     config.OnDeviceProfile,
		onSerialState:       config.OnSerialState,
		onSerialFrame:       config.OnSerialFrame,
		onDisconnect:        config.OnDisconnect,
		onSerialOwnerResult: config.OnSerialOwnerResult,
		serialHistoryCursor: config.SerialHistoryCursor,
		done:                make(chan struct{}),
		connections:         make(map[*websocket.Conn]struct{}),
		sessions:            make(map[string]*websocket.Conn),
		disconnecting:       make(map[string]*websocket.Conn),
		snapshotSignals:     make(map[*websocket.Conn]chan struct{}),
	}, nil
}

// PublishSnapshot replaces the latest validated display document and wakes
// every authenticated Deck without waiting for network I/O. Slow Decks
// coalesce intermediate updates and receive the newest complete document.
func (hub *Hub) PublishSnapshot(document []byte) error {
	if len(document) == 0 || len(document) > MaxSnapshotMessageBytes {
		return ErrInvalidSnapshot
	}
	envelope, err := protocol.ParseEnvelope(document)
	if err != nil || envelope.Type != "snapshot.ai" {
		return ErrInvalidSnapshot
	}
	if _, err = aisnapshot.Decode(document); err != nil {
		return ErrInvalidSnapshot
	}
	owned := append([]byte(nil), document...)
	hub.mu.Lock()
	if hub.closed {
		hub.mu.Unlock()
		clear(owned)
		return ErrInvalidSnapshot
	}
	clear(hub.latestSnapshot)
	hub.latestSnapshot = owned
	hub.snapshotGeneration++
	signals := make([]chan struct{}, 0, len(hub.snapshotSignals))
	for _, signal := range hub.snapshotSignals {
		signals = append(signals, signal)
	}
	hub.mu.Unlock()
	for _, signal := range signals {
		select {
		case signal <- struct{}{}:
		default:
		}
	}
	return nil
}

func (hub *Hub) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", http.MethodGet)
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	authentication, ok := requestAuthentication(request)
	if !ok {
		unauthorized(response)
		return
	}
	verified, err := hub.authenticator.Verify(request.Context(), pairing.Authentication{
		DeviceID:        authentication.deviceID,
		Token:           authentication.token,
		DeviceIdentity:  authentication.deviceIdentity,
		ProtocolVersion: authentication.protocol,
	})
	if err != nil {
		http.Error(response, "device trust unavailable", http.StatusServiceUnavailable)
		return
	}
	if !verified || hub.isClosed() {
		unauthorized(response)
		return
	}
	connection, err := websocket.Accept(response, request, &websocket.AcceptOptions{
		Subprotocols: []string{Subprotocol},
	})
	if err != nil {
		return
	}
	if connection.Subprotocol() != Subprotocol || !hub.addConnection(connection) {
		_ = connection.Close(websocket.StatusPolicyViolation, "unsupported Device Link")
		return
	}
	defer func() {
		hub.removeConnection(connection, authentication.deviceID)
		_ = connection.CloseNow()
	}()
	connection.SetReadLimit(MaxControlMessageBytes)
	hub.serveConnection(request.Context(), connection, authentication)
}

func (hub *Hub) Close() {
	hub.closeOnce.Do(func() {
		hub.mu.Lock()
		hub.closed = true
		close(hub.done)
		connections := make([]*websocket.Conn, 0, len(hub.connections))
		for connection := range hub.connections {
			connections = append(connections, connection)
		}
		clear(hub.latestSnapshot)
		hub.latestSnapshot = nil
		hub.mu.Unlock()
		for _, connection := range connections {
			// Runtime shutdown is bounded. A Deck that stops reading must not make
			// each graceful WebSocket close consume the library's five-second wait.
			_ = connection.CloseNow()
		}
	})
}

// ConnectedDecks reports authenticated Device Link sessions. Connections that
// have not completed device.hello are intentionally excluded.
func (hub *Hub) ConnectedDecks() int {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	return len(hub.sessions)
}

func (hub *Hub) RequestSerialOwner(
	ctx context.Context,
	deviceID string,
	request SerialOwnerRequest,
) error {
	request.Type = MessageSerialOwnerRequest
	request.ProtocolVersion = ProtocolVersion
	if request.SerialSessionID == 0 || request.RequestID == 0 {
		return ErrDeviceUnavailable
	}
	return hub.writeDeviceControl(ctx, deviceID, request)
}

func (hub *Hub) SendSerialOwnerActivity(
	ctx context.Context,
	deviceID string,
	activity SerialOwnerActivity,
) error {
	activity.Type = MessageSerialOwnerActivity
	activity.ProtocolVersion = ProtocolVersion
	if activity.SerialSessionID == 0 || activity.LeaseID == 0 {
		return ErrDeviceUnavailable
	}
	return hub.writeDeviceControl(ctx, deviceID, activity)
}

func (hub *Hub) SendSerialWebFrame(
	ctx context.Context,
	deviceID string,
	frame serialprotocol.Frame,
) error {
	if frame.Channel != serialprotocol.ChannelWebTX {
		return ErrDeviceUnavailable
	}
	document, err := serialprotocol.Encode(frame)
	if err != nil {
		return err
	}
	defer clear(document)
	connection := hub.deviceConnection(deviceID)
	if connection == nil || connection.Write(ctx, websocket.MessageBinary, document) != nil {
		return ErrDeviceUnavailable
	}
	return nil
}

func (hub *Hub) writeSerialHistoryRequest(
	ctx context.Context,
	deviceID string,
	sessionID uint64,
	afterSequence uint64,
) error {
	return hub.writeDeviceControl(ctx, deviceID, SerialHistoryRequest{
		Type: MessageSerialHistoryRequest, ProtocolVersion: ProtocolVersion,
		SerialSessionID: sessionID, AfterSequence: afterSequence,
	})
}

func (hub *Hub) writeDeviceControl(ctx context.Context, deviceID string, message any) error {
	document, err := json.Marshal(message)
	if err != nil || len(document) == 0 || len(document) > MaxControlMessageBytes {
		return ErrDeviceUnavailable
	}
	defer clear(document)
	connection := hub.deviceConnection(deviceID)
	if connection == nil || connection.Write(ctx, websocket.MessageText, document) != nil {
		return ErrDeviceUnavailable
	}
	return nil
}

func (hub *Hub) deviceConnection(deviceID string) *websocket.Conn {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return nil
	}
	return hub.sessions[deviceID]
}

func (hub *Hub) isClosed() bool {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	return hub.closed
}

func (hub *Hub) addConnection(connection *websocket.Conn) bool {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return false
	}
	hub.connections[connection] = struct{}{}
	return true
}

func (hub *Hub) reserve(deviceID string, connection *websocket.Conn) bool {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return false
	}
	if _, duplicate := hub.sessions[deviceID]; duplicate {
		return false
	}
	if _, disconnecting := hub.disconnecting[deviceID]; disconnecting {
		return false
	}
	hub.sessions[deviceID] = connection
	hub.snapshotSignals[connection] = make(chan struct{}, 1)
	return true
}

func (hub *Hub) removeConnection(connection *websocket.Conn, deviceID string) {
	hub.mu.Lock()
	delete(hub.connections, connection)
	removedActiveSession := false
	if hub.sessions[deviceID] == connection {
		delete(hub.sessions, deviceID)
		hub.disconnecting[deviceID] = connection
		removedActiveSession = true
	}
	delete(hub.snapshotSignals, connection)
	hub.mu.Unlock()
	if removedActiveSession && hub.onDisconnect != nil {
		hub.onDisconnect(deviceID)
	}
	if removedActiveSession {
		hub.mu.Lock()
		if hub.disconnecting[deviceID] == connection {
			delete(hub.disconnecting, deviceID)
		}
		hub.mu.Unlock()
	}
}

func (hub *Hub) snapshotSignal(connection *websocket.Conn) <-chan struct{} {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	return hub.snapshotSignals[connection]
}

func (hub *Hub) writeLatestSnapshot(
	connection *websocket.Conn,
	previousGeneration uint64,
) (uint64, bool) {
	hub.mu.Lock()
	generation := hub.snapshotGeneration
	if generation == previousGeneration || len(hub.latestSnapshot) == 0 {
		hub.mu.Unlock()
		return previousGeneration, true
	}
	document := append([]byte(nil), hub.latestSnapshot...)
	hub.mu.Unlock()
	defer clear(document)
	ctx, cancel := context.WithTimeout(context.Background(), hub.heartbeatInterval)
	err := connection.Write(ctx, websocket.MessageText, document)
	cancel()
	if err != nil {
		return previousGeneration, false
	}
	return generation, true
}

func (hub *Hub) serveConnection(
	requestContext context.Context,
	connection *websocket.Conn,
	authentication authenticatedDeck,
) {
	helloContext, cancelHello := context.WithTimeout(requestContext, hub.heartbeatTimeout)
	messageType, message, err := connection.Read(helloContext)
	cancelHello()
	if err != nil {
		clear(message)
		return
	}
	if messageType != websocket.MessageText {
		clear(message)
		_ = connection.Close(websocket.StatusPolicyViolation, "device.hello must be text")
		return
	}
	hello, err := parseDeviceHello(message, authentication.deviceID)
	clear(message)
	if err != nil {
		_ = connection.Close(websocket.StatusPolicyViolation, "invalid device.hello")
		return
	}
	if !hub.reserve(authentication.deviceID, connection) {
		_ = connection.Close(websocket.StatusPolicyViolation, "duplicate Device ID")
		return
	}
	if hub.onSerialState != nil {
		if err = hub.onSerialState(
			authentication.deviceID,
			hello.SerialSessionID,
			hello.SerialState,
		); err != nil {
			_ = connection.Close(websocket.StatusPolicyViolation, "invalid Serial Session state")
			return
		}
	}
	if hub.onDeviceProfile != nil {
		capabilities := append([]string(nil), hello.Capabilities...)
		sort.Strings(capabilities)
		hub.onDeviceProfile(configmodel.DeviceProfile{
			DeviceID:        hello.DeviceID,
			FirmwareVersion: hello.FirmwareVersion,
			Board:           hello.Board,
			Capabilities:    capabilities,
			LastSeenUTC:     hub.now().UTC().Format(time.RFC3339Nano),
		})
	}
	if !hub.writeHeartbeat(connection) {
		return
	}
	if hello.SerialSessionID != 0 && hub.serialHistoryCursor != nil {
		if afterSequence, available := hub.serialHistoryCursor(
			authentication.deviceID,
			hello.SerialSessionID,
		); available {
			ctx, cancel := context.WithTimeout(context.Background(), hub.heartbeatInterval)
			err = hub.writeSerialHistoryRequest(
				ctx,
				authentication.deviceID,
				hello.SerialSessionID,
				afterSequence,
			)
			cancel()
			if err != nil {
				return
			}
		}
	}
	snapshotSignal := hub.snapshotSignal(connection)
	var snapshotGeneration uint64
	var snapshotWritten bool
	if snapshotGeneration, snapshotWritten = hub.writeLatestSnapshot(
		connection,
		snapshotGeneration,
	); !snapshotWritten {
		return
	}

	frames := make(chan receivedFrame)
	readerDone := make(chan struct{})
	defer close(readerDone)
	go readFrames(connection, frames, readerDone)
	heartbeatTimer := time.NewTimer(hub.heartbeatTimeout)
	defer heartbeatTimer.Stop()
	heartbeatTicker := time.NewTicker(hub.heartbeatInterval)
	defer heartbeatTicker.Stop()
	var previousMonotonic uint64
	var hasPreviousMonotonic bool
	for {
		select {
		case <-hub.done:
			return
		case <-requestContext.Done():
			return
		case <-heartbeatTimer.C:
			_ = connection.Close(websocket.StatusPolicyViolation, "device heartbeat timeout")
			return
		case <-heartbeatTicker.C:
			verified, verifyErr := hub.authenticator.Verify(requestContext, pairing.Authentication{
				DeviceID:        authentication.deviceID,
				Token:           authentication.token,
				DeviceIdentity:  authentication.deviceIdentity,
				ProtocolVersion: authentication.protocol,
			})
			if verifyErr != nil || !verified {
				_ = connection.Close(websocket.StatusPolicyViolation, "device trust revoked")
				return
			}
			if !hub.writeHeartbeat(connection) {
				return
			}
		case <-snapshotSignal:
			var written bool
			snapshotGeneration, written = hub.writeLatestSnapshot(
				connection,
				snapshotGeneration,
			)
			if !written {
				return
			}
		case frame := <-frames:
			keepConnection := func() bool {
				defer clear(frame.message)
				if frame.err != nil {
					return false
				}
				if frame.messageType == websocket.MessageBinary {
					serialFrame, decodeErr := serialprotocol.Decode(frame.message)
					defer clear(serialFrame.Payload)
					if decodeErr != nil || hub.onSerialFrame == nil ||
						hub.onSerialFrame(authentication.deviceID, serialFrame) != nil {
						_ = connection.Close(websocket.StatusPolicyViolation, "invalid Serial binary frame")
						return false
					}
					return true
				}
				if frame.messageType != websocket.MessageText {
					_ = connection.Close(websocket.StatusPolicyViolation, "unsupported Device Link frame")
					return false
				}
				envelope, envelopeErr := protocol.ParseEnvelope(frame.message)
				if envelopeErr != nil {
					_ = connection.Close(websocket.StatusPolicyViolation, "invalid Device Link control message")
					return false
				}
				switch envelope.Type {
				case MessageHeartbeat:
					heartbeat, parseErr := parseHeartbeat(
						frame.message,
						previousMonotonic,
						hasPreviousMonotonic,
					)
					if parseErr != nil {
						_ = connection.Close(websocket.StatusPolicyViolation, "invalid device.heartbeat")
						return false
					}
					previousMonotonic = heartbeat.MonotonicMS
					hasPreviousMonotonic = true
					if !heartbeatTimer.Stop() {
						select {
						case <-heartbeatTimer.C:
						default:
						}
					}
					heartbeatTimer.Reset(hub.heartbeatTimeout)
				case MessageSerialState:
					state, parseErr := parseSerialState(frame.message)
					if parseErr != nil || hub.onSerialState == nil ||
						hub.onSerialState(
							authentication.deviceID,
							state.SerialSessionID,
							state.SerialState,
						) != nil {
						_ = connection.Close(websocket.StatusPolicyViolation, "invalid serial.state")
						return false
					}
					if state.SerialSessionID != 0 && hub.serialHistoryCursor != nil {
						if afterSequence, available := hub.serialHistoryCursor(
							authentication.deviceID,
							state.SerialSessionID,
						); available {
							writeContext, cancel := context.WithTimeout(
								context.Background(),
								hub.heartbeatInterval,
							)
							writeErr := hub.writeSerialHistoryRequest(
								writeContext,
								authentication.deviceID,
								state.SerialSessionID,
								afterSequence,
							)
							cancel()
							if writeErr != nil {
								return false
							}
						}
					}
				case MessageSerialOwnerResult:
					result, parseErr := parseSerialOwnerResult(frame.message)
					if parseErr != nil || hub.onSerialOwnerResult == nil ||
						hub.onSerialOwnerResult(authentication.deviceID, result) != nil {
						_ = connection.Close(websocket.StatusPolicyViolation, "invalid serial.owner.result")
						return false
					}
				default:
					_ = connection.Close(websocket.StatusPolicyViolation, "unsupported Device Link control message")
					return false
				}
				return true
			}()
			if !keepConnection {
				return
			}
			go readFrames(connection, frames, readerDone)
		}
	}
}

func readFrames(
	connection *websocket.Conn,
	frames chan<- receivedFrame,
	done <-chan struct{},
) {
	messageType, message, err := connection.Read(context.Background())
	select {
	case frames <- receivedFrame{messageType: messageType, message: message, err: err}:
	case <-done:
		clear(message)
	}
}

func (hub *Hub) writeHeartbeat(connection *websocket.Conn) bool {
	now := hub.now()
	elapsed := hub.elapsed()
	if elapsed < 0 {
		elapsed = 0
	}
	message, err := json.Marshal(Heartbeat{
		Type:            MessageHeartbeat,
		ProtocolVersion: ProtocolVersion,
		UTC:             now.UTC().Format(time.RFC3339Nano),
		MonotonicMS:     uint64(elapsed / time.Millisecond),
		TXQueueDepth:    0,
		TXQueueCapacity: 1,
		RXQueueDepth:    0,
		RXQueueCapacity: 1,
	})
	if err != nil || len(message) > MaxControlMessageBytes {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), hub.heartbeatInterval)
	defer cancel()
	return connection.Write(ctx, websocket.MessageText, message) == nil
}

func requestAuthentication(request *http.Request) (authenticatedDeck, bool) {
	if len(request.Header.Values("Authorization")) != 1 ||
		len(request.Header.Values("X-Device-ID")) != 1 ||
		len(request.Header.Values("X-Device-Identity")) != 1 ||
		len(request.Header.Values("X-Protocol-Version")) != 1 {
		return authenticatedDeck{}, false
	}
	scheme, token, found := strings.Cut(request.Header.Get("Authorization"), " ")
	protocolVersion, err := strconv.Atoi(request.Header.Get("X-Protocol-Version"))
	deviceID := request.Header.Get("X-Device-ID")
	identity := request.Header.Get("X-Device-Identity")
	if !found || scheme != "Bearer" || token == "" || len(token) > 128 ||
		len(deviceID) > 64 || len(identity) > 683 || err != nil ||
		protocolVersion != ProtocolVersion {
		return authenticatedDeck{}, false
	}
	return authenticatedDeck{
		deviceID:       deviceID,
		token:          token,
		deviceIdentity: identity,
		protocol:       protocolVersion,
	}, true
}

func unauthorized(response http.ResponseWriter) {
	response.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(response, "unauthorized", http.StatusUnauthorized)
}
