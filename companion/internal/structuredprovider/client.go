package structuredprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
)

const maximumSafeUnixMilliseconds = int64(253_402_300_799_999)
const maximumSafeJSONInteger = uint64(9_007_199_254_740_991)

type collectionResult struct {
	provider   aisnapshot.Provider
	diagnostic Diagnostic
}

func collectOnce(ctx context.Context, config normalizedConfig, client httpClient) (collectionResult, error) {
	started := time.Now()
	diagnostic := Diagnostic{ProviderID: config.Definition.ID, AdapterVersion: AdapterVersion}
	request, err := buildRequest(ctx, config)
	if err != nil {
		diagnostic.ErrorCode = diagnosticErrorCode(err)
		return collectionResult{diagnostic: diagnostic}, err
	}
	response, err := client.Do(request)
	clearSensitiveHeaders(request, config.Definition.Request.Headers)
	diagnostic.LatencyMillis = time.Since(started).Milliseconds()
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if errors.Is(err, ErrNetworkPolicy) {
			err = ErrNetworkPolicy
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			err = context.DeadlineExceeded
		} else {
			err = ErrUnavailable
		}
		diagnostic.ErrorCode = diagnosticErrorCode(err)
		return collectionResult{diagnostic: diagnostic}, err
	}
	defer response.Body.Close()
	diagnostic.HTTPStatus = response.StatusCode
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		diagnostic.ErrorCode = diagnosticErrorCode(ErrAuthStale)
		return collectionResult{diagnostic: diagnostic}, ErrAuthStale
	case http.StatusForbidden:
		diagnostic.ErrorCode = diagnosticErrorCode(ErrPermission)
		return collectionResult{diagnostic: diagnostic}, ErrPermission
	default:
		diagnostic.ErrorCode = diagnosticErrorCode(ErrUnavailable)
		return collectionResult{diagnostic: diagnostic}, ErrUnavailable
	}
	if encoding := strings.TrimSpace(response.Header.Get("Content-Encoding")); encoding != "" &&
		!strings.EqualFold(encoding, "identity") {
		diagnostic.ErrorCode = diagnosticErrorCode(ErrSchemaChanged)
		return collectionResult{diagnostic: diagnostic}, ErrSchemaChanged
	}
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" {
		diagnostic.ErrorCode = diagnosticErrorCode(ErrSchemaChanged)
		return collectionResult{diagnostic: diagnostic}, ErrSchemaChanged
	}
	maximum := config.maximumResponse
	if response.ContentLength > maximum {
		diagnostic.ErrorCode = diagnosticErrorCode(ErrResponseTooLarge)
		return collectionResult{diagnostic: diagnostic}, ErrResponseTooLarge
	}
	document, readErr := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if readErr != nil {
		diagnostic.ErrorCode = diagnosticErrorCode(ErrUnavailable)
		return collectionResult{diagnostic: diagnostic}, ErrUnavailable
	}
	if int64(len(document)) > maximum {
		diagnostic.ErrorCode = diagnosticErrorCode(ErrResponseTooLarge)
		return collectionResult{diagnostic: diagnostic}, ErrResponseTooLarge
	}
	var validated json.RawMessage
	if protocol.DecodeStrictDocumentLimit(document, int(maximum), &validated) != nil {
		diagnostic.ErrorCode = diagnosticErrorCode(ErrSchemaChanged)
		return collectionResult{diagnostic: diagnostic}, ErrSchemaChanged
	}
	root, decodeErr := decodeGenericJSON(validated)
	if decodeErr != nil {
		diagnostic.ErrorCode = diagnosticErrorCode(ErrSchemaChanged)
		return collectionResult{diagnostic: diagnostic}, ErrSchemaChanged
	}
	provider, normalizeErr := normalizeStructuredProvider(config, root)
	if normalizeErr != nil {
		diagnostic.ErrorCode = diagnosticErrorCode(normalizeErr)
		return collectionResult{diagnostic: diagnostic}, normalizeErr
	}
	diagnostic.ResponseAccepted = true
	return collectionResult{provider: provider, diagnostic: diagnostic}, nil
}

