package runtime

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/devicelink"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/ota"
)

type otaHTTPSender struct {
	mu     sync.Mutex
	offers int
}

func (sender *otaHTTPSender) SendOTAOffer(context.Context, string, devicelink.OTAOffer) error {
	sender.mu.Lock()
	sender.offers++
	sender.mu.Unlock()
	return nil
}

func (*otaHTTPSender) SendOTAChunk(context.Context, string, devicelink.OTAChunk) error {
	return nil
}

func signedHTTPArchive(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	image := []byte("signed-firmware-image")
	digest := sha256.Sum256(image)
	manifest := ota.Manifest{
		Version: "0.3.0-dev", Board: devicelink.BoardESP32S3RLCD42,
		ImageLength: uint32(len(image)), ImageSHA256: hex.EncodeToString(digest[:]),
		SigningKeyID: 7, MinimumProtocolVersion: 1,
	}
	canonical, err := ota.CanonicalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(canonical)
	r, s, err := ecdsa.Sign(rand.Reader, key, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	manifest.Signature = make([]byte, 64)
	r.FillBytes(manifest.Signature[:32])
	s.FillBytes(manifest.Signature[32:])
	document, err := json.Marshal(ota.Archive{BundleVersion: 1, Manifest: manifest, Image: image})
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func TestOTAHTTPRequiresPreviewAndExplicitConfirmation(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sender := &otaHTTPSender{}
	service, err := ota.New(ota.Config{
		Sender: sender, Keys: map[uint32]*ecdsa.PublicKey{7: &key.PublicKey},
		ResultTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	application := &Runtime{ota: service}

	previewRequest := httptest.NewRequest(http.MethodPost, "/api/v1/ota/preview", bytes.NewReader(signedHTTPArchive(t, key)))
	previewRequest.Header.Set("Content-Type", "application/vnd.s3deck.ota+json")
	previewResponse := httptest.NewRecorder()
	application.handleOTAPreview(previewResponse, previewRequest)
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview = %d %q", previewResponse.Code, previewResponse.Body.String())
	}
	var preview ota.Preview
	if err = json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil || preview.Receipt == "" {
		t.Fatalf("decode preview: %v", err)
	}

	apply := func(confirm bool) *httptest.ResponseRecorder {
		body, _ := json.Marshal(otaApplyRequest{
			Receipt: preview.Receipt, DeviceID: "deck-a1b2c3d4", Confirm: confirm,
		})
		request := httptest.NewRequest(http.MethodPost, "/api/v1/ota/apply", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		application.handleOTAApply(response, request)
		return response
	}
	if response := apply(false); response.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed apply = %d", response.Code)
	}
	sender.mu.Lock()
	if sender.offers != 0 {
		t.Fatal("unconfirmed apply sent an OTA offer")
	}
	sender.mu.Unlock()
	if response := apply(true); response.Code != http.StatusAccepted {
		t.Fatalf("confirmed apply = %d %q", response.Code, response.Body.String())
	}
	deadline := time.Now().Add(time.Second)
	for {
		sender.mu.Lock()
		offers := sender.offers
		sender.mu.Unlock()
		if offers == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("confirmed apply did not send exactly one OTA offer")
		}
		time.Sleep(time.Millisecond)
	}
}
