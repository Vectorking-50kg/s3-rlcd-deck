package pairingv2

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
)

const (
	ContractVersion        uint32 = 2
	MaximumContractMessage        = 4096
	MaximumCertificateDER         = 1024
)

var (
	ErrMalformedContract  = errors.New("malformed Pairing v2 contract message")
	ErrUnsupportedMajor   = errors.New("unsupported Pairing v2 protocol major")
	ErrUnexpectedSequence = errors.New("unexpected Pairing v2 message sequence")

	hex128Pattern  = regexp.MustCompile(`^[0-9a-f]{32}$`)
	hashPattern    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	tokenPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	devicePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{7,63}$`)
	servicePattern = regexp.MustCompile(
		`^[a-z0-9][a-z0-9-]{0,62}\._s3rlcd-hub\._tcp\.local\.$`,
	)
)

type Message interface {
	pairingV2Message()
}

type Credentials struct {
	Type                   string `json:"type"`
	ProtocolVersion        uint32 `json:"protocol_version"`
	SessionID              string `json:"session_id"`
	TransactionID          string `json:"transaction_id"`
	Sequence               uint32 `json:"sequence"`
	WindowNonce            string `json:"window_nonce"`
	CompanionNonce         string `json:"companion_nonce"`
	HubService             string `json:"hub_service"`
	HubAddress             string `json:"hub_address"`
	Token                  string `json:"token"`
	CertificateFingerprint string `json:"certificate_fingerprint"`
	CertificateDER         string `json:"certificate_der"`
	DeviceLinkProtocol     uint32 `json:"device_link_protocol"`
}

type CommitReady struct {
	Type             string `json:"type"`
	ProtocolVersion  uint32 `json:"protocol_version"`
	SessionID        string `json:"session_id"`
	TransactionID    string `json:"transaction_id"`
	Sequence         uint32 `json:"sequence"`
	WindowNonce      string `json:"window_nonce"`
	CompanionNonce   string `json:"companion_nonce"`
	DeckNonce        string `json:"deck_nonce"`
	DeviceID         string `json:"device_id"`
	DeviceIdentity   string `json:"device_identity"`
	ProfileID        string `json:"profile_id"`
	TranscriptSHA256 string `json:"transcript_sha256"`
}

type Commit struct {
	Type             string `json:"type"`
	ProtocolVersion  uint32 `json:"protocol_version"`
	SessionID        string `json:"session_id"`
	TransactionID    string `json:"transaction_id"`
	Sequence         uint32 `json:"sequence"`
	DeckNonce        string `json:"deck_nonce"`
	TranscriptSHA256 string `json:"transcript_sha256"`
}

type CommitReceipt struct {
	Type              string `json:"type"`
	ProtocolVersion   uint32 `json:"protocol_version"`
	SessionID         string `json:"session_id"`
	TransactionID     string `json:"transaction_id"`
	Sequence          uint32 `json:"sequence"`
	ProfileID         string `json:"profile_id"`
	ProfileGeneration uint32 `json:"profile_generation"`
	TranscriptSHA256  string `json:"transcript_sha256"`
}

type StatusRequest struct {
	Type            string `json:"type"`
	ProtocolVersion uint32 `json:"protocol_version"`
	SessionID       string `json:"session_id"`
	TransactionID   string `json:"transaction_id"`
	Sequence        uint32 `json:"sequence"`
}

type Status struct {
	Type             string `json:"type"`
	ProtocolVersion  uint32 `json:"protocol_version"`
	SessionID        string `json:"session_id"`
	TransactionID    string `json:"transaction_id"`
	Sequence         uint32 `json:"sequence"`
	State            string `json:"state"`
	ErrorCode        string `json:"error_code"`
	TranscriptSHA256 string `json:"transcript_sha256"`
}

type Cancel struct {
	Type            string `json:"type"`
	ProtocolVersion uint32 `json:"protocol_version"`
	SessionID       string `json:"session_id"`
	TransactionID   string `json:"transaction_id"`
	Sequence        uint32 `json:"sequence"`
}

type Error struct {
	Type            string `json:"type"`
	ProtocolVersion uint32 `json:"protocol_version"`
	SessionID       string `json:"session_id"`
	TransactionID   string `json:"transaction_id"`
	Sequence        uint32 `json:"sequence"`
	Code            string `json:"code"`
}

func (Credentials) pairingV2Message()   {}
func (CommitReady) pairingV2Message()   {}
func (Commit) pairingV2Message()        {}
func (CommitReceipt) pairingV2Message() {}
func (StatusRequest) pairingV2Message() {}
func (Status) pairingV2Message()        {}
func (Cancel) pairingV2Message()        {}
func (Error) pairingV2Message()         {}

func DecodeContractMessage(document []byte) (Message, error) {
	var raw map[string]json.RawMessage
	if len(document) == 0 || len(document) > MaximumContractMessage || bytes.ContainsRune(document, '\\') ||
		protocol.DecodeStrictDocumentLimit(document, MaximumContractMessage, &raw) != nil {
		return nil, ErrMalformedContract
	}
	typeRaw, typePresent := raw["type"]
	versionRaw, versionPresent := raw["protocol_version"]
	if !typePresent || !versionPresent {
		return nil, ErrMalformedContract
	}
	var messageType string
	var version uint32
	if json.Unmarshal(typeRaw, &messageType) != nil || json.Unmarshal(versionRaw, &version) != nil {
		return nil, ErrMalformedContract
	}
	if version != ContractVersion {
		return nil, ErrUnsupportedMajor
	}
	var message Message
	switch messageType {
	case "pairing.credentials":
		message = &Credentials{}
	case "pairing.commit_ready":
		message = &CommitReady{}
	case "pairing.commit":
		message = &Commit{}
	case "pairing.commit_receipt":
		message = &CommitReceipt{}
	case "pairing.status_request":
		message = &StatusRequest{}
	case "pairing.status":
		message = &Status{}
	case "pairing.cancel":
		message = &Cancel{}
	case "pairing.error":
		message = &Error{}
	default:
		return nil, ErrMalformedContract
	}
	if protocol.DecodeStrictDocumentLimit(document, MaximumContractMessage, message) != nil {
		return nil, ErrMalformedContract
	}
	if err := validateContractMessage(message); err != nil {
		return nil, err
	}
	return message, nil
}

func validateContractMessage(message Message) error {
	switch value := message.(type) {
	case *Credentials:
		if !commonFieldsValid(value.Type, value.ProtocolVersion, value.SessionID, value.TransactionID) ||
			value.Sequence != 1 || !hex128Pattern.MatchString(value.WindowNonce) ||
			!hex128Pattern.MatchString(value.CompanionNonce) ||
			!servicePattern.MatchString(value.HubService) || !validHubAddress(value.HubAddress) ||
			!tokenPattern.MatchString(value.Token) || value.DeviceLinkProtocol != 1 ||
			!certificateMatches(value.CertificateDER, value.CertificateFingerprint) {
			return ErrMalformedContract
		}
	case *CommitReady:
		if !commonFieldsValid(value.Type, value.ProtocolVersion, value.SessionID, value.TransactionID) ||
			value.Sequence != 2 || !hex128Pattern.MatchString(value.WindowNonce) ||
			!hex128Pattern.MatchString(value.CompanionNonce) || !hex128Pattern.MatchString(value.DeckNonce) ||
			!devicePattern.MatchString(value.DeviceID) || !validDeviceIdentity(value.DeviceIdentity) ||
			!hashPattern.MatchString(value.ProfileID) || !hashPattern.MatchString(value.TranscriptSHA256) {
			return ErrMalformedContract
		}
	case *Commit:
		if !commonFieldsValid(value.Type, value.ProtocolVersion, value.SessionID, value.TransactionID) ||
			value.Sequence != 3 || !hex128Pattern.MatchString(value.DeckNonce) ||
			!hashPattern.MatchString(value.TranscriptSHA256) {
			return ErrMalformedContract
		}
	case *CommitReceipt:
		if !commonFieldsValid(value.Type, value.ProtocolVersion, value.SessionID, value.TransactionID) ||
			value.Sequence != 4 || value.ProfileGeneration == 0 ||
			!hashPattern.MatchString(value.ProfileID) || !hashPattern.MatchString(value.TranscriptSHA256) {
			return ErrMalformedContract
		}
	case *StatusRequest:
		if !commonFieldsValid(value.Type, value.ProtocolVersion, value.SessionID, value.TransactionID) ||
			value.Sequence < 5 {
			return ErrMalformedContract
		}
	case *Status:
		if !commonFieldsValid(value.Type, value.ProtocolVersion, value.SessionID, value.TransactionID) ||
			value.Sequence < 5 || !validPairingState(value.State) || !validErrorCode(value.ErrorCode) ||
			!hashPattern.MatchString(value.TranscriptSHA256) {
			return ErrMalformedContract
		}
	case *Cancel:
		if !commonFieldsValid(value.Type, value.ProtocolVersion, value.SessionID, value.TransactionID) ||
			value.Sequence < 1 {
			return ErrMalformedContract
		}
	case *Error:
		if !commonFieldsValid(value.Type, value.ProtocolVersion, value.SessionID, value.TransactionID) ||
			value.Sequence < 1 || !validErrorCode(value.Code) {
			return ErrMalformedContract
		}
	default:
		return ErrMalformedContract
	}
	return nil
}

// TranscriptSHA256 computes the canonical digest that binds the PAKE session,
// Companion credentials, and Deck identity to one transaction. The returned
// digest must exactly match commit-ready, commit, status, and receipt messages.
func TranscriptSHA256(credentials Credentials, ready CommitReady) (string, error) {
	if validateContractMessage(&credentials) != nil || validateContractMessage(&ready) != nil ||
		credentials.SessionID != ready.SessionID ||
		credentials.TransactionID != ready.TransactionID ||
		credentials.WindowNonce != ready.WindowNonce ||
		credentials.CompanionNonce != ready.CompanionNonce ||
		credentials.CertificateFingerprint != ready.ProfileID {
		return "", ErrMalformedContract
	}
	certificate, err := base64.StdEncoding.Strict().DecodeString(credentials.CertificateDER)
	if err != nil || len(certificate) == 0 || len(certificate) > MaximumCertificateDER {
		clearBytes(certificate)
		return "", ErrMalformedContract
	}
	defer clearBytes(certificate)

	transcript := make([]byte, 0, MaximumContractMessage)
	defer clearBytes(transcript)
	transcript = append(transcript, "s3-rlcd-pairing-v2-transcript"...)
	transcript = append(transcript, 0)
	appendField := func(label string, value []byte) bool {
		if len(value) > int(^uint32(0)) || len(transcript)+len(label)+5+len(value) > MaximumContractMessage {
			return false
		}
		transcript = append(transcript, label...)
		transcript = append(transcript, 0)
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(value)))
		transcript = append(transcript, length[:]...)
		transcript = append(transcript, value...)
		return true
	}
	appendUint32 := func(label string, value uint32) bool {
		var encoded [4]byte
		binary.BigEndian.PutUint32(encoded[:], value)
		return appendField(label, encoded[:])
	}
	if !appendUint32("protocol_version", ContractVersion) ||
		!appendField("session_id", []byte(credentials.SessionID)) ||
		!appendField("transaction_id", []byte(credentials.TransactionID)) ||
		!appendField("window_nonce", []byte(credentials.WindowNonce)) ||
		!appendField("companion_nonce", []byte(credentials.CompanionNonce)) ||
		!appendField("hub_service", []byte(credentials.HubService)) ||
		!appendField("hub_address", []byte(credentials.HubAddress)) ||
		!appendField("token", []byte(credentials.Token)) ||
		!appendField("certificate_fingerprint", []byte(credentials.CertificateFingerprint)) ||
		!appendField("certificate_der", certificate) ||
		!appendUint32("device_link_protocol", credentials.DeviceLinkProtocol) ||
		!appendField("deck_nonce", []byte(ready.DeckNonce)) ||
		!appendField("device_id", []byte(ready.DeviceID)) ||
		!appendField("device_identity", []byte(ready.DeviceIdentity)) ||
		!appendField("profile_id", []byte(ready.ProfileID)) {
		return "", ErrMalformedContract
	}
	digest := sha256.Sum256(transcript)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func commonFieldsValid(messageType string, version uint32, sessionID, transactionID string) bool {
	return strings.HasPrefix(messageType, "pairing.") && version == ContractVersion &&
		hex128Pattern.MatchString(sessionID) && hex128Pattern.MatchString(transactionID)
}

func certificateMatches(encoded, fingerprint string) bool {
	if !hashPattern.MatchString(fingerprint) || len(encoded) == 0 || len(encoded) > 1368 {
		return false
	}
	der, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(der) == 0 || len(der) > MaximumCertificateDER {
		clearBytes(der)
		return false
	}
	digest := sha256.Sum256(der)
	clearBytes(der)
	expected, err := hex.DecodeString(strings.TrimPrefix(fingerprint, "sha256:"))
	if err != nil || len(expected) != sha256.Size {
		clearBytes(expected)
		return false
	}
	matches := subtle.ConstantTimeCompare(digest[:], expected) == 1
	clearBytes(expected)
	return matches
}

func validDeviceIdentity(value string) bool {
	if len(value) < 22 || len(value) > 683 {
		return false
	}
	identity, err := base64.RawURLEncoding.Strict().DecodeString(value)
	valid := err == nil && len(identity) >= 16 && len(identity) <= 512
	clearBytes(identity)
	return valid
}

func validHubAddress(value string) bool {
	host, portText, err := net.SplitHostPort(value)
	if err != nil || host == "" || strings.Contains(host, "%") {
		return false
	}
	address, err := netip.ParseAddr(host)
	port, portErr := strconv.ParseUint(portText, 10, 16)
	return err == nil && portErr == nil && port != 0 &&
		(portText == "0" || portText[0] != '0') && usablePairingIPv4(address)
}

func validPairingState(value string) bool {
	switch value {
	case "staged", "committed", "connecting", "online", "failed", "cancelled", "expired":
		return true
	default:
		return false
	}
}

func validErrorCode(value string) bool {
	switch value {
	case "none", "busy", "expired", "rate_limited", "incompatible_protocol", "malformed",
		"authentication_failed", "storage_failure", "capacity_reached", "link_failed", "cancelled":
		return true
	default:
		return false
	}
}
