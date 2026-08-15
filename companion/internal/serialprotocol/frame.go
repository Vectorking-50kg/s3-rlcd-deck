package serialprotocol

import (
	"bytes"
	"encoding/binary"
	"errors"
)

const (
	HeaderBytes     = 32
	MaxPayloadBytes = 256
)

var (
	Magic                 = [4]byte{'S', 'R', 'D', '1'}
	ErrMalformedFrame     = errors.New("malformed Serial binary frame")
	ErrUnsupportedChannel = errors.New("unsupported Serial binary channel")
	ErrPayloadTooLarge    = errors.New("Serial binary payload is too large")
)

type Channel uint8

const (
	ChannelTargetRX Channel = 1
	ChannelWebTX    Channel = 2
)

type Frame struct {
	Channel     Channel
	SessionID   uint64
	Sequence    uint64
	MonotonicMS uint64
	Payload     []byte
}

func validChannel(channel Channel) bool {
	return channel == ChannelTargetRX || channel == ChannelWebTX
}

func Encode(frame Frame) ([]byte, error) {
	if !validChannel(frame.Channel) {
		return nil, ErrUnsupportedChannel
	}
	if frame.SessionID == 0 || frame.Sequence == 0 || len(frame.Payload) == 0 {
		return nil, ErrMalformedFrame
	}
	if len(frame.Payload) > MaxPayloadBytes {
		return nil, ErrPayloadTooLarge
	}
	document := make([]byte, HeaderBytes+len(frame.Payload))
	copy(document[:4], Magic[:])
	document[4] = byte(frame.Channel)
	// Byte 5 is reserved flags and remains zero in v1.
	binary.BigEndian.PutUint16(document[6:8], uint16(len(frame.Payload)))
	binary.BigEndian.PutUint64(document[8:16], frame.SessionID)
	binary.BigEndian.PutUint64(document[16:24], frame.Sequence)
	binary.BigEndian.PutUint64(document[24:32], frame.MonotonicMS)
	copy(document[HeaderBytes:], frame.Payload)
	return document, nil
}

func Decode(document []byte) (Frame, error) {
	if len(document) < HeaderBytes || !bytes.Equal(document[:4], Magic[:]) || document[5] != 0 {
		return Frame{}, ErrMalformedFrame
	}
	channel := Channel(document[4])
	if !validChannel(channel) {
		return Frame{}, ErrUnsupportedChannel
	}
	payloadBytes := int(binary.BigEndian.Uint16(document[6:8]))
	if payloadBytes == 0 || payloadBytes > MaxPayloadBytes || len(document) != HeaderBytes+payloadBytes {
		return Frame{}, ErrMalformedFrame
	}
	frame := Frame{
		Channel:     channel,
		SessionID:   binary.BigEndian.Uint64(document[8:16]),
		Sequence:    binary.BigEndian.Uint64(document[16:24]),
		MonotonicMS: binary.BigEndian.Uint64(document[24:32]),
		Payload:     append([]byte(nil), document[HeaderBytes:]...),
	}
	if frame.SessionID == 0 || frame.Sequence == 0 {
		clear(frame.Payload)
		return Frame{}, ErrMalformedFrame
	}
	return frame, nil
}
