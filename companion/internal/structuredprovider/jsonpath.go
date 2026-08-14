package structuredprovider

import (
	"bytes"
	"encoding/json"
	"strconv"
	"unicode"
	"unicode/utf8"
)

const (
	maximumJSONPathBytes = 256
	maximumJSONPathSteps = 16
	maximumJSONNodes     = 8192
	maximumJSONDepth     = 64
)

type pathStep struct {
	key   string
	index int
	array bool
}

type compiledPath []pathStep

func compilePath(source string) (compiledPath, error) {
	if source == "" {
		return nil, nil
	}
	if len(source) > maximumJSONPathBytes || source[0] != '$' || !utf8.ValidString(source) {
		return nil, ErrInvalidConfig
	}
	steps := make(compiledPath, 0, 4)
	for index := 1; index < len(source); {
		if len(steps) >= maximumJSONPathSteps {
			return nil, ErrInvalidConfig
		}
		switch source[index] {
		case '.':
			index++
			start := index
			for index < len(source) && pathIdentifierByte(source[index]) {
				index++
			}
			if start == index || index-start > 64 {
				return nil, ErrInvalidConfig
			}
			steps = append(steps, pathStep{key: source[start:index]})
		case '[':
			index++
			if index >= len(source) {
				return nil, ErrInvalidConfig
			}
			if source[index] == '\'' || source[index] == '"' {
				quote := source[index]
				index++
				start := index
				for index < len(source) && source[index] != quote {
					if source[index] == '\\' || source[index] < 0x20 {
						return nil, ErrInvalidConfig
					}
					index++
				}
				if index >= len(source) || index == start || index-start > 64 {
					return nil, ErrInvalidConfig
				}
				key := source[start:index]
				index++
				if index >= len(source) || source[index] != ']' {
					return nil, ErrInvalidConfig
				}
				index++
				steps = append(steps, pathStep{key: key})
				continue
			}
			start := index
			for index < len(source) && source[index] >= '0' && source[index] <= '9' {
				index++
			}
			if start == index || index >= len(source) || source[index] != ']' {
				return nil, ErrInvalidConfig
			}
			value, err := strconv.ParseUint(source[start:index], 10, 16)
			if err != nil || value > 1023 {
				return nil, ErrInvalidConfig
			}
			index++
			steps = append(steps, pathStep{array: true, index: int(value)})
		default:
			return nil, ErrInvalidConfig
		}
	}
	if len(steps) == 0 {
		return nil, ErrInvalidConfig
	}
	return steps, nil
}

func pathIdentifierByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' || value == '_' || value == '-'
}

func (path compiledPath) value(root any) (any, error) {
	current := root
	for _, step := range path {
		if step.array {
			values, ok := current.([]any)
			if !ok || step.index >= len(values) {
				return nil, ErrSchemaChanged
			}
			current = values[step.index]
			continue
		}
		object, ok := current.(map[string]any)
		if !ok {
			return nil, ErrSchemaChanged
		}
		var exists bool
		current, exists = object[step.key]
		if !exists || current == nil {
			return nil, ErrSchemaChanged
		}
	}
	return current, nil
}

func decodeGenericJSON(document []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, ErrSchemaChanged
	}
	nodes := 0
	if !boundedJSONValue(value, 0, &nodes) {
		return nil, ErrSchemaChanged
	}
	return value, nil
}

func boundedJSONValue(value any, depth int, nodes *int) bool {
	*nodes++
	if *nodes > maximumJSONNodes || depth > maximumJSONDepth {
		return false
	}
	switch typed := value.(type) {
	case map[string]any:
		for _, child := range typed {
			if !boundedJSONValue(child, depth+1, nodes) {
				return false
			}
		}
	case []any:
		for _, child := range typed {
			if !boundedJSONValue(child, depth+1, nodes) {
				return false
			}
		}
	}
	return true
}

func validDisplayText(value string, maximum int) bool {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maximum {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
