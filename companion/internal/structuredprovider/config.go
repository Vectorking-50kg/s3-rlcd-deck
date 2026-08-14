package structuredprovider

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/secretstore"
)

const (
	maximumHeaders      = 32
	maximumHeaderBytes  = 16 << 10
	maximumRequestBody  = 64 << 10
	maximumSecretBytes  = 16 << 10
	maximumProviderID   = 32
	maximumDisplayRunes = 48
)

var providerIDPattern = regexp.MustCompile("^[a-z][a-z0-9_-]{0,31}$")

type compiledMapping struct {
	balance    compiledPath
	used       compiledPath
	total      compiledPath
	reset      compiledPath
	currency   compiledPath
	definition Mapping
}

type normalizedConfig struct {
	Config
	target          *url.URL
	mapping         compiledMapping
	warning         string
	refreshInterval time.Duration
	requestTimeout  time.Duration
	maximumResponse int64
}

func normalizeConfig(config Config) (normalizedConfig, error) {
	definition := config.Definition
	if !providerIDPattern.MatchString(definition.ID) || len(definition.ID) > maximumProviderID ||
		definition.ID == "codex" || definition.ID == "cursor" ||
		!validDisplayText(definition.DisplayName, maximumDisplayRunes) {
		return normalizedConfig{}, ErrInvalidConfig
	}
	if definition.Request.Method == "" {
		definition.Request.Method = MethodGET
	}
	if definition.Request.Method != MethodGET && definition.Request.Method != MethodPOST {
		return normalizedConfig{}, ErrInvalidConfig
	}
	target, err := url.Parse(definition.Request.URL)
	if err != nil || len(definition.Request.URL) > 2048 || target.Host == "" ||
		target.User != nil || target.Fragment != "" ||
		(target.Scheme != "https" && target.Scheme != "http") || !validTargetHost(target.Hostname()) ||
		target.RawQuery != "" {
		return normalizedConfig{}, ErrInvalidConfig
	}
	if literal := net.ParseIP(target.Hostname()); literal != nil && !allowedTargetIP(literal, target.Scheme) {
		return normalizedConfig{}, ErrNetworkPolicy
	}
	if target.Path == "" {
		target.Path = "/"
	}
	if definition.Request.Method == MethodGET && len(definition.Request.Body) != 0 {
		return normalizedConfig{}, ErrInvalidConfig
	}
	if len(definition.Request.Body) > maximumRequestBody {
		return normalizedConfig{}, ErrInvalidConfig
	}
	if len(definition.Request.Body) != 0 {
		var value any
		if protocol.DecodeStrictDocumentLimit(
			definition.Request.Body,
			maximumRequestBody,
			&value,
		) != nil {
			return normalizedConfig{}, ErrInvalidConfig
		}
		value, err = decodeGenericJSON(definition.Request.Body)
		if err != nil || containsSensitiveBodyField(value) {
			return normalizedConfig{}, ErrInvalidConfig
		}
		definition.Request.Body = append(json.RawMessage(nil), definition.Request.Body...)
	}
	headers, err := normalizeHeaders(definition.Request.Headers)
	if err != nil {
		return normalizedConfig{}, err
	}
	definition.Request.Headers = headers
	if definition.RequestTimeoutSeconds == 0 {
		definition.RequestTimeoutSeconds = uint16(DefaultRequestTimeout / time.Second)
	}
	if definition.RequestTimeoutSeconds > 30 {
		return normalizedConfig{}, ErrInvalidConfig
	}
	if !allowedRefresh(definition.RefreshMinutes) {
		return normalizedConfig{}, ErrInvalidConfig
	}
	if definition.MaximumResponseBytes <= 0 {
		definition.MaximumResponseBytes = DefaultMaximumResponse
	}
	if definition.MaximumResponseBytes > DefaultMaximumResponse {
		return normalizedConfig{}, ErrInvalidConfig
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	config.Definition = definition
	mapping, err := compileMapping(definition.Mapping)
	if err != nil {
		return normalizedConfig{}, err
	}
	if !validDefinitionProvider(definition, mapping) {
		return normalizedConfig{}, ErrInvalidConfig
	}
	warning := ""
	if target.Scheme == "http" {
		warning = "private-network HTTP is not encrypted"
	}
	requestTimeout := time.Duration(definition.RequestTimeoutSeconds) * time.Second
	if config.requestTimeout > 0 {
		requestTimeout = config.requestTimeout
	}
	return normalizedConfig{
		Config: config, target: target, mapping: mapping, warning: warning,
		refreshInterval: time.Duration(definition.RefreshMinutes) * time.Minute,
		requestTimeout:  requestTimeout, maximumResponse: definition.MaximumResponseBytes,
	}, nil
}

func validTargetHost(host string) bool {
	if host == "" || len(host) > 253 || strings.Contains(host, "%") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
				character >= '0' && character <= '9' || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func containsSensitiveBodyField(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for name, child := range typed {
			if sensitiveFieldName(name) {
				return true
			}
			if containsSensitiveBodyField(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsSensitiveBodyField(child) {
				return true
			}
		}
	}
	return false
}

func sensitiveFieldName(name string) bool {
	normalized := strings.Map(func(character rune) rune {
		if character >= 'A' && character <= 'Z' {
			return character + ('a' - 'A')
		}
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			return character
		}
		return -1
	}, name)
	for _, marker := range []string{
		"token", "secret", "password", "passwd", "passphrase", "credential", "auth", "apikey",
		"privatekey", "subscriptionkey", "accesskey", "cookie", "prompt", "message",
		"bearer", "signature", "hmac", "jwt", "sessionkey",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return normalized == "key" || normalized == "pwd"
}

func validDefinitionProvider(definition Definition, mapping compiledMapping) bool {
	staleSeconds := uint32(definition.RefreshMinutes) * 3 * 60
	provider := aisnapshot.Provider{
		SchemaVersion:     aisnapshot.SchemaVersion{Major: aisnapshot.SchemaMajor, Minor: aisnapshot.SchemaMinor},
		ID:                definition.ID,
		DisplayName:       definition.DisplayName,
		Status:            aisnapshot.ProviderOK,
		Source:            aisnapshot.ProviderSourceStructuredHTTP,
		Confidence:        aisnapshot.ConfidenceVerified,
		Experimental:      definition.Experimental,
		StaleAfterSeconds: &staleSeconds,
		Windows:           []aisnapshot.QuotaWindow{},
	}
	if mapping.balance != nil {
		currency := mapping.definition.FixedCurrency
		if currency == "" {
			currency = "USD"
		}
		provider.Balance = &aisnapshot.Money{Currency: currency}
	}
	if mapping.used != nil {
		provider.Windows = append(provider.Windows, aisnapshot.QuotaWindow{Name: mapping.definition.WindowName})
	}
	return aisnapshot.ValidateProvider(
		provider,
		time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC),
	) == nil
}

func normalizeHeaders(source []Header) ([]Header, error) {
	if len(source) > maximumHeaders {
		return nil, ErrInvalidConfig
	}
	seen := make(map[string]struct{}, len(source))
	result := make([]Header, len(source))
	total := 0
	for index, header := range source {
		name := http.CanonicalHeaderKey(strings.TrimSpace(header.Name))
		lower := strings.ToLower(name)
		if name == "" || !validHeaderName(name) || forbiddenHeader(lower) ||
			!allowedSecretPrefix(name, header.Prefix) || !validReference(header.SecretReference) {
			return nil, ErrInvalidConfig
		}
		if _, duplicate := seen[lower]; duplicate {
			return nil, ErrInvalidConfig
		}
		seen[lower] = struct{}{}
		total += len(name) + len(header.Prefix) + len(header.SecretReference)
		if total > maximumHeaderBytes {
			return nil, ErrInvalidConfig
		}
		header.Name = name
		result[index] = header
	}
	return result, nil
}

func normalizeDraftHeaders(source []Header) ([]Header, error) {
	draft := append([]Header(nil), source...)
	placeholder := secretstore.Reference("secret-00000000000000000000000000000000")
	for index := range draft {
		if draft[index].SecretReference != "" {
			return nil, ErrInvalidConfig
		}
		draft[index].SecretReference = placeholder
	}
	normalized, err := normalizeHeaders(draft)
	if err != nil {
		return nil, err
	}
	for index := range normalized {
		if normalized[index].SecretReference == placeholder {
			normalized[index].SecretReference = ""
		}
	}
	return normalized, nil
}

// NormalizeBackupDefinition validates and canonicalizes a decrypted Provider
// draft without permitting any persisted Secret Reference. Backup owns the
// secret bytes and binds them to fresh references only during commit.
func NormalizeBackupDefinition(definition Definition) (Definition, error) {
	draft := cloneDefinition(definition)
	headers, err := normalizeDraftHeaders(draft.Request.Headers)
	if err != nil {
		return Definition{}, err
	}
	draft.Request.Headers = headers
	placeholder := secretstore.Reference("secret-00000000000000000000000000000000")
	for index := range draft.Request.Headers {
		draft.Request.Headers[index].SecretReference = placeholder
	}
	normalized, err := normalizeConfig(Config{Definition: draft})
	if err != nil {
		return Definition{}, err
	}
	result := cloneDefinition(normalized.Definition)
	for index := range result.Request.Headers {
		result.Request.Headers[index].SecretReference = ""
	}
	return result, nil
}

func allowedSecretPrefix(name, prefix string) bool {
	if prefix == "" {
		return true
	}
	if !strings.EqualFold(name, "Authorization") {
		return false
	}
	switch prefix {
	case "Bearer ", "Basic ", "Token ":
		return true
	default:
		return false
	}
}

func validHeaderName(value string) bool {
	for _, character := range value {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			strings.ContainsRune("!#$%&'*+-.^_|~", character) {
			continue
		}
		return false
	}
	return true
}

func forbiddenHeader(lower string) bool {
	switch lower {
	case "host", "connection", "content-length", "transfer-encoding", "trailer",
		"upgrade", "proxy-authorization", "proxy-authenticate", "proxy-connection",
		"keep-alive", "te", "accept", "accept-encoding", "content-type", "cookie":
		return true
	default:
		return strings.HasPrefix(lower, "sec-")
	}
}

func validHeaderText(value string) bool {
	if !utf8.ValidString(value) || len(value) > maximumSecretBytes {
		return false
	}
	return !strings.ContainsRune(value, 13) && !strings.ContainsRune(value, 10) &&
		!strings.ContainsRune(value, 0)
}

func validReference(value secretstore.Reference) bool {
	_, err := secretstore.ParseReference(value.String())
	return err == nil
}

func allowedRefresh(value uint16) bool {
	switch value {
	case 1, 5, 15, 30, 60:
		return true
	default:
		return false
	}
}

func compileMapping(definition Mapping) (compiledMapping, error) {
	if definition.BalanceDivisor == 0 {
		definition.BalanceDivisor = 1
	}
	if definition.WindowName == "" {
		definition.WindowName = "account"
	}
	if !validDisplayText(definition.WindowName, 24) ||
		(definition.BalancePath == "" && definition.UsedPath == "") ||
		(definition.UsedPath == "") != (definition.TotalPath == "") ||
		(definition.BalancePath != "" && definition.FixedCurrency == "" && definition.CurrencyPath == "") ||
		(definition.FixedCurrency != "" && definition.CurrencyPath != "") ||
		definition.FixedCurrency != "" && !validCurrency(definition.FixedCurrency) ||
		definition.ResetPath != "" && definition.ResetFormat == "" ||
		definition.ResetPath == "" && definition.ResetFormat != "" {
		return compiledMapping{}, ErrInvalidConfig
	}
	if definition.ResetFormat != "" && definition.ResetFormat != ResetRFC3339 &&
		definition.ResetFormat != ResetUnixSeconds && definition.ResetFormat != ResetUnixMilliseconds {
		return compiledMapping{}, ErrInvalidConfig
	}
	paths := []*string{
		&definition.BalancePath,
		&definition.UsedPath,
		&definition.TotalPath,
		&definition.ResetPath,
		&definition.CurrencyPath,
	}
	compiled := make([]compiledPath, len(paths))
	for index, source := range paths {
		var err error
		compiled[index], err = compilePath(*source)
		if err != nil {
			return compiledMapping{}, fmt.Errorf("%w: invalid JSONPath", ErrInvalidConfig)
		}
	}
	return compiledMapping{
		balance: compiled[0], used: compiled[1], total: compiled[2],
		reset: compiled[3], currency: compiled[4], definition: definition,
	}, nil
}

func validCurrency(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, character := range value {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return true
}
