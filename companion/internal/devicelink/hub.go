package devicelink

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairing"
	"github.com/coder/websocket"
)

const (
	defaultHeartbeatInterval = 10 * time.Second
	defaultHeartbeatTimeout  = 30 * time.Second
)

type Authenticator interface {
	Verify(context.Context, pairing.Authentication) (bool, error)
}

type Config struct {
	Authenticator         Authenticator
	HeartbeatInterval     time.Duration
	HeartbeatTimeout      time.Duration
	ServerProtocolVersion int
	Now                   func() time.Time
	Elapsed               func() time.Duration
}

type Hub struct {
	authenticator         Authenticator
	heartbeatInterval     time.Duration
	heartbeatTimeout      time.Duration
	serverProtocolVersion int
	now                   func() time.Time
	elapsed               func() time.Duration
	done                  chan struct{}
	closeOnce             sync.Once

	mu                   sync.Mutex
	closed               bool
	connections          map[*websocket.Conn]struct{}
	sessions             map[string]*websocket.Conn
	acceptedConnections  uint64
	disconnections       uint64
	authenticationErrors uint64
	protocolErrors       uint64
}

// Snapshot is the complete redacted Device Link observation surface. It is
// safe to expose through management status because it contains counters only,
// never device identities, bearer tokens, or certificate material.
type Snapshot struct {
	ConnectedDecks       int    `json:"connected_decks"`
	AcceptedConnections  uint64 `json:"accepted_connections"`
	Disconnections       uint64 `json:"disconnections"`
	AuthenticationErrors uint64 `json:"authentication_errors"`
	ProtocolErrors       uint64 `json:"protocol_errors"`
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
	if config.ServerProtocolVersion == 0 {
		config.ServerProtocolVersion = ProtocolVersion
	}
	if config.ServerProtocolVersion < 1 {
		return nil, errors.New("Device Link server protocol version is invalid")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Elapsed == nil {
		started := time.Now()
		config.Elapsed = func() time.Duration { return time.Since(started) }
	}
	return &Hub{
		authenticator:         config.Authenticator,
		heartbeatInterval:     config.HeartbeatInterval,
		heartbeatTimeout:      config.HeartbeatTimeout,
		serverProtocolVersion: config.ServerProtocolVersion,
		now:                   config.Now,
		elapsed:               config.Elapsed,
		done:                  make(chan struct{}),
		connections:           make(map[*websocket.Conn]struct{}),
		sessions:              make(map[string]*websocket.Conn),
	}, nil
}

func (hub *Hub) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", http.MethodGet)
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	authentication, ok := requestAuthentication(request)
	if !ok {
		hub.recordAuthenticationError()
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
		if !verified {
			hub.recordAuthenticationError()
		}
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
		hub.recordProtocolError()
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
		hub.mu.Unlock()
		for _, connection := range connections {
			// Runtime shutdown is bounded. A Deck that stops reading must not make
			// each graceful WebSocket close consume the library's five-second wait.
			_ = connection.CloseNow()
		}
	})
}

// Snapshot reports authenticated sessions and lifecycle counters. Connections
// that have not completed device.hello are intentionally excluded.
func (hub *Hub) Snapshot() Snapshot {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	return Snapshot{
		ConnectedDecks:       len(hub.sessions),
		AcceptedConnections:  hub.acceptedConnections,
		Disconnections:       hub.disconnections,
		AuthenticationErrors: hub.authenticationErrors,
		ProtocolErrors:       hub.protocolErrors,
	}
}

func (hub *Hub) ConnectedDecks() int {
	return hub.Snapshot().ConnectedDecks
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
	hub.sessions[deviceID] = connection
	hub.acceptedConnections++
	return true
}

func (hub *Hub) removeConnection(connection *websocket.Conn, deviceID string) {
	hub.mu.Lock()
	delete(hub.connections, connection)
	if hub.sessions[deviceID] == connection {
		delete(hub.sessions, deviceID)
		hub.disconnections++
	}
	hub.mu.Unlock()
}

func (hub *Hub) recordAuthenticationError() {
	hub.mu.Lock()
	hub.authenticationErrors++
	hub.mu.Unlock()
}

func (hub *Hub) recordProtocolError() {
	hub.mu.Lock()
	hub.protocolErrors++
	hub.mu.Unlock()
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
		return
	}
	if messageType != websocket.MessageText {
		hub.recordProtocolError()
		_ = connection.Close(websocket.StatusPolicyViolation, "device.hello must be text")
		return
	}
	if _, err = parseDeviceHello(message, authentication.deviceID); err != nil {
		hub.recordProtocolError()
		_ = connection.Close(websocket.StatusPolicyViolation, "invalid device.hello")
		return
	}
	if !hub.reserve(authentication.deviceID, connection) {
		hub.recordProtocolError()
		_ = connection.Close(websocket.StatusPolicyViolation, "duplicate Device ID")
		return
	}
	if !hub.writeHeartbeat(connection) {
		return
	}

	frames := make(chan receivedFrame, 1)
	go readFrames(connection, frames)
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
			hub.recordProtocolError()
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
		case frame := <-frames:
			if frame.err != nil {
				return
			}
			if frame.messageType != websocket.MessageText {
				hub.recordProtocolError()
				_ = connection.Close(websocket.StatusPolicyViolation, "control messages must be text")
				return
			}
			heartbeat, parseErr := parseHeartbeat(
				frame.message,
				previousMonotonic,
				hasPreviousMonotonic,
			)
			if parseErr != nil {
				hub.recordProtocolError()
				_ = connection.Close(websocket.StatusPolicyViolation, "invalid device.heartbeat")
				return
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
			go readFrames(connection, frames)
		}
	}
}

func readFrames(connection *websocket.Conn, frames chan<- receivedFrame) {
	messageType, message, err := connection.Read(context.Background())
	frames <- receivedFrame{messageType: messageType, message: message, err: err}
}

func (hub *Hub) writeHeartbeat(connection *websocket.Conn) bool {
	now := hub.now()
	elapsed := hub.elapsed()
	if elapsed < 0 {
		elapsed = 0
	}
	message, err := json.Marshal(Heartbeat{
		Type:            MessageHeartbeat,
		ProtocolVersion: hub.serverProtocolVersion,
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
