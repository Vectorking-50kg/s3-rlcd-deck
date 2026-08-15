package ota

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/devicelink"
)

type fakeSender struct {
	mu      sync.Mutex
	service *Service
	image   []byte
	offer   devicelink.OTAOffer
}

type silentSender struct{}

type blockedSender struct{}

func (*silentSender) SendOTAOffer(context.Context, string, devicelink.OTAOffer) error { return nil }
func (*silentSender) SendOTAChunk(context.Context, string, devicelink.OTAChunk) error { return nil }
func (*blockedSender) SendOTAOffer(ctx context.Context, _ string, _ devicelink.OTAOffer) error {
	<-ctx.Done()
	return ctx.Err()
}
func (*blockedSender) SendOTAChunk(ctx context.Context, _ string, _ devicelink.OTAChunk) error {
	<-ctx.Done()
	return ctx.Err()
}

func (sender *fakeSender) SendOTAOffer(_ context.Context, deviceID string, offer devicelink.OTAOffer) error {
	sender.mu.Lock()
	sender.offer = offer
	sender.mu.Unlock()
	go func() {
		_ = sender.service.HandleResult(deviceID, devicelink.OTAResult{
			Type: devicelink.MessageOTAResult, ProtocolVersion: 1,
			TransactionID: offer.TransactionID, State: "receiving", Code: "ok",
			ImageLength: offer.ImageLength,
		})
	}()
	return nil
}

func (sender *fakeSender) SendOTAChunk(_ context.Context, deviceID string, chunk devicelink.OTAChunk) error {
	data := make([]byte, len(chunk.Data))
	decoded, err := decodeChunk(chunk.Data, data)
	if err != nil {
		return err
	}
	sender.mu.Lock()
	sender.image = append(sender.image, data[:decoded]...)
	offer := sender.offer
	received := uint32(len(sender.image))
	sender.mu.Unlock()
	state := "receiving"
	if chunk.Final {
		state = "ready_to_reboot"
	}
	go func() {
		_ = sender.service.HandleResult(deviceID, devicelink.OTAResult{
			Type: devicelink.MessageOTAResult, ProtocolVersion: 1,
			TransactionID: chunk.TransactionID, State: state, Code: "ok",
			ReceivedBytes: received, ImageLength: offer.ImageLength,
		})
	}()
	return nil
}

func signedArchive(t *testing.T, key *ecdsa.PrivateKey, version, board string, image []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(image)
	manifest := Manifest{
		Version: version, Board: board, ImageLength: uint32(len(image)),
		ImageSHA256: hex.EncodeToString(digest[:]), SigningKeyID: 7,
		MinimumProtocolVersion: 1,
	}
	canonical, err := CanonicalManifest(manifest)
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
	archive, err := json.Marshal(Archive{BundleVersion: 1, Manifest: manifest, Image: image})
	if err != nil {
		t.Fatal(err)
	}
	return archive
}

