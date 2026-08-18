package pairingv2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairing"
)

type coordinatorTrustRandom struct {
	mutex sync.Mutex
	value byte
}

func (*coordinatorTrustRandom) Number(int) (int, error) {
	return 0, errors.New("number generation is not used by Pairing v2")
}

func (random *coordinatorTrustRandom) Bytes(count int) ([]byte, error) {
	random.mutex.Lock()
	defer random.mutex.Unlock()
	if count != 32 {
		return nil, errors.New("unexpected token size")
	}
	value := bytes.Repeat([]byte{random.value}, count)
	random.value++
	return value, nil
}

type scriptedPairingChannel struct {
	mutex               sync.Mutex
	credentials         Credentials
	ready               CommitReady
	credentialsReceived chan struct{}
	commitReceived      chan struct{}
	badTranscript       bool
	badReceipt          bool
	credentialErrorCode string
	commitErrorCode     string
	connectError        error
	closed              bool
}

func newScriptedPairingChannel() *scriptedPairingChannel {
	return &scriptedPairingChannel{
		credentialsReceived: make(chan struct{}, 1),
		commitReceived:      make(chan struct{}, 1),
	}
}

func (channel *scriptedPairingChannel) Exchange(
	_ context.Context,
	endpoint string,
	document []byte,
) ([]byte, error) {
	if endpoint != transactionEndpoint {
		return nil, errors.New("unexpected endpoint")
	}
	message, err := DecodeContractMessage(document)
	if err != nil {
		return nil, err
	}
	switch value := message.(type) {
	case *Credentials:
		channel.mutex.Lock()
		channel.credentials = *value
		if channel.credentialErrorCode != "" {
			failure := Error{
				Type: "pairing.error", ProtocolVersion: ContractVersion,
				SessionID: value.SessionID, TransactionID: value.TransactionID,
				Sequence: 2, Code: channel.credentialErrorCode,
			}
			response, marshalErr := json.Marshal(failure)
			channel.mutex.Unlock()
			channel.credentialsReceived <- struct{}{}
			return response, marshalErr
		}
		channel.ready = CommitReady{
			Type: "pairing.commit_ready", ProtocolVersion: ContractVersion,
			SessionID: value.SessionID, TransactionID: value.TransactionID, Sequence: 2,
			WindowNonce: value.WindowNonce, CompanionNonce: value.CompanionNonce,
			DeckNonce:        "11111111111111111111111111111111",
			DeviceID:         "deck_12345678",
			DeviceIdentity:   "ZGV2aWNlLWlkZW50aXR5LTE",
			ProfileID:        value.CertificateFingerprint,
			TranscriptSHA256: "sha256:" + strings.Repeat("0", 64),
		}
		digest, digestErr := TranscriptSHA256(*value, channel.ready)
		if digestErr != nil {
			channel.mutex.Unlock()
			return nil, digestErr
		}
		if channel.badTranscript {
			digest = "sha256:" + strings.Repeat("f", 64)
		}
		channel.ready.TranscriptSHA256 = digest
		response, marshalErr := json.Marshal(channel.ready)
		channel.mutex.Unlock()
		channel.credentialsReceived <- struct{}{}
		return response, marshalErr
	case *Commit:
		channel.mutex.Lock()
		ready := channel.ready
		if channel.commitErrorCode != "" {
			failure := Error{
				Type: "pairing.error", ProtocolVersion: ContractVersion,
				SessionID: value.SessionID, TransactionID: value.TransactionID,
				Sequence: 4, Code: channel.commitErrorCode,
			}
			response, marshalErr := json.Marshal(failure)
			channel.mutex.Unlock()
			channel.commitReceived <- struct{}{}
			return response, marshalErr
		}
		valid := value.SessionID == ready.SessionID &&
			value.TransactionID == ready.TransactionID && value.DeckNonce == ready.DeckNonce &&
			value.TranscriptSHA256 == ready.TranscriptSHA256
		receipt := CommitReceipt{
			Type: "pairing.commit_receipt", ProtocolVersion: ContractVersion,
			SessionID: value.SessionID, TransactionID: value.TransactionID, Sequence: 4,
			ProfileID: ready.ProfileID, ProfileGeneration: 7,
			TranscriptSHA256: value.TranscriptSHA256,
		}
		if channel.badReceipt {
			receipt.ProfileID = "sha256:" + strings.Repeat("f", 64)
		}
		response, marshalErr := json.Marshal(receipt)
		channel.mutex.Unlock()
		if !valid {
			return nil, errors.New("invalid commit")
		}
		channel.commitReceived <- struct{}{}
		return response, marshalErr
	default:
		return nil, errors.New("unexpected Pairing v2 transaction message")
	}
}

