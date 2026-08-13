package devicelink

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairing"
	"github.com/coder/websocket"
)

const (
	testDeviceID       = "deck-a1b2c3d4"
	testDeviceToken    = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFG"
	testDeviceIdentity = "ZGVjay1kZXZpY2UtaWRlbnRpdHktbWF0ZXJpYWw"
)

type testAuthenticator struct {
	mu      sync.Mutex
	revoked bool
}

func (authenticator *testAuthenticator) Verify(
	_ context.Context,
	authentication pairing.Authentication,
) (bool, error) {
	authenticator.mu.Lock()
	defer authenticator.mu.Unlock()
	return !authenticator.revoked &&
		authentication.DeviceID == testDeviceID &&
		authentication.Token == testDeviceToken &&
		authentication.DeviceIdentity == testDeviceIdentity &&
		authentication.ProtocolVersion == 1, nil
}

func (authenticator *testAuthenticator) revoke() {
	authenticator.mu.Lock()
	authenticator.revoked = true
	authenticator.mu.Unlock()
}

func newTestHub(t *testing.T) (*Hub, *testAuthenticator, *httptest.Server) {
	t.Helper()
	authenticator := &testAuthenticator{}
	hub, err := New(Config{
		Authenticator:     authenticator,
		HeartbeatInterval: 20 * time.Millisecond,
		HeartbeatTimeout:  100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server := httptest.NewTLSServer(hub)
	t.Cleanup(func() {
		hub.Close()
		server.Close()
	})
	return hub, authenticator, server
}

func dialTestDeck(
	t *testing.T,
	server *httptest.Server,
	deviceID string,
	token string,
) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	header.Set("X-Device-ID", deviceID)
	header.Set("X-Device-Identity", testDeviceIdentity)
	header.Set("X-Protocol-Version", "1")
	return websocket.Dial(context.Background(), "wss"+server.URL[5:], &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true, // httptest certificate; production Deck pins it.
		}}},
		HTTPHeader:   header,
		Subprotocols: []string{Subprotocol},
	})
}

func validHello() []byte {
	message, _ := json.Marshal(DeviceHello{
		Type:            MessageDeviceHello,
		ProtocolVersion: ProtocolVersion,
		DeviceID:        testDeviceID,
		FirmwareVersion: "0.2.0-dev",
		Board:           BoardESP32S3RLCD42,
		Capabilities:    []string{"display", "serial", "ota"},
		SerialState:     "disarmed",
	})
	return message
}

func validHeartbeat(monotonic uint64) []byte {
	message, _ := json.Marshal(Heartbeat{
		Type:            MessageHeartbeat,
		ProtocolVersion: ProtocolVersion,
		UTC:             "2026-08-13T01:02:03Z",
		MonotonicMS:     monotonic,
		TXQueueDepth:    0,
		TXQueueCapacity: 8,
		RXQueueDepth:    0,
		RXQueueCapacity: 8,
	})
	return message
}

func readText(t *testing.T, connection *websocket.Conn, timeout time.Duration) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	messageType, message, err := connection.Read(ctx)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if messageType != websocket.MessageText {
		t.Fatalf("message type = %v, want text", messageType)
	}
	return message
}

func TestHubAuthenticatesThenRequiresDeviceHelloBeforeHeartbeat(t *testing.T) {
	_, _, server := newTestHub(t)
	connection, response, err := dialTestDeck(t, server, testDeviceID, testDeviceToken)
	if err != nil {
		t.Fatalf("Dial() status = %v, error = %v", response, err)
	}
	defer connection.CloseNow()
	if connection.Subprotocol() != Subprotocol {
		t.Fatalf("subprotocol = %q", connection.Subprotocol())
	}
	if err = connection.Write(context.Background(), websocket.MessageText, validHello()); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	var heartbeat Heartbeat
	if err = json.Unmarshal(readText(t, connection, time.Second), &heartbeat); err != nil {
		t.Fatalf("decode heartbeat: %v", err)
	}
	if heartbeat.Type != MessageHeartbeat || heartbeat.ProtocolVersion != ProtocolVersion ||
		heartbeat.UTC == "" || heartbeat.RXQueueCapacity == 0 || heartbeat.TXQueueCapacity == 0 {
		t.Fatalf("heartbeat = %#v", heartbeat)
	}
}

