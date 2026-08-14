package structuredprovider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
)

const maximumCurlImportBytes = 64 << 10

// ImportCurl parses a deliberately small curl subset. It never starts a
// process, expands a variable, reads a file, follows a redirect, or interprets
// a shell operator.
func ImportCurl(source string) (CurlImport, error) {
	if len(source) == 0 || len(source) > maximumCurlImportBytes || !utf8.ValidString(source) {
		return CurlImport{}, ErrInvalidCurl
	}
	tokens, err := lexCurl(source)
	if err != nil || len(tokens) < 2 || tokens[0] != "curl" {
		return CurlImport{}, ErrInvalidCurl
	}
	request := Request{Method: MethodGET}
	var data string
	var encodedHeaders []string
	for index := 1; index < len(tokens); index++ {
		token := tokens[index]
		option, inline, hasInline := strings.Cut(token, "=")
		nextValue := func() (string, bool) {
			if hasInline {
				return inline, inline != ""
			}
			index++
			if index >= len(tokens) {
				return "", false
			}
			return tokens[index], true
		}
		switch option {
		case "-s", "-S", "-sS", "--silent", "--show-error":
			if hasInline {
				return CurlImport{}, ErrInvalidCurl
			}
		case "-X", "--request":
			value, ok := nextValue()
			if !ok {
				return CurlImport{}, ErrInvalidCurl
			}
			request.Method = Method(strings.ToUpper(value))
		case "-H", "--header":
			value, ok := nextValue()
			if !ok {
				return CurlImport{}, ErrInvalidCurl
			}
			encodedHeaders = append(encodedHeaders, value)
		case "-d", "--data", "--data-raw", "--data-binary":
			value, ok := nextValue()
			if !ok || data != "" || strings.HasPrefix(value, "@") {
				return CurlImport{}, ErrInvalidCurl
			}
			data = value
		case "--url":
			value, ok := nextValue()
			if !ok || request.URL != "" {
				return CurlImport{}, ErrInvalidCurl
			}
			request.URL = value
		default:
			if strings.HasPrefix(token, "-") || hasInline || request.URL != "" {
				return CurlImport{}, ErrInvalidCurl
			}
			request.URL = token
		}
	}
	if request.URL == "" || request.Method != MethodGET && request.Method != MethodPOST {
		return CurlImport{}, ErrInvalidCurl
	}
	target, parseErr := url.Parse(request.URL)
	if parseErr != nil || target.Host == "" || target.User != nil || target.Fragment != "" ||
		(target.Scheme != "https" && target.Scheme != "http") || !validTargetHost(target.Hostname()) ||
		target.RawQuery != "" {
		return CurlImport{}, ErrInvalidCurl
	}
	if data != "" {
		if request.Method == MethodGET {
			request.Method = MethodPOST
		}
		var value any
		if protocol.DecodeStrictDocumentLimit([]byte(data), maximumRequestBody, &value) != nil {
			return CurlImport{}, ErrInvalidCurl
		}
		value, err = decodeGenericJSON([]byte(data))
		if err != nil || containsSensitiveBodyField(value) {
			return CurlImport{}, ErrInvalidCurl
		}
		request.Body = append(json.RawMessage(nil), data...)
	}
	imported := CurlImport{Request: request}
	completed := false
	defer func() {
		if !completed {
			for _, secret := range imported.Secrets {
				overwrite(secret.Value)
			}
		}
	}()
	parsedHeaders := make([]Header, 0, len(encodedHeaders))
	for _, encoded := range encodedHeaders {
		name, value, ok := strings.Cut(encoded, ":")
		name = http.CanonicalHeaderKey(strings.TrimSpace(name))
		value = strings.TrimSpace(value)
		if !ok || name == "" || value == "" || !validHeaderName(name) || !validHeaderText(value) {
			return CurlImport{}, ErrInvalidCurl
		}
		lower := strings.ToLower(name)
		if lower == "accept" || lower == "content-type" {
			if !strings.EqualFold(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]), "application/json") {
				return CurlImport{}, ErrInvalidCurl
			}
			continue
		}
		if forbiddenHeader(lower) {
			return CurlImport{}, ErrInvalidCurl
		}
		header := Header{Name: name}
		reference := fmt.Sprintf("imported_header_%d", len(imported.Secrets)+1)
		prefix, secret := splitSecretPrefix(name, value)
		if secret == "" {
			return CurlImport{}, ErrInvalidCurl
		}
		header.SecretReference = reference
		header.Prefix = prefix
		imported.Secrets = append(imported.Secrets, ImportedSecret{
			Reference: reference,
			Value:     append([]byte(nil), secret...),
		})
		parsedHeaders = append(parsedHeaders, header)
	}
	imported.Request.Headers = parsedHeaders
	if _, err = normalizeHeaders(imported.Request.Headers); err != nil {
		return CurlImport{}, ErrInvalidCurl
	}
	completed = true
	return imported, nil
}

func splitSecretPrefix(name, value string) (string, string) {
	if strings.EqualFold(name, "Authorization") {
		if separator := strings.IndexByte(value, ' '); separator > 0 && separator+1 < len(value) {
			prefix := value[:separator+1]
			if allowedSecretPrefix(name, prefix) {
				return prefix, value[separator+1:]
			}
		}
	}
	return "", value
}

func lexCurl(source string) ([]string, error) {
	var tokens []string
	var current strings.Builder
	quote := rune(0)
	escaped := false
	flush := func() {
		if current.Len() != 0 {
			tokens = append(tokens, current.String())
			current.Reset()
		}
	}
	for _, character := range source {
		if escaped {
			escaped = false
			if character == '\n' {
				continue
			}
			current.WriteRune(character)
			continue
		}
		if character == '$' || character == '`' || character == 0 || character == '\r' {
			return nil, ErrInvalidCurl
		}
		if quote == '\'' {
			if character == '\'' {
				quote = 0
			} else {
				current.WriteRune(character)
			}
			continue
		}
		if quote == '"' {
			switch character {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			default:
				current.WriteRune(character)
			}
			continue
		}
		switch character {
		case '\'', '"':
			quote = character
		case '\\':
			escaped = true
		case ';', '|', '&', '<', '>', '#':
			return nil, ErrInvalidCurl
		case '\n':
			return nil, ErrInvalidCurl
		default:
			if unicode.IsSpace(character) {
				flush()
			} else {
				current.WriteRune(character)
			}
		}
	}
	if escaped || quote != 0 {
		return nil, ErrInvalidCurl
	}
	flush()
	return tokens, nil
}