func (channel *scriptedPairingChannel) Close() {
	channel.mutex.Lock()
	channel.closed = true
	channel.mutex.Unlock()
}

func (channel *scriptedPairingChannel) proofIdentity() (string, string) {
	channel.mutex.Lock()
	defer channel.mutex.Unlock()
	return channel.ready.DeviceID, channel.ready.TransactionID
}

func newCoordinatorFixture(
	t *testing.T,
	channel *scriptedPairingChannel,
) (*Coordinator, *pairing.Service, SessionView) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)}
	source := &fakeSource{observations: []Observation{validObservation(0x2a)}}
	randomBytes := make([]byte, candidateRefBytes+candidateRefBytes+16*3)
	for index := range randomBytes {
		randomBytes[index] = byte(index + 1)
	}
	discovery, err := NewDiscovery(DiscoveryConfig{
		Source: source, Clock: clock,
		Random:       bytes.NewReader(randomBytes[:candidateRefBytes]),
		CandidateTTL: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := discovery.Scan(context.Background())
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Scan() = %#v, %v", candidates, err)
	}
	certificate := []byte("pairing-v2-test-certificate")
	certificateDigest := sha256.Sum256(certificate)
	trust, err := pairing.New(pairing.Config{
		Clock: clock, Store: pairing.NewMemoryStore(),
		Random:                 &coordinatorTrustRandom{value: 0x41},
		CertificateFingerprint: "sha256:" + hex.EncodeToString(certificateDigest[:]),
		CertificateDER:         certificate,
		CodePepper:             bytes.Repeat([]byte{0x99}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Discovery: discovery, Trust: trust, Clock: clock,
		Random: bytes.NewReader(randomBytes[candidateRefBytes:]),
		Connect: func(context.Context, Route, []byte) (SecureChannel, error) {
			if channel.connectError != nil {
				return nil, channel.connectError
			}
			return channel, nil
		},
		Hub: func(context.Context) (HubLocator, error) {
			return HubLocator{
				Service: "s3deck-companion-a1b2._s3rlcd-hub._tcp.local.",
				Address: "192.168.31.3:7780",
			}, nil
		},
		SessionTTL:       2 * time.Minute,
		LinkProofTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := coordinator.Begin(candidates[0].Reference)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator, trust, session
}

func TestCoordinatorCommitsOnlyAfterExactProvisionalHeartbeat(t *testing.T) {
	channel := newScriptedPairingChannel()
	coordinator, trust, session := newCoordinatorFixture(t, channel)
	type result struct {
		view SessionView
		err  error
	}
	completed := make(chan result, 1)
	go func() {
		view, err := coordinator.Confirm(context.Background(), session.Reference, "012345")
		completed <- result{view: view, err: err}
	}()
	select {
	case <-channel.credentialsReceived:
	case <-time.After(time.Second):
		t.Fatal("credentials were not sent")
	}
	waitForCoordinatorState(t, coordinator, session.Reference, SessionProvingLink)
	deviceID, transactionID := channel.proofIdentity()
	if err := coordinator.ObserveProvisionalHeartbeat(deviceID, transactionID); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-completed:
		if outcome.err != nil || outcome.view.State != SessionPaired {
			t.Fatalf("Confirm() = %#v, %v", outcome.view, outcome.err)
		}
	case <-time.After(time.Second):
		t.Fatal("Pairing v2 transaction did not complete")
	}
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x41}, 32))
	valid, err := trust.Verify(context.Background(), pairing.Authentication{
		DeviceID: deviceID, Token: token,
		DeviceIdentity:  "ZGV2aWNlLWlkZW50aXR5LTE",
		ProtocolVersion: pairing.ProtocolVersion,
	})
	if err != nil || !valid {
		t.Fatalf("committed trust Verify() = %t, %v", valid, err)
	}
	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"192.168.31.3", token, "ZGV2aWNl", transactionID} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("session view leaked %q: %s", secret, encoded)
		}
	}
}

