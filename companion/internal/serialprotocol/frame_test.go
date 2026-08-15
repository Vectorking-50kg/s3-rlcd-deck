package serialprotocol

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	frame := Frame{
		Channel:     ChannelTargetRX,
		SessionID:   42,
		Sequence:    7,
		MonotonicMS: 1234,
		Payload:     []byte{0x00, 0xff, 'A'},
	}
	document, err := Encode(frame)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if len(document) != HeaderBytes+len(frame.Payload) {
		t.Fatalf("encoded length = %d", len(document))
	}
	decoded, err := Decode(document)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if decoded.Channel != frame.Channel || decoded.SessionID != frame.SessionID ||
		decoded.Sequence != frame.Sequence || decoded.MonotonicMS != frame.MonotonicMS ||
		hex.EncodeToString(decoded.Payload) != hex.EncodeToString(frame.Payload) {
		t.Fatalf("decoded frame = %#v", decoded)
	}
	document[len(document)-1] = 'Z'
	if decoded.Payload[len(decoded.Payload)-1] != 'A' {
		t.Fatal("Decode() did not own its payload")
	}
}

func TestFrameRejectsMalformedDocuments(t *testing.T) {
	valid, err := Encode(Frame{Channel: ChannelTargetRX, SessionID: 1, Sequence: 1, Payload: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		mutate   func([]byte) []byte
		expected error
	}{
		{"short header", func(document []byte) []byte { return document[:HeaderBytes-1] }, ErrMalformedFrame},
		{"bad magic", func(document []byte) []byte { document[0] = 0; return document }, ErrMalformedFrame},
		{"unknown channel", func(document []byte) []byte { document[4] = 3; return document }, ErrUnsupportedChannel},
		{"nonzero flags", func(document []byte) []byte { document[5] = 1; return document }, ErrMalformedFrame},
		{"zero session", func(document []byte) []byte { clear(document[8:16]); return document }, ErrMalformedFrame},
		{"zero sequence", func(document []byte) []byte { clear(document[16:24]); return document }, ErrMalformedFrame},
		{"wrong length", func(document []byte) []byte { document[7] = 2; return document }, ErrMalformedFrame},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := append([]byte(nil), valid...)
			_, decodeErr := Decode(test.mutate(document))
			if !errors.Is(decodeErr, test.expected) {
				t.Fatalf("Decode() error = %v, want %v", decodeErr, test.expected)
			}
		})
	}
	tooLarge := Frame{Channel: ChannelTargetRX, SessionID: 1, Sequence: 1, Payload: make([]byte, MaxPayloadBytes+1)}
	if _, err = Encode(tooLarge); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("Encode(oversize) error = %v", err)
	}
}

func TestConstantsMatchSharedCatalog(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	document, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "..", "protocol", "catalog", "serial-frame-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		MagicHex    string `json:"magic_hex"`
		HeaderBytes int    `json:"header_bytes"`
		MaxPayload  int    `json:"max_payload_bytes"`
		ByteOrder   string `json:"byte_order"`
		Channels    struct {
			TargetRX uint8 `json:"target_rx"`
			WebTX    uint8 `json:"web_tx"`
		} `json:"channels"`
	}
	if err = json.Unmarshal(document, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.MagicHex != hex.EncodeToString(Magic[:]) || catalog.HeaderBytes != HeaderBytes ||
		catalog.MaxPayload != MaxPayloadBytes || catalog.ByteOrder != "big" ||
		catalog.Channels.TargetRX != uint8(ChannelTargetRX) || catalog.Channels.WebTX != uint8(ChannelWebTX) {
		t.Fatalf("Go constants drifted from shared catalog: %#v", catalog)
	}
}

func TestSharedBinaryFixtures(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	directory := filepath.Join(filepath.Dir(filename), "..", "..", "..", "protocol", "fixtures", "serial-frame-v1")
	var manifest struct {
		Cases []struct {
			File     string `json:"file"`
			Accepted bool   `json:"accepted"`
		} `json:"cases"`
	}
	document, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(document, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, testCase := range manifest.Cases {
		t.Run(testCase.File, func(t *testing.T) {
			hexDocument, readErr := os.ReadFile(filepath.Join(directory, testCase.File))
			if readErr != nil {
				t.Fatal(readErr)
			}
			binaryDocument, decodeHexErr := hex.DecodeString(string(bytes.TrimSpace(hexDocument)))
			if decodeHexErr != nil {
				t.Fatal(decodeHexErr)
			}
			_, decodeErr := Decode(binaryDocument)
			if (decodeErr == nil) != testCase.Accepted {
				t.Fatalf("Decode() error = %v, accepted = %t", decodeErr, testCase.Accepted)
			}
		})
	}
}
