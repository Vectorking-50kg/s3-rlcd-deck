package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"unicode/utf8"
)

const (
	CurrentVersion         uint32 = 1
	MaxControlMessageBytes        = 16 * 1024
)

var (
	ErrMalformedEnvelope  = errors.New("malformed protocol envelope")
	ErrMessageTooLarge    = errors.New("protocol message too large")
	ErrUnsupportedVersion = errors.New("unsupported protocol version")
)

var messageTypePattern = regexp.MustCompile(
	`^[a-z][a-z0-9_]{0,31}(\.[a-z][a-z0-9_]{0,31})+$`,
)

type Envelope struct {
	Type            string          `json:"type"`
	ProtocolVersion uint32          `json:"protocol_version"`
	Message         json.RawMessage `json:"-"`
}

func ParseEnvelope(message []byte) (Envelope, error) {
	if len(message) > MaxControlMessageBytes {
		return Envelope{}, ErrMessageTooLarge
	}
	if !utf8.Valid(message) {
		return Envelope{}, ErrMalformedEnvelope
	}
	if err := validateJSONDocument(message); err != nil {
		return Envelope{}, errors.Join(ErrMalformedEnvelope, err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(message, &document); err != nil {
		return Envelope{}, errors.Join(ErrMalformedEnvelope, err)
	}
	typeValue, typePresent := document["type"]
	versionValue, versionPresent := document["protocol_version"]
	if !typePresent || !versionPresent || bytes.Equal(bytes.TrimSpace(versionValue), []byte("null")) {
		return Envelope{}, ErrMalformedEnvelope
	}
	var envelope Envelope
	if err := json.Unmarshal(typeValue, &envelope.Type); err != nil {
		return Envelope{}, errors.Join(ErrMalformedEnvelope, err)
	}
	if err := json.Unmarshal(versionValue, &envelope.ProtocolVersion); err != nil {
		return Envelope{}, errors.Join(ErrMalformedEnvelope, err)
	}
	if len(envelope.Type) > 64 || !messageTypePattern.MatchString(envelope.Type) {
		return Envelope{}, ErrMalformedEnvelope
	}
	if envelope.ProtocolVersion != CurrentVersion {
		return Envelope{}, ErrUnsupportedVersion
	}
	envelope.Message = append(json.RawMessage(nil), message...)
	return envelope, nil
}

func DecodeStrictDocument(message []byte, destination any) error {
	return DecodeStrictDocumentLimit(message, MaxControlMessageBytes, destination)
}

func DecodeStrictDocumentLimit(message []byte, maximumBytes int, destination any) error {
	if maximumBytes <= 0 || len(message) > maximumBytes || !utf8.Valid(message) {
		return ErrMalformedEnvelope
	}
	if err := validateJSONDocument(message); err != nil {
		return errors.Join(ErrMalformedEnvelope, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(message))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.Join(ErrMalformedEnvelope, err)
	}
	return nil
}

func validateJSONDocument(message []byte) error {
	if !validJSONStringEscapes(message) {
		return ErrMalformedEnvelope
	}
	decoder := json.NewDecoder(bytes.NewReader(message))
	decoder.UseNumber()
	if err := validateJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrMalformedEnvelope
	}
	return nil
}

// encoding/json replaces unpaired UTF-16 surrogate escapes with U+FFFD.
// Reject them at the wire boundary so every implementation sees the same
// Unicode document instead of silently repairing malformed input.
func validJSONStringEscapes(message []byte) bool {
	inString := false
	for index := 0; index < len(message); index++ {
		character := message[index]
		if !inString {
			if character == '"' {
				inString = true
			}
			continue
		}
		if character == '"' {
			inString = false
			continue
		}
		if character != '\\' {
			continue
		}
		index++
		if index >= len(message) {
			return false
		}
		if message[index] != 'u' {
			continue
		}
		codepoint, ok := jsonHexQuad(message, index+1)
		if !ok {
			return false
		}
		index += 4
		if codepoint >= 0xdc00 && codepoint <= 0xdfff {
			return false
		}
		if codepoint < 0xd800 || codepoint > 0xdbff {
			continue
		}
		if index+6 >= len(message) || message[index+1] != '\\' || message[index+2] != 'u' {
			return false
		}
		low, lowOK := jsonHexQuad(message, index+3)
		if !lowOK || low < 0xdc00 || low > 0xdfff {
			return false
		}
		index += 6
	}
	return !inString
}

func jsonHexQuad(message []byte, start int) (uint16, bool) {
	if start+4 > len(message) {
		return 0, false
	}
	var value uint16
	for _, character := range message[start : start+4] {
		value <<= 4
		switch {
		case character >= '0' && character <= '9':
			value |= uint16(character - '0')
		case character >= 'a' && character <= 'f':
			value |= uint16(character-'a') + 10
		case character >= 'A' && character <= 'F':
			value |= uint16(character-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func validateJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return keyErr
			}
			key, ok := keyToken.(string)
			if !ok {
				return ErrMalformedEnvelope
			}
			if _, duplicate := seen[key]; duplicate {
				return ErrMalformedEnvelope
			}
			seen[key] = struct{}{}
			if valueErr := validateJSONValue(decoder); valueErr != nil {
				return valueErr
			}
		}
		closing, closingErr := decoder.Token()
		if closingErr != nil || closing != json.Delim('}') {
			return ErrMalformedEnvelope
		}
		return nil
	case '[':
		for decoder.More() {
			if valueErr := validateJSONValue(decoder); valueErr != nil {
				return valueErr
			}
		}
		closing, closingErr := decoder.Token()
		if closingErr != nil || closing != json.Delim(']') {
			return ErrMalformedEnvelope
		}
		return nil
	default:
		return ErrMalformedEnvelope
	}
}