func buildRequest(ctx context.Context, config normalizedConfig) (*http.Request, error) {
	var body io.Reader
	if len(config.Definition.Request.Body) != 0 {
		body = bytes.NewReader(config.Definition.Request.Body)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		string(config.Definition.Request.Method),
		config.target.String(),
		body,
	)
	if err != nil {
		return nil, ErrInvalidConfig
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	headerBytes := 0
	for _, header := range config.Definition.Request.Headers {
		if config.Secrets == nil {
			return nil, ErrAuthStale
		}
		secret, resolveErr := config.Secrets.Get(ctx, header.SecretReference)
		if resolveErr != nil || len(secret) == 0 || len(secret) > maximumSecretBytes ||
			!validSecret(secret) {
			overwrite(secret)
			return nil, ErrAuthStale
		}
		value := header.Prefix + string(secret)
		overwrite(secret)
		headerBytes += len(header.Name) + len(value)
		if headerBytes > maximumHeaderBytes {
			clearSensitiveHeaders(request, config.Definition.Request.Headers)
			return nil, ErrInvalidConfig
		}
		request.Header.Set(header.Name, value)
	}
	return request, nil
}

func validSecret(value []byte) bool {
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func clearSensitiveHeaders(request *http.Request, headers []Header) {
	for _, header := range headers {
		if header.SecretReference != "" {
			request.Header.Del(header.Name)
		}
	}
}

func overwrite(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func normalizeStructuredProvider(config normalizedConfig, root any) (aisnapshot.Provider, error) {
	mapping := config.mapping
	updatedTime := config.Now().UTC()
	updated := updatedTime.Format(time.RFC3339Nano)
	updatedUnixMS := updatedTime.UnixMilli()
	staleSeconds := uint32(config.Definition.RefreshMinutes) * 3 * 60
	provider := aisnapshot.Provider{
		SchemaVersion:     aisnapshot.SchemaVersion{Major: aisnapshot.SchemaMajor, Minor: aisnapshot.SchemaMinor},
		ID:                config.Definition.ID,
		DisplayName:       config.Definition.DisplayName,
		Status:            aisnapshot.ProviderOK,
		Source:            aisnapshot.ProviderSourceStructuredHTTP,
		Confidence:        aisnapshot.ConfidenceVerified,
		Experimental:      config.Definition.Experimental,
		UpdatedAt:         &updated,
		UpdatedAtUnixMS:   &updatedUnixMS,
		StaleAfterSeconds: &staleSeconds,
		Windows:           []aisnapshot.QuotaWindow{},
	}
	if mapping.balance != nil {
		value, err := mapping.balance.value(root)
		if err != nil {
			return aisnapshot.Provider{}, err
		}
		amount, err := moneyMicros(value, mapping.definition.BalanceDivisor)
		if err != nil {
			return aisnapshot.Provider{}, err
		}
		currency := mapping.definition.FixedCurrency
		if mapping.currency != nil {
			currencyValue, pathErr := mapping.currency.value(root)
			var ok bool
			currency, ok = currencyValue.(string)
			if pathErr != nil || !ok || !validCurrency(currency) {
				return aisnapshot.Provider{}, ErrSchemaChanged
			}
		}
		provider.Balance = &aisnapshot.Money{AmountMicros: amount, Currency: currency}
	}
	if mapping.used != nil {
		usedValue, err := mapping.used.value(root)
		if err != nil {
			return aisnapshot.Provider{}, err
		}
		totalValue, err := mapping.total.value(root)
		if err != nil {
			return aisnapshot.Provider{}, err
		}
		used, err := quotaBasisPoints(usedValue, totalValue)
		if err != nil {
			return aisnapshot.Provider{}, err
		}
		remaining := uint16(10_000 - used)
		window := aisnapshot.QuotaWindow{
			Name:                 mapping.definition.WindowName,
			UsedBasisPoints:      &used,
			RemainingBasisPoints: &remaining,
		}
		if mapping.reset != nil {
			resetValue, pathErr := mapping.reset.value(root)
			reset, resetUnixMS, parseErr := resetTime(resetValue, mapping.definition.ResetFormat)
			if pathErr != nil || parseErr != nil {
				return aisnapshot.Provider{}, ErrSchemaChanged
			}
			window.ResetsAt = &reset
			window.ResetsAtUnixMS = &resetUnixMS
		}
		provider.Windows = append(provider.Windows, window)
	}
	if aisnapshot.ValidateProvider(provider, updatedTime) != nil {
		return aisnapshot.Provider{}, ErrSchemaChanged
	}
	return provider, nil
}

func numericValue(value any) (*big.Rat, error) {
	var source string
	switch typed := value.(type) {
	case json.Number:
		source = typed.String()
	case string:
		source = typed
	default:
		return nil, ErrSchemaChanged
	}
	if source == "" || strings.HasPrefix(source, "+") || strings.Contains(source, "/") {
		return nil, ErrSchemaChanged
	}
	result, ok := new(big.Rat).SetString(source)
	if !ok || result.Sign() < 0 || result.Num().BitLen() > 256 || result.Denom().BitLen() > 256 {
		return nil, ErrSchemaChanged
	}
	return result, nil
}

func moneyMicros(value any, divisor uint64) (uint64, error) {
	amount, err := numericValue(value)
	if err != nil || divisor == 0 {
		return 0, ErrSchemaChanged
	}
	amount.Mul(amount, new(big.Rat).SetInt64(1_000_000))
	amount.Quo(amount, new(big.Rat).SetInt(new(big.Int).SetUint64(divisor)))
	result, err := roundedUint64(amount)
	if err != nil || result > maximumSafeJSONInteger {
		return 0, ErrSchemaChanged
	}
	return result, nil
}

func quotaBasisPoints(usedValue, totalValue any) (uint16, error) {
	used, err := numericValue(usedValue)
	if err != nil {
		return 0, ErrSchemaChanged
	}
	total, err := numericValue(totalValue)
	if err != nil || total.Sign() <= 0 || used.Cmp(total) > 0 {
		return 0, ErrSchemaChanged
	}
	ratio := new(big.Rat).Mul(used, new(big.Rat).SetInt64(10_000))
	ratio.Quo(ratio, total)
	value, err := roundedUint64(ratio)
	if err != nil || value > 10_000 {
		return 0, ErrSchemaChanged
	}
	return uint16(value), nil
}

func roundedUint64(value *big.Rat) (uint64, error) {
	if value.Sign() < 0 {
		return 0, ErrSchemaChanged
	}
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(value.Num(), value.Denom(), remainder)
	remainder.Lsh(remainder, 1)
	if remainder.Cmp(value.Denom()) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsUint64() {
		return 0, ErrSchemaChanged
	}
	return quotient.Uint64(), nil
}

func resetTime(value any, format ResetFormat) (string, int64, error) {
	var parsed time.Time
	switch format {
	case ResetRFC3339:
		text, ok := value.(string)
		var err error
		if !ok {
			return "", 0, ErrSchemaChanged
		}
		parsed, err = time.Parse(time.RFC3339Nano, text)
		if err != nil || parsed.Location() != time.UTC || parsed.Format(time.RFC3339Nano) != text {
			return "", 0, ErrSchemaChanged
		}
	case ResetUnixSeconds, ResetUnixMilliseconds:
		number, err := numericValue(value)
		if err != nil || !number.IsInt() || !number.Num().IsInt64() {
			return "", 0, ErrSchemaChanged
		}
		milliseconds := number.Num().Int64()
		if format == ResetUnixSeconds {
			if milliseconds > maximumSafeUnixMilliseconds/1000 {
				return "", 0, ErrSchemaChanged
			}
			milliseconds *= 1000
		}
		if milliseconds < 0 || milliseconds > maximumSafeUnixMilliseconds {
			return "", 0, ErrSchemaChanged
		}
		parsed = time.UnixMilli(milliseconds).UTC()
	default:
		return "", 0, ErrSchemaChanged
	}
	return parsed.Format(time.RFC3339Nano), parsed.UnixMilli(), nil
}

func diagnosticErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrAuthStale):
		return "auth_stale"
	case errors.Is(err, ErrPermission):
		return "permission_denied"
	case errors.Is(err, ErrSchemaChanged), errors.Is(err, ErrResponseTooLarge):
		return "schema_changed"
	case errors.Is(err, ErrNetworkPolicy):
		return "network_policy"
	default:
		return "unavailable"
	}
}
