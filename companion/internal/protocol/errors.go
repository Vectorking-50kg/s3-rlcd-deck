package protocol

import "errors"

type ErrorCode string

const (
	MalformedEnvelopeCode  ErrorCode = "malformed_envelope"
	MessageTooLargeCode    ErrorCode = "message_too_large"
	UnsupportedVersionCode ErrorCode = "unsupported_protocol_version"
	InternalErrorCode      ErrorCode = "internal_error"
)

func Code(err error) ErrorCode {
	switch {
	case errors.Is(err, ErrMessageTooLarge):
		return MessageTooLargeCode
	case errors.Is(err, ErrUnsupportedVersion):
		return UnsupportedVersionCode
	case errors.Is(err, ErrMalformedEnvelope):
		return MalformedEnvelopeCode
	default:
		return InternalErrorCode
	}
}