func TestCoordinatorRejectsAlteredTranscriptBeforeTrustOrCommit(t *testing.T) {
	channel := newScriptedPairingChannel()
	channel.badTranscript = true
	coordinator, trust, session := newCoordinatorFixture(t, channel)
	view, err := coordinator.Confirm(context.Background(), session.Reference, "012345")
	if !errors.Is(err, ErrPairingFailed) || view.State != SessionFailed {
		t.Fatalf("Confirm(altered transcript) = %#v, %v", view, err)
	}
	select {
	case <-channel.commitReceived:
		t.Fatal("altered transcript reached commit")
	default:
	}
	trusts, err := trust.ListTrusts(context.Background())
	if err != nil || len(trusts) != 0 {
		t.Fatalf("trusts after altered transcript = %#v, %v", trusts, err)
	}
}

func TestCoordinatorPreservesStableFailureCodes(t *testing.T) {
	t.Run("Companion Hub is unavailable before credentials leave the Mac", func(t *testing.T) {
		channel := newScriptedPairingChannel()
		coordinator, _, session := newCoordinatorFixture(t, channel)
		coordinator.hub = func(context.Context) (HubLocator, error) {
			return HubLocator{}, errors.New("Hub advertisement unavailable")
		}
		view, err := coordinator.Confirm(context.Background(), session.Reference, "012345")
		if !errors.Is(err, ErrPairingFailed) || view.State != SessionFailed ||
			view.ErrorCode != "hub_unavailable" {
			t.Fatalf("Confirm(Hub unavailable) = %#v, %v", view, err)
		}
		select {
		case <-channel.credentialsReceived:
			t.Fatal("credentials left the Mac without a registered Device Hub")
		default:
		}
	})

	t.Run("Security2 rejects a wrong code", func(t *testing.T) {
		channel := newScriptedPairingChannel()
		channel.connectError = errSecurity2Proof
		coordinator, _, session := newCoordinatorFixture(t, channel)
		view, err := coordinator.Confirm(context.Background(), session.Reference, "000000")
		if !errors.Is(err, ErrPairingFailed) || view.State != SessionFailed ||
			view.ErrorCode != "authentication_failed" {
			t.Fatalf("Confirm(wrong code) = %#v, %v", view, err)
		}
	})

	t.Run("Deck rejects credential staging", func(t *testing.T) {
		channel := newScriptedPairingChannel()
		channel.credentialErrorCode = "capacity_reached"
		coordinator, trust, session := newCoordinatorFixture(t, channel)
		view, err := coordinator.Confirm(context.Background(), session.Reference, "012345")
		if !errors.Is(err, ErrPairingFailed) || view.State != SessionFailed ||
			view.ErrorCode != "capacity_reached" {
			t.Fatalf("Confirm(capacity) = %#v, %v", view, err)
		}
		trusts, listErr := trust.ListTrusts(context.Background())
		if listErr != nil || len(trusts) != 0 {
			t.Fatalf("trusts after capacity failure = %#v, %v", trusts, listErr)
		}
	})

	t.Run("Deck rejects durable commit", func(t *testing.T) {
		channel := newScriptedPairingChannel()
		channel.commitErrorCode = "storage_failure"
		coordinator, trust, session := newCoordinatorFixture(t, channel)
		type confirmation struct {
			view SessionView
			err  error
		}
		completed := make(chan confirmation, 1)
		go func() {
			view, err := coordinator.Confirm(context.Background(), session.Reference, "012345")
			completed <- confirmation{view: view, err: err}
		}()
		<-channel.credentialsReceived
		waitForCoordinatorState(t, coordinator, session.Reference, SessionProvingLink)
		deviceID, transactionID := channel.proofIdentity()
		if err := coordinator.ObserveProvisionalHeartbeat(deviceID, transactionID); err != nil {
			t.Fatal(err)
		}
		outcome := <-completed
		if !errors.Is(outcome.err, ErrPairingFailed) || outcome.view.State != SessionFailed ||
			outcome.view.ErrorCode != "storage_failure" {
			t.Fatalf("Confirm(storage) = %#v, %v", outcome.view, outcome.err)
		}
		trusts, listErr := trust.ListTrusts(context.Background())
		if listErr != nil || len(trusts) != 0 {
			t.Fatalf("trusts after storage failure = %#v, %v", trusts, listErr)
		}
	})
}

