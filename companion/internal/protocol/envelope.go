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