func TestHubRejectsWrongOrRevokedDeviceTokenBeforeUpgrade(t *testing.T) {
	_, authenticator, server := newTestHub(t)
	for name, token := range map[string]string{"wrong": "wrong-token", "revoked": testDeviceToken} {
		t.Run(name, func(t *testing.T) {
			if name == "revoked" {
				authenticator.revoke()
			}
			connection, response, err := dialTestDeck(t, server, testDeviceID, token)
			if connection != nil {
				connection.CloseNow()
			}
			if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("Dial() response = %#v, error = %v", response, err)
			}
		})
	}
}

func TestHubRejectsInvalidFirstMessageAndOversizeFrame(t *testing.T) {
	tests := []struct {
		name        string
		messageType websocket.MessageType
		message     []byte
	}{
		{"heartbeat before hello", websocket.MessageText, validHeartbeat(1)},
		{"binary hello", websocket.MessageBinary, validHello()},
		{"oversize hello", websocket.MessageText, append(validHello(), make([]byte, MaxControlMessageBytes)...)},
	}
	wrongBoard := DeviceHello{}
	_ = json.Unmarshal(validHello(), &wrongBoard)
	wrongBoard.Board = "other-board"
	wrongBoardMessage, _ := json.Marshal(wrongBoard)
	tests = append(tests, struct {
		name        string
		messageType websocket.MessageType
		message     []byte
	}{"wrong board", websocket.MessageText, wrongBoardMessage})

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, _, server := newTestHub(t)
			connection, _, err := dialTestDeck(t, server, testDeviceID, testDeviceToken)
			if err != nil {
				t.Fatalf("Dial() error = %v", err)
			}
			defer connection.CloseNow()
			_ = connection.Write(context.Background(), testCase.messageType, testCase.message)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, _, err = connection.Read(ctx)
			if status := websocket.CloseStatus(err); status != websocket.StatusPolicyViolation &&
				status != websocket.StatusMessageTooBig {
				t.Fatalf("close status = %v, error = %v", status, err)
			}
		})
	}
}

func TestHubRejectsDuplicateDeviceIDAndAllowsReconnectAfterClose(t *testing.T) {
	_, _, server := newTestHub(t)
	first, _, err := dialTestDeck(t, server, testDeviceID, testDeviceToken)
	if err != nil {
		t.Fatalf("first Dial() error = %v", err)
	}
	if err = first.Write(context.Background(), websocket.MessageText, validHello()); err != nil {
		t.Fatalf("first hello: %v", err)
	}
	_ = readText(t, first, time.Second)

	duplicate, _, err := dialTestDeck(t, server, testDeviceID, testDeviceToken)
	if err != nil {
		t.Fatalf("duplicate Dial() error = %v", err)
	}
	_ = duplicate.Write(context.Background(), websocket.MessageText, validHello())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	_, _, duplicateErr := duplicate.Read(ctx)
	cancel()
	duplicate.CloseNow()
	if websocket.CloseStatus(duplicateErr) != websocket.StatusPolicyViolation {
		t.Fatalf("duplicate close = %v", duplicateErr)
	}

	_ = first.Close(websocket.StatusNormalClosure, "test reconnect")
	time.Sleep(30 * time.Millisecond)
	reconnected, _, err := dialTestDeck(t, server, testDeviceID, testDeviceToken)
	if err != nil {
		t.Fatalf("reconnect Dial() error = %v", err)
	}
	defer reconnected.CloseNow()
	if err = reconnected.Write(context.Background(), websocket.MessageText, validHello()); err != nil {
		t.Fatalf("reconnect hello: %v", err)
	}
	_ = readText(t, reconnected, time.Second)
}

