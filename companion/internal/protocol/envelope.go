package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
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
	if err := validateJSONDocument(message); err != nil {
		return Envelope{}, errors.Join(ErrMalformedEnvelope, err)
	}
	var envelope Envelope
	if err := json.Unmarshal(message, &envelope); err != nil {
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
