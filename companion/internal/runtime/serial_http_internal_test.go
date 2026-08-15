package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairing"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/serialhub"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/serialprotocol"
	"github.com/coder/websocket"
)

type serialHTTPClock struct{ now time.Time }

func (clock serialHTTPClock) Now() time.Time { return clock.now }

func newSerialHTTPRuntime(t *testing.T) *Runtime {
	t.Helper()
	certificate := []byte("serial-http-test-certificate")
	digest := sha256.Sum256(certificate)
	pairingService, err := pairing.New(pairing.Config{
		Clock:                  serialHTTPClock{now: time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)},
		Store:                  pairing.NewMemoryStore(),
		CertificateFingerprint: "sha256:" + hex.EncodeToString(digest[:]),
		CertificateDER:         certificate,
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := New(Config{
		Version: "serial-http-test",
		Management: ManagementConfig{
			Address: "127.0.0.1:7777", AdminToken: "serial-http-management-token",
		},
		DeviceHub: DeviceHubConfig{Address: "127.0.0.1:7780"},
		Pairing:   pairingService,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		application.deviceLink.Close()
		application.serialHub.Close()
	})
	return application
}

func TestSerialObserverStreamsIndependentBinaryHistoryAndDownloadIsBounded(t *testing.T) {
	application := newSerialHTTPRuntime(t)
	if err := application.serialHub.Reconcile("deck-observe", 99, serialhub.StateUSBTX); err != nil {
		t.Fatal(err)
	}
	for sequence, payload := range [][]byte{{0x00, 0xff}, []byte("two")} {
		if err := application.serialHub.Ingest("deck-observe", serialprotocol.Frame{
			Channel: serialprotocol.ChannelTargetRX, SessionID: 99,
			Sequence: uint64(sequence + 1), MonotonicMS: uint64(sequence * 10),
			Payload: payload,
		}); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(application.managementRoutes())
	defer server.Close()
	application.config.Management.AllowedOrigin = server.URL
	const sessionToken = "serial-observer-session"
	if !application.sessions.add(sessionToken, "unused-csrf", time.Now()) {
		t.Fatal("add management session")
	}

	dialObserver := func() *websocket.Conn {
		header := http.Header{}
		header.Set("Cookie", managementSessionCookie+"="+sessionToken)
		header.Set("Origin", server.URL)
		connection, response, err := websocket.Dial(
			context.Background(),
			"ws"+server.URL[4:]+"/api/v1/serial/observe",
			&websocket.DialOptions{HTTPHeader: header, Subprotocols: []string{serialObserverSubprotocol}},
		)
		if err != nil {
			t.Fatalf("Dial(observer) response=%v error=%v", response, err)
		}
		return connection
	}
	first := dialObserver()
	defer first.CloseNow()
	second := dialObserver()
	defer second.CloseNow()
	for _, connection := range []*websocket.Conn{first, second} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		messageType, _, err := connection.Read(ctx)
		cancel()
		if err != nil || messageType != websocket.MessageText {
			t.Fatalf("initial observer state type=%v error=%v", messageType, err)
		}
		for sequence := uint64(1); sequence <= 2; sequence++ {
			ctx, cancel = context.WithTimeout(context.Background(), time.Second)
			messageType, document, err := connection.Read(ctx)
			cancel()
			if err != nil || messageType != websocket.MessageBinary {
				t.Fatalf("observer frame type=%v error=%v", messageType, err)
			}
			frame, decodeErr := serialprotocol.Decode(document)
			if decodeErr != nil || frame.Sequence != sequence || frame.SessionID != 99 {
				t.Fatalf("observer frame=%#v error=%v", frame, decodeErr)
			}
		}
	}

	request, err := http.NewRequest(
		http.MethodGet,
		server.URL+"/api/v1/serial/download?session_id=99&from_sequence=1&maximum_bytes=5",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(&http.Cookie{Name: managementSessionCookie, Value: sessionToken})
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	download, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(download) != 5 || download[0] != 0 || download[1] != 0xff || string(download[2:]) != "two" {
		t.Fatalf("download status=%d bytes=%x", response.StatusCode, download)
	}

	request, _ = http.NewRequest(
		http.MethodGet,
		server.URL+"/api/v1/serial/download?session_id=99&maximum_bytes=4",
		nil,
	)
	request.AddCookie(&http.Cookie{Name: managementSessionCookie, Value: sessionToken})
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize download status=%d", response.StatusCode)
	}
}

func TestSerialManagementEndpointsRequireAuthenticationAndOrigin(t *testing.T) {
	application := newSerialHTTPRuntime(t)
	server := httptest.NewServer(application.managementRoutes())
	defer server.Close()
	application.config.Management.AllowedOrigin = server.URL
	response, err := http.Get(server.URL + "/api/v1/serial/status")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", response.StatusCode)
	}
}

func TestSerialStatusNeverExposesObserverOrLeaseCapabilities(t *testing.T) {
	application := newSerialHTTPRuntime(t)
	if err := application.serialHub.Reconcile("deck-status", 101, serialhub.StateUSBTX); err != nil {
		t.Fatal(err)
	}
	request, err := application.serialHub.Leases().Acquire("private-browser-id", 101)
	if err != nil || request.RequestID == 0 {
		t.Fatalf("Acquire() = %#v, %v", request, err)
	}
	server := httptest.NewServer(application.managementRoutes())
	defer server.Close()
	const sessionToken = "serial-status-session"
	if !application.sessions.add(sessionToken, "unused-csrf", time.Now()) {
		t.Fatal("add management session")
	}
	httpRequest, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/serial/status", nil)
	httpRequest.AddCookie(&http.Cookie{Name: managementSessionCookie, Value: sessionToken})
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		t.Fatal(err)
	}
	document, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK ||
		bytes.Contains(document, []byte("private-browser-id")) ||
		bytes.Contains(document, []byte(`"lease_id"`)) ||
		bytes.Contains(document, []byte(`"client_id"`)) {
		t.Fatalf("public Serial status leaked a capability: status=%d document=%s", response.StatusCode, document)
	}
}