func TestHubCloseIsBoundedWhenADeckDoesNotAnswerTheCloseHandshake(t *testing.T) {
	hub, _, server := newTestHub(t)
	connection, _, err := dialTestDeck(t, server, testDeviceID, testDeviceToken)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	if err = connection.Write(context.Background(), websocket.MessageText, validHello()); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	_ = readText(t, connection, time.Second)

	started := time.Now()
	hub.Close()
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("Hub.Close() took %v with an unresponsive Deck", elapsed)
	}
}

func TestHubDropsLostHeartbeatOutOfOrderAndRevokedSession(t *testing.T) {
	t.Run("lost heartbeat", func(t *testing.T) {
		_, _, server := newTestHub(t)
		connection, _, err := dialTestDeck(t, server, testDeviceID, testDeviceToken)
		if err != nil {
			t.Fatal(err)
		}
		defer connection.CloseNow()
		_ = connection.Write(context.Background(), websocket.MessageText, validHello())
		for {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_, _, readErr := connection.Read(ctx)
			cancel()
			if readErr != nil {
				if websocket.CloseStatus(readErr) != websocket.StatusPolicyViolation {
					t.Fatalf("lost heartbeat close = %v", readErr)
				}
				break
			}
		}
	})

	t.Run("monotonic time regresses", func(t *testing.T) {
		_, _, server := newTestHub(t)
		connection, _, err := dialTestDeck(t, server, testDeviceID, testDeviceToken)
		if err != nil {
			t.Fatal(err)
		}
		defer connection.CloseNow()
		_ = connection.Write(context.Background(), websocket.MessageText, validHello())
		_ = readText(t, connection, time.Second)
		_ = connection.Write(context.Background(), websocket.MessageText, validHeartbeat(20))
		_ = connection.Write(context.Background(), websocket.MessageText, validHeartbeat(19))
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		for {
			_, _, readErr := connection.Read(ctx)
			if readErr != nil {
				if websocket.CloseStatus(readErr) != websocket.StatusPolicyViolation {
					t.Fatalf("out-of-order close = %v", readErr)
				}
				break
			}
		}
	})

	t.Run("trust revoked while connected", func(t *testing.T) {
		_, authenticator, server := newTestHub(t)
		connection, _, err := dialTestDeck(t, server, testDeviceID, testDeviceToken)
		if err != nil {
			t.Fatal(err)
		}
		defer connection.CloseNow()
		_ = connection.Write(context.Background(), websocket.MessageText, validHello())
		_ = readText(t, connection, time.Second)
		authenticator.revoke()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		for {
			_, _, readErr := connection.Read(ctx)
			if readErr != nil {
				if websocket.CloseStatus(readErr) != websocket.StatusPolicyViolation {
					t.Fatalf("revoked close = %v", readErr)
				}
				break
			}
		}
	})
}

func TestAuthenticationHeaderContractRejectsMalformedValues(t *testing.T) {
	_, _, server := newTestHub(t)
	header := http.Header{
		"Authorization":      {"Bearer " + testDeviceToken},
		"X-Device-ID":        {testDeviceID},
		"X-Device-Identity":  {base64.RawURLEncoding.EncodeToString([]byte("too short"))},
		"X-Protocol-Version": {strconv.Itoa(ProtocolVersion)},
	}
	connection, response, err := websocket.Dial(
		context.Background(),
		"wss"+server.URL[5:],
		&websocket.DialOptions{
			HTTPClient: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12, InsecureSkipVerify: true,
			}}},
			HTTPHeader:   header,
			Subprotocols: []string{Subprotocol},
		},
	)
	if connection != nil {
		connection.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("Dial() response = %#v, error = %v", response, err)
	}
}
