package pairingv2

import (
	"errors"
	"fmt"
)

const (
	wireVarint = 0
	wireBytes  = 2
)

type protoField struct {
	number uint64
	wire   uint64
	value  []byte
	varint uint64
}

func appendProtoVarint(output []byte, value uint64) []byte {
	for value >= 0x80 {
		output = append(output, byte(value)|0x80)
		value >>= 7
	}
	return append(output, byte(value))
}

func appendProtoBytes(output []byte, number uint64, value []byte) []byte {
	output = appendProtoVarint(output, number<<3|wireBytes)
	output = appendProtoVarint(output, uint64(len(value)))
	return append(output, value...)
}

func appendProtoEnum(output []byte, number uint64, value uint64) []byte {
	output = appendProtoVarint(output, number<<3|wireVarint)
	return appendProtoVarint(output, value)
}

func parseProtoFields(document []byte, maximumLength int) ([]protoField, error) {
	if len(document) == 0 || len(document) > maximumLength {
		return nil, errors.New("invalid protobuf document length")
	}
	fields := make([]protoField, 0, 4)
	seen := make(map[uint64]struct{}, 4)
	for offset := 0; offset < len(document); {
		key, next, err := readProtoVarint(document, offset)
		if err != nil || key == 0 {
			return nil, errors.New("invalid protobuf field key")
		}
		offset = next
		field := protoField{number: key >> 3, wire: key & 7}
		if _, duplicate := seen[field.number]; duplicate {
			return nil, fmt.Errorf("duplicate protobuf field %d", field.number)
		}
		seen[field.number] = struct{}{}
		switch field.wire {
		case wireVarint:
			field.varint, offset, err = readProtoVarint(document, offset)
			if err != nil {
				return nil, errors.New("invalid protobuf varint")
			}
		case wireBytes:
			var length uint64
			length, offset, err = readProtoVarint(document, offset)
			if err != nil || length > uint64(len(document)-offset) {
				return nil, errors.New("invalid protobuf byte field")
			}
			end := offset + int(length)
			field.value = document[offset:end]
			offset = end
		default:
			return nil, fmt.Errorf("unsupported protobuf wire type %d", field.wire)
		}
		fields = append(fields, field)
	}
	return fields, nil
}

func readProtoVarint(document []byte, offset int) (uint64, int, error) {
	var value uint64
	for shift := uint(0); shift < 64 && offset < len(document); shift += 7 {
		current := document[offset]
		offset++
		if shift == 63 && current > 1 {
			return 0, offset, errors.New("protobuf varint overflow")
		}
		value |= uint64(current&0x7f) << shift
		if current&0x80 == 0 {
			return value, offset, nil
		}
	}
	return 0, offset, errors.New("unterminated protobuf varint")
}

func requireProtoField(fields []protoField, number, wire uint64) (protoField, error) {
	for _, field := range fields {
		if field.number == number {
			if field.wire != wire {
				return protoField{}, fmt.Errorf("protobuf field %d has wrong wire type", number)
			}
			return field, nil
		}
	}
	return protoField{}, fmt.Errorf("missing protobuf field %d", number)
}

func rejectUnknownProtoFields(fields []protoField, allowed ...uint64) error {
	for _, field := range fields {
		known := false
		for _, number := range allowed {
			known = known || field.number == number
		}
		if !known {
			return fmt.Errorf("unknown protobuf field %d", field.number)
		}
	}
	return nil
}