func TestCoordinatorRollsBackTrustWhenDeckReceiptIsInvalid(t *testing.T) {
	channel := newScriptedPairingChannel()
	channel.badReceipt = true
	coordinator, trust, session := newCoordinatorFixture(t, channel)
	completed := make(chan error, 1)
	go func() {
		_, err := coordinator.Confirm(context.Background(), session.Reference, "012345")
		completed <- err
	}()
	<-channel.credentialsReceived
	waitForCoordinatorState(t, coordinator, session.Reference, SessionProvingLink)
	deviceID, transactionID := channel.proofIdentity()
	if err := coordinator.ObserveProvisionalHeartbeat(deviceID, transactionID); err != nil {
		t.Fatal(err)
	}
	if err := <-completed; !errors.Is(err, ErrPairingFailed) {
		t.Fatalf("Confirm(bad receipt) error = %v", err)
	}
	trusts, err := trust.ListTrusts(context.Background())
	if err != nil || len(trusts) != 0 {
		t.Fatalf("trust rollback = %#v, %v", trusts, err)
	}
}

func TestCoordinatorCancellationClearsProvisionalTrust(t *testing.T) {
	channel := newScriptedPairingChannel()
	coordinator, trust, session := newCoordinatorFixture(t, channel)
	completed := make(chan error, 1)
	go func() {
		_, err := coordinator.Confirm(context.Background(), session.Reference, "012345")
		completed <- err
	}()
	<-channel.credentialsReceived
	waitForCoordinatorState(t, coordinator, session.Reference, SessionProvingLink)
	view, err := coordinator.Cancel(session.Reference)
	if err != nil || view.State != SessionCancelled {
		t.Fatalf("Cancel() = %#v, %v", view, err)
	}
	if err = <-completed; !errors.Is(err, ErrPairingFailed) {
		t.Fatalf("cancelled Confirm() error = %v", err)
	}
	deviceID, _ := channel.proofIdentity()
	_, valid, verifyErr := trust.VerifyProvisional(context.Background(), pairing.Authentication{
		DeviceID:        deviceID,
		Token:           base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x41}, 32)),
		DeviceIdentity:  "ZGV2aWNlLWlkZW50aXR5LTE",
		ProtocolVersion: pairing.ProtocolVersion,
	})
	if verifyErr != nil || valid {
		t.Fatalf("provisional trust survived cancellation: %t, %v", valid, verifyErr)
	}
}

func TestCoordinatorStartsConfirmationAsynchronouslyAndCloseDrainsIt(t *testing.T) {
	channel := newScriptedPairingChannel()
	coordinator, trust, session := newCoordinatorFixture(t, channel)
	started, err := coordinator.StartConfirm(session.Reference, "012345")
	if err != nil || started.State != SessionAuthenticating {
		t.Fatalf("StartConfirm() = %#v, %v", started, err)
	}
	select {
	case <-channel.credentialsReceived:
	case <-time.After(time.Second):
		t.Fatal("asynchronous confirmation did not send credentials")
	}
	waitForCoordinatorState(t, coordinator, session.Reference, SessionProvingLink)
	closeContext, cancelClose := context.WithTimeout(context.Background(), time.Second)
	err = coordinator.Close(closeContext)
	cancelClose()
	if err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	view, err := coordinator.Status(session.Reference)
	if err != nil || view.State != SessionFailed {
		t.Fatalf("Status(after Close) = %#v, %v", view, err)
	}
	deviceID, _ := channel.proofIdentity()
	_, valid, verifyErr := trust.VerifyProvisional(context.Background(), pairing.Authentication{
		DeviceID:        deviceID,
		Token:           base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x41}, 32)),
		DeviceIdentity:  "ZGV2aWNlLWlkZW50aXR5LTE",
		ProtocolVersion: pairing.ProtocolVersion,
	})
	if verifyErr != nil || valid {
		t.Fatalf("provisional trust survived Close: %t, %v", valid, verifyErr)
	}
	if _, err = coordinator.Begin("anything"); !errors.Is(err, ErrCoordinatorClosed) {
		t.Fatalf("Begin(after Close) error = %v", err)
	}
}

func waitForCoordinatorState(
	t *testing.T,
	coordinator *Coordinator,
	reference string,
	want SessionState,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		view, err := coordinator.Status(reference)
		if err != nil {
			t.Fatal(err)
		}
		if view.State == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("session did not reach %s", want)
}