func TestPreviewRequiresExplicitApplyBeforeStreamingSignedImage(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sender := &fakeSender{}
	service, err := New(Config{
		Sender: sender, Keys: map[uint32]*ecdsa.PublicKey{7: &key.PublicKey},
		ResultTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	sender.service = service
	defer service.Close()
	image := make([]byte, 7_001)
	for index := range image {
		image[index] = byte(index)
	}
	preview, err := service.Preview(signedArchive(t, key, "0.3.0", devicelink.BoardESP32S3RLCD42, image))
	if err != nil {
		t.Fatal(err)
	}
	if preview.Receipt == "" || preview.Version != "0.3.0" || preview.ImageLength != uint32(len(image)) {
		t.Fatalf("preview = %#v", preview)
	}
	if len(sender.image) != 0 || sender.offer.TransactionID != "" {
		t.Fatal("preview streamed firmware without explicit confirmation")
	}
	if err = service.Apply(preview.Receipt, "deck-a1b2c3d4"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for service.Status("deck-a1b2c3d4").State != StateReadyToReboot && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	status := service.Status("deck-a1b2c3d4")
	if status.State != StateReadyToReboot || status.ReceivedBytes != uint32(len(image)) {
		t.Fatalf("status = %#v", status)
	}
	if string(sender.image) != string(image) {
		t.Fatal("streamed image differs from signed archive")
	}
	if err = service.Apply(preview.Receipt, "deck-a1b2c3d4"); err != ErrInvalidReceipt {
		t.Fatalf("second Apply error = %v", err)
	}
}

func TestPreviewRejectsWrongSignatureBoardLengthAndUnknownFields(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	service, err := New(Config{
		Sender: &fakeSender{}, Keys: map[uint32]*ecdsa.PublicKey{7: &key.PublicKey},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	valid := signedArchive(t, key, "0.3.0", devicelink.BoardESP32S3RLCD42, []byte("image"))
	var archive map[string]any
	_ = json.Unmarshal(valid, &archive)
	manifest := archive["manifest"].(map[string]any)
	manifest["board"] = "wrong-board"
	wrongBoard, _ := json.Marshal(archive)
	if _, err = service.Preview(wrongBoard); err == nil {
		t.Fatal("wrong board was accepted")
	}
	_ = json.Unmarshal(valid, &archive)
	manifest = archive["manifest"].(map[string]any)
	manifest["image_length"] = float64(999)
	wrongLength, _ := json.Marshal(archive)
	if _, err = service.Preview(wrongLength); err == nil {
		t.Fatal("wrong length was accepted")
	}
	_ = json.Unmarshal(valid, &archive)
	manifest = archive["manifest"].(map[string]any)
	manifest["signature"] = make([]byte, 64)
	wrongSignature, _ := json.Marshal(archive)
	if _, err = service.Preview(wrongSignature); err == nil {
		t.Fatal("wrong signature was accepted")
	}
	_ = json.Unmarshal(valid, &archive)
	archive["extra"] = true
	unknown, _ := json.Marshal(archive)
	if _, err = service.Preview(unknown); err == nil {
		t.Fatal("unknown archive field was accepted")
	}
}

func TestCanonicalManifestUsesFixedWidthBigEndianContract(t *testing.T) {
	manifest := Manifest{
		Version: "1.2.3", Board: devicelink.BoardESP32S3RLCD42,
		ImageLength: 0x00010203, ImageSHA256: string(make([]byte, 64)),
		SigningKeyID: 7, MinimumProtocolVersion: 1,
	}
	for index := range manifest.ImageSHA256 {
		manifest.ImageSHA256 = manifest.ImageSHA256[:index] + "0" + manifest.ImageSHA256[index+1:]
	}
	document, err := CanonicalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(document) != 134 || string(document[:10]) != "S3RLCDOTA1" ||
		new(big.Int).SetBytes(document[10:14]).Uint64() != 7 ||
		new(big.Int).SetBytes(document[18:22]).Uint64() != 0x00010203 {
		t.Fatalf("canonical manifest = %x", document)
	}
}

func TestMissingOrMismatchedDeckResultFailsExactTransaction(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	service, err := New(Config{
		Sender: &silentSender{}, Keys: map[uint32]*ecdsa.PublicKey{7: &key.PublicKey},
		ResultTimeout: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	preview, err := service.Preview(signedArchive(
		t, key, "0.3.0", devicelink.BoardESP32S3RLCD42, []byte("image"),
	))
	if err != nil || service.Apply(preview.Receipt, "deck-a1b2c3d4") != nil {
		t.Fatalf("start interrupted transaction: %v", err)
	}
	if err = service.HandleResult("deck-a1b2c3d4", devicelink.OTAResult{
		TransactionID: "00112233445566778899aabbccddeeff",
	}); !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("mismatched result error = %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for service.Status("deck-a1b2c3d4").State != StateFailed && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	status := service.Status("deck-a1b2c3d4")
	if status.State != StateFailed || status.Code != "result_timeout" || status.ReceivedBytes != 0 {
		t.Fatalf("interrupted status = %#v", status)
	}
}

func TestTransactionHasAnIndependentTotalDeadline(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	service, err := New(Config{
		Sender: &blockedSender{}, Keys: map[uint32]*ecdsa.PublicKey{7: &key.PublicKey},
		ResultTimeout: time.Second, TransactionTimeout: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	preview, err := service.Preview(signedArchive(
		t, key, "0.3.0", devicelink.BoardESP32S3RLCD42, []byte("image"),
	))
	if err != nil || service.Apply(preview.Receipt, "deck-a1b2c3d4") != nil {
		t.Fatalf("start bounded transaction: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for service.Status("deck-a1b2c3d4").State != StateFailed && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	status := service.Status("deck-a1b2c3d4")
	if status.State != StateFailed || status.Code != "transaction_timeout" {
		t.Fatalf("bounded status = %#v", status)
	}
}

func TestProductionKeyMatchesAuthoritativeCatalog(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate OTA test source")
	}
	document, err := os.ReadFile(filepath.Join(
		filepath.Dir(source), "..", "..", "..", "protocol", "catalog", "ota-signing-keys-v1.json",
	))
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		SchemaVersion int `json:"schema_version"`
		Keys          []struct {
			ID     uint32 `json:"id"`
			Public string `json:"public_key_sec1_hex"`
			Status string `json:"status"`
		} `json:"keys"`
	}
	if err = json.Unmarshal(document, &catalog); err != nil || catalog.SchemaVersion != 1 || len(catalog.Keys) != 1 {
		t.Fatalf("decode signing catalog: %v", err)
	}
	key := productionKeys()[catalog.Keys[0].ID]
	if key == nil {
		t.Fatal("catalog key ID is absent from Go production keys")
	}
	encoded := elliptic.Marshal(elliptic.P256(), key.X, key.Y)
	defer clear(encoded)
	if catalog.Keys[0].Status != "active" || hex.EncodeToString(encoded) != catalog.Keys[0].Public {
		t.Fatal("Go production key drifted from protocol catalog")
	}
}
