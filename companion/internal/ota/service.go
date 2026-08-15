// Package ota owns signed firmware preview receipts and the exact-result
// Device Link transaction. Firmware bytes are volatile and are cleared after
// success, failure, expiry, or shutdown.
package ota

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"regexp"
	"sync"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/devicelink"
)

const (
	MaximumImageBytes         = 1_740_800
	MaximumArchiveBytes       = 3 << 20
	chunkBytes                = 3_072
	defaultReceiptTTL         = 5 * time.Minute
	defaultResultTimeout      = 10 * time.Second
	defaultTransactionTimeout = 10 * time.Minute
	maximumReceipts           = 8
	maximumStatuses           = 64
)

var (
	ErrInvalidArchive = errors.New("invalid signed firmware archive")
	ErrInvalidReceipt = errors.New("invalid or expired OTA preview receipt")
	ErrBusy           = errors.New("an OTA transaction is already active")
	ErrInvalidResult  = errors.New("invalid OTA device result")
	versionPattern    = regexp.MustCompile(`^[0-9][0-9A-Za-z.+-]{0,30}$`)
	deviceIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{7,63}$`)
)

type Manifest struct {
	Version                string `json:"version"`
	Board                  string `json:"board"`
	ImageLength            uint32 `json:"image_length"`
	ImageSHA256            string `json:"image_sha256"`
	Signature              []byte `json:"signature"`
	SigningKeyID           uint32 `json:"signing_key_id"`
	MinimumProtocolVersion uint32 `json:"minimum_protocol_version"`
}

type Archive struct {
	BundleVersion int      `json:"bundle_version"`
	Manifest      Manifest `json:"manifest"`
	Image         []byte   `json:"image"`
}

type Preview struct {
	Receipt                string `json:"receipt"`
	Version                string `json:"version"`
	Board                  string `json:"board"`
	ImageLength            uint32 `json:"image_length"`
	ImageSHA256            string `json:"image_sha256"`
	SigningKeyID           uint32 `json:"signing_key_id"`
	MinimumProtocolVersion uint32 `json:"minimum_protocol_version"`
}

type State string

const (
	StateOffering      State = "offering"
	StateReceiving     State = "receiving"
	StateReadyToReboot State = "ready_to_reboot"
	StateFailed        State = "failed"
)

type Status struct {
	DeviceID      string `json:"device_id"`
	State         State  `json:"state"`
	Code          string `json:"code"`
	Version       string `json:"version"`
	ReceivedBytes uint32 `json:"received_bytes"`
	ImageLength   uint32 `json:"image_length"`
	UpdatedAt     string `json:"updated_at"`
}

type Sender interface {
	SendOTAOffer(context.Context, string, devicelink.OTAOffer) error
	SendOTAChunk(context.Context, string, devicelink.OTAChunk) error
}

type Config struct {
	Sender             Sender
	Keys               map[uint32]*ecdsa.PublicKey
	Now                func() time.Time
	ReceiptTTL         time.Duration
	ResultTimeout      time.Duration
	TransactionTimeout time.Duration
}

type receiptEntry struct {
	archive   Archive
	expiresAt time.Time
}

type transaction struct {
	id      string
	results chan devicelink.OTAResult
	archive Archive
}

type Service struct {
	sender             Sender
	keys               map[uint32]*ecdsa.PublicKey
	now                func() time.Time
	receiptTTL         time.Duration
	resultTimeout      time.Duration
	transactionTimeout time.Duration
	ctx                context.Context
	cancel             context.CancelFunc
	closeOnce          sync.Once
	wg                 sync.WaitGroup

	mu          sync.Mutex
	closed      bool
	receipts    map[string]receiptEntry
	active      map[string]*transaction
	statuses    map[string]Status
	statusOrder []string
}

func New(config Config) (*Service, error) {
	if config.Sender == nil {
		return nil, errors.New("OTA Device Link sender is required")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.ReceiptTTL == 0 {
		config.ReceiptTTL = defaultReceiptTTL
	}
	if config.ResultTimeout == 0 {
		config.ResultTimeout = defaultResultTimeout
	}
	if config.TransactionTimeout == 0 {
		config.TransactionTimeout = defaultTransactionTimeout
	}
	if config.ReceiptTTL <= 0 || config.ResultTimeout <= 0 || config.TransactionTimeout <= 0 {
		return nil, errors.New("OTA timing must be positive")
	}
	if len(config.Keys) == 0 {
		config.Keys = productionKeys()
	}
	keys := make(map[uint32]*ecdsa.PublicKey, len(config.Keys))
	for id, key := range config.Keys {
		if id == 0 || key == nil || key.Curve != elliptic.P256() ||
			!key.Curve.IsOnCurve(key.X, key.Y) {
			return nil, errors.New("invalid OTA signing key")
		}
		keys[id] = &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).Set(key.X), Y: new(big.Int).Set(key.Y)}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		sender: config.Sender, keys: keys, now: config.Now,
		receiptTTL: config.ReceiptTTL, resultTimeout: config.ResultTimeout,
		transactionTimeout: config.TransactionTimeout,
		ctx:                ctx, cancel: cancel, receipts: make(map[string]receiptEntry),
		active: make(map[string]*transaction), statuses: make(map[string]Status),
	}, nil
}

func productionKeys() map[uint32]*ecdsa.PublicKey {
	encoded, _ := hex.DecodeString("04656b37c0adb4d5c2a971fc07fad9bcdd679d30eeb2bff140a01d88f292fa2bb578d0f3320488c576d9990bb69dc9b05547e1752c61de8d0b84e4a8fc04ddb137")
	x, y := elliptic.Unmarshal(elliptic.P256(), encoded)
	clear(encoded)
	return map[uint32]*ecdsa.PublicKey{1: {Curve: elliptic.P256(), X: x, Y: y}}
}

func CanonicalManifest(manifest Manifest) ([]byte, error) {
	if !validManifestMetadata(manifest) {
		return nil, ErrInvalidArchive
	}
	digest, err := hex.DecodeString(manifest.ImageSHA256)
	if err != nil || len(digest) != sha256.Size {
		clear(digest)
		return nil, ErrInvalidArchive
	}
	document := make([]byte, 134)
	copy(document, []byte("S3RLCDOTA1"))
	binary.BigEndian.PutUint32(document[10:14], manifest.SigningKeyID)
	binary.BigEndian.PutUint32(document[14:18], manifest.MinimumProtocolVersion)
	binary.BigEndian.PutUint32(document[18:22], manifest.ImageLength)
	copy(document[22:70], manifest.Board)
	copy(document[70:102], manifest.Version)
	copy(document[102:134], digest)
	clear(digest)
	return document, nil
}

func validManifestFields(manifest Manifest) bool {
	return validManifestMetadata(manifest) && len(manifest.Signature) == 64
}

func validManifestMetadata(manifest Manifest) bool {
	digest, digestErr := hex.DecodeString(manifest.ImageSHA256)
	clear(digest)
	return versionPattern.MatchString(manifest.Version) &&
		manifest.Board == devicelink.BoardESP32S3RLCD42 &&
		manifest.ImageLength > 0 && manifest.ImageLength <= MaximumImageBytes &&
		len(manifest.ImageSHA256) == sha256.Size*2 && digestErr == nil &&
		manifest.SigningKeyID != 0 &&
		manifest.MinimumProtocolVersion != 0
}

func (service *Service) Preview(document []byte) (Preview, error) {
	if len(document) == 0 || len(document) > MaximumArchiveBytes {
		return Preview{}, ErrInvalidArchive
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var archive Archive
	if err := decoder.Decode(&archive); err != nil {
		destroyArchive(&archive)
		return Preview{}, ErrInvalidArchive
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF || archive.BundleVersion != 1 ||
		!validManifestFields(archive.Manifest) ||
		len(archive.Image) != int(archive.Manifest.ImageLength) {
		destroyArchive(&archive)
		return Preview{}, ErrInvalidArchive
	}
	expectedDigest, err := hex.DecodeString(archive.Manifest.ImageSHA256)
	actualDigest := sha256.Sum256(archive.Image)
	if err != nil || subtle.ConstantTimeCompare(expectedDigest, actualDigest[:]) != 1 {
		clear(expectedDigest)
		destroyArchive(&archive)
		return Preview{}, ErrInvalidArchive
	}
	clear(expectedDigest)
	canonical, err := CanonicalManifest(archive.Manifest)
	key := service.keys[archive.Manifest.SigningKeyID]
	hash := sha256.Sum256(canonical)
	clear(canonical)
	r := new(big.Int).SetBytes(archive.Manifest.Signature[:32])
	s := new(big.Int).SetBytes(archive.Manifest.Signature[32:])
	if err != nil || key == nil || !ecdsa.Verify(key, hash[:], r, s) {
		destroyArchive(&archive)
		return Preview{}, ErrInvalidArchive
	}
	receiptBytes := make([]byte, 32)
	if _, err = rand.Read(receiptBytes); err != nil {
		clear(receiptBytes)
		destroyArchive(&archive)
		return Preview{}, errors.New("OTA preview receipt unavailable")
	}
	receipt := base64.RawURLEncoding.EncodeToString(receiptBytes)
	clear(receiptBytes)
	now := service.now().UTC()
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed {
		destroyArchive(&archive)
		return Preview{}, errors.New("OTA service is closed")
	}
	service.pruneReceiptsLocked(now)
	if len(service.receipts) >= maximumReceipts {
		destroyArchive(&archive)
		return Preview{}, errors.New("OTA preview capacity reached")
	}
	service.receipts[receipt] = receiptEntry{archive: archive, expiresAt: now.Add(service.receiptTTL)}
	return Preview{
		Receipt: receipt, Version: archive.Manifest.Version, Board: archive.Manifest.Board,
		ImageLength: archive.Manifest.ImageLength, ImageSHA256: archive.Manifest.ImageSHA256,
		SigningKeyID:           archive.Manifest.SigningKeyID,
		MinimumProtocolVersion: archive.Manifest.MinimumProtocolVersion,
	}, nil
}

func (service *Service) Apply(receipt, deviceID string) error {
	if receipt == "" || !deviceIDPattern.MatchString(deviceID) {
		return ErrInvalidReceipt
	}
	now := service.now().UTC()
	service.mu.Lock()
	service.pruneReceiptsLocked(now)
	entry, exists := service.receipts[receipt]
	if service.closed || !exists {
		service.mu.Unlock()
		return ErrInvalidReceipt
	}
	if len(service.active) != 0 {
		service.mu.Unlock()
		return ErrBusy
	}
	delete(service.receipts, receipt)
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		service.receipts[receipt] = entry
		service.mu.Unlock()
		return errors.New("OTA transaction ID unavailable")
	}
	id := hex.EncodeToString(idBytes)
	clear(idBytes)
	transaction := &transaction{id: id, results: make(chan devicelink.OTAResult, 1), archive: entry.archive}
	service.active[deviceID] = transaction
	service.rememberStatusDeviceLocked(deviceID)
	service.statuses[deviceID] = Status{
		DeviceID: deviceID, State: StateOffering, Code: "pending",
		Version: entry.archive.Manifest.Version, ImageLength: entry.archive.Manifest.ImageLength,
		UpdatedAt: now.Format(time.RFC3339Nano),
	}
	service.wg.Add(1)
	service.mu.Unlock()
	go service.run(deviceID, transaction)
	return nil
}

func (service *Service) rememberStatusDeviceLocked(deviceID string) {
	for index, existing := range service.statusOrder {
		if existing != deviceID {
			continue
		}
		copy(service.statusOrder[index:], service.statusOrder[index+1:])
		service.statusOrder = service.statusOrder[:len(service.statusOrder)-1]
		break
	}
	service.statusOrder = append(service.statusOrder, deviceID)
	for len(service.statusOrder) > maximumStatuses {
		oldest := service.statusOrder[0]
		service.statusOrder = service.statusOrder[1:]
		delete(service.statuses, oldest)
	}
}

func (service *Service) run(deviceID string, transaction *transaction) {
	defer service.wg.Done()
	defer func() {
		service.mu.Lock()
		if service.active[deviceID] == transaction {
			delete(service.active, deviceID)
		}
		service.mu.Unlock()
		destroyArchive(&transaction.archive)
	}()
	transactionContext, cancel := context.WithTimeout(service.ctx, service.transactionTimeout)
	defer cancel()
	manifest := transaction.archive.Manifest
	offer := devicelink.OTAOffer{
		TransactionID: transaction.id, Version: manifest.Version, Board: manifest.Board,
		ImageLength: manifest.ImageLength, ImageSHA256: manifest.ImageSHA256,
		Signature:              base64.StdEncoding.EncodeToString(manifest.Signature),
		SigningKeyID:           manifest.SigningKeyID,
		MinimumProtocolVersion: manifest.MinimumProtocolVersion,
	}
	if err := service.sendOffer(transactionContext, deviceID, offer); err != nil {
		service.fail(deviceID, transportFailureCode(transactionContext))
		return
	}
	result, ok := service.waitResult(transactionContext, transaction)
	if !ok || result.State != "receiving" || result.Code != "ok" ||
		result.ReceivedBytes != 0 || result.ImageLength != manifest.ImageLength {
		service.fail(deviceID, resultCode(result, ok))
		return
	}
	service.update(deviceID, StateReceiving, "ok", 0)
	for offset := 0; offset < len(transaction.archive.Image); offset += chunkBytes {
		end := min(offset+chunkBytes, len(transaction.archive.Image))
		final := end == len(transaction.archive.Image)
		chunk := devicelink.OTAChunk{
			TransactionID: transaction.id, Offset: uint32(offset), Final: final,
			Data: base64.StdEncoding.EncodeToString(transaction.archive.Image[offset:end]),
		}
		if err := service.sendChunk(transactionContext, deviceID, chunk); err != nil {
			service.fail(deviceID, transportFailureCode(transactionContext))
			return
		}
		result, ok = service.waitResult(transactionContext, transaction)
		expectedState := "receiving"
		if final {
			expectedState = "ready_to_reboot"
		}
		if !ok || result.Code != "ok" || result.State != expectedState ||
			result.ReceivedBytes != uint32(end) || result.ImageLength != manifest.ImageLength {
			service.fail(deviceID, resultCode(result, ok))
			return
		}
		if final {
			service.update(deviceID, StateReadyToReboot, "ok", uint32(end))
		} else {
			service.update(deviceID, StateReceiving, "ok", uint32(end))
		}
	}
}

func (service *Service) sendOffer(parent context.Context, deviceID string, offer devicelink.OTAOffer) error {
	ctx, cancel := context.WithTimeout(parent, service.resultTimeout)
	defer cancel()
	return service.sender.SendOTAOffer(ctx, deviceID, offer)
}

func (service *Service) sendChunk(parent context.Context, deviceID string, chunk devicelink.OTAChunk) error {
	ctx, cancel := context.WithTimeout(parent, service.resultTimeout)
	defer cancel()
	return service.sender.SendOTAChunk(ctx, deviceID, chunk)
}

func (service *Service) waitResult(ctx context.Context, transaction *transaction) (devicelink.OTAResult, bool) {
	timer := time.NewTimer(service.resultTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return devicelink.OTAResult{}, false
	case <-timer.C:
		return devicelink.OTAResult{}, false
	case result := <-transaction.results:
		return result, true
	}
}

func transportFailureCode(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "transaction_timeout"
	}
	return "transport_unavailable"
}

func resultCode(result devicelink.OTAResult, available bool) string {
	if !available {
		return "result_timeout"
	}
	if result.Code == "ok" {
		return "invalid_result"
	}
	return result.Code
}

func (service *Service) HandleResult(deviceID string, result devicelink.OTAResult) error {
	service.mu.Lock()
	transaction := service.active[deviceID]
	if transaction == nil || transaction.id != result.TransactionID {
		service.mu.Unlock()
		return ErrInvalidResult
	}
	service.mu.Unlock()
	select {
	case transaction.results <- result:
		return nil
	default:
		return ErrInvalidResult
	}
}

func (service *Service) Status(deviceID string) Status {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.statuses[deviceID]
}

func (service *Service) update(deviceID string, state State, code string, received uint32) {
	service.mu.Lock()
	status := service.statuses[deviceID]
	status.State = state
	status.Code = code
	status.ReceivedBytes = received
	status.UpdatedAt = service.now().UTC().Format(time.RFC3339Nano)
	service.statuses[deviceID] = status
	service.mu.Unlock()
}

func (service *Service) fail(deviceID, code string) {
	service.update(deviceID, StateFailed, code, service.Status(deviceID).ReceivedBytes)
}

func (service *Service) pruneReceiptsLocked(now time.Time) {
	for receipt, entry := range service.receipts {
		if !now.Before(entry.expiresAt) {
			destroyArchive(&entry.archive)
			delete(service.receipts, receipt)
		}
	}
}

func (service *Service) Close() {
	service.closeOnce.Do(func() {
		service.mu.Lock()
		service.closed = true
		for receipt, entry := range service.receipts {
			destroyArchive(&entry.archive)
			delete(service.receipts, receipt)
		}
		service.mu.Unlock()
		service.cancel()
		service.wg.Wait()
		service.mu.Lock()
		clear(service.statuses)
		service.statuses = nil
		clear(service.statusOrder)
		service.statusOrder = nil
		service.mu.Unlock()
	})
}

// CloseContext cancels transport work and waits for volatile firmware bytes to
// be cleared. A timeout leaves the Service owner intact so shutdown can be
// retried without losing ownership of the worker or archive buffers.
func (service *Service) CloseContext(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		service.Close()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func destroyArchive(archive *Archive) {
	if archive != nil {
		clear(archive.Image)
		archive.Image = nil
		clear(archive.Manifest.Signature)
		archive.Manifest.Signature = nil
	}
}

func decodeChunk(encoded string, output []byte) (int, error) {
	return base64.StdEncoding.Decode(output, []byte(encoded))
}
