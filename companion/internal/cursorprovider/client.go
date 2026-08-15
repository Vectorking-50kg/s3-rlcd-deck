package cursorprovider

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
)

const (
	privateUsageEndpointV1 = "https://api2.cursor.sh/aiserver.v1.DashboardService/GetCurrentPeriodUsage"
	defaultMaximumResponse = 64 << 10
	defaultRequestTimeout  = 5 * time.Second
	defaultRefreshInterval = 5 * time.Minute
	defaultRetryInterval   = 30 * time.Second
	defaultStaleAfter      = 15 * time.Minute
	maximumPrivateText     = 4096
	maximumBucketModels    = 128
)

type privateUsageResponseV1 struct {
	BillingCycleStart                int64                     `json:"billingCycleStart,string"`
	BillingCycleEnd                  int64                     `json:"billingCycleEnd,string"`
	PlanUsage                        *privatePlanUsageV1       `json:"planUsage"`
	SpendLimitUsage                  *privateSpendLimitUsageV1 `json:"spendLimitUsage"`
	DisplayThreshold                 *int32                    `json:"displayThreshold"`
	Enabled                          bool                      `json:"enabled"`
	DisplayMessage                   string                    `json:"displayMessage"`
	FreeBestOfNPromotion             *privatePromotionV1       `json:"freeBestOfNPromotion"`
	AutoModelSelectedDisplayMessage  *string                   `json:"autoModelSelectedDisplayMessage"`
	NamedModelSelectedDisplayMessage *string                   `json:"namedModelSelectedDisplayMessage"`
	AutoBucketModels                 []string                  `json:"autoBucketModels"`
}

type privatePlanUsageV1 struct {
	TotalSpend       int32    `json:"totalSpend"`
	IncludedSpend    int32    `json:"includedSpend"`
	BonusSpend       int32    `json:"bonusSpend"`
	Remaining        int32    `json:"remaining"`
	Limit            int32    `json:"limit"`
	RemainingBonus   *bool    `json:"remainingBonus"`
	BonusTooltip     *string  `json:"bonusTooltip"`
	AutoSpend        *int32   `json:"autoSpend"`
	APISpend         *int32   `json:"apiSpend"`
	AutoLimit        *int32   `json:"autoLimit"`
	APILimit         *int32   `json:"apiLimit"`
	AutoPercentUsed  *float64 `json:"autoPercentUsed"`
	APIPercentUsed   *float64 `json:"apiPercentUsed"`
	TotalPercentUsed *float64 `json:"totalPercentUsed"`
}

type privateSpendLimitUsageV1 struct {
	TotalSpend          int32  `json:"totalSpend"`
	PooledLimit         *int32 `json:"pooledLimit"`
	PooledUsed          *int32 `json:"pooledUsed"`
	PooledRemaining     *int32 `json:"pooledRemaining"`
	IndividualLimit     *int32 `json:"individualLimit"`
	IndividualUsed      int32  `json:"individualUsed"`
	IndividualRemaining int32  `json:"individualRemaining"`
	LimitType           string `json:"limitType"`
	OverallLimit        *int32 `json:"overallLimit"`
	OverallUsed         *int32 `json:"overallUsed"`
	OverallRemaining    *int32 `json:"overallRemaining"`
}

type privatePromotionV1 struct {
	TrialsUsed      int32 `json:"trialsUsed"`
	TrialsRemaining int32 `json:"trialsRemaining"`
}

func collectProvider(ctx context.Context, config Config) (aisnapshot.Provider, error) {
	token, err := config.TokenSource.AccessToken(ctx)
	if err != nil {
		return aisnapshot.Provider{}, normalizeCollectionError(err)
	}
	defer overwrite(token)

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		config.endpointURL,
		bytes.NewReader([]byte("{}")),
	)
	if err != nil {
		return aisnapshot.Provider{}, ErrUnavailable
	}
	authorization := "Bearer " + string(token)
	request.Header.Set("Authorization", authorization)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Connect-Protocol-Version", "1")
	request.Header.Set("Accept", "application/json")

	response, requestErr := config.HTTPClient.Do(request)
	request.Header.Del("Authorization")
	authorization = ""
	if requestErr != nil {
		return aisnapshot.Provider{}, normalizeCollectionError(requestErr)
	}
	defer response.Body.Close()

	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return aisnapshot.Provider{}, ErrNotLoggedIn
	case http.StatusForbidden:
		return aisnapshot.Provider{}, ErrPermission
	default:
		return aisnapshot.Provider{}, ErrUnavailable
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/json" && mediaType != "application/connect+json") {
		return aisnapshot.Provider{}, ErrSchemaChanged
	}
	if response.ContentLength > config.maximumResponse {
		return aisnapshot.Provider{}, ErrSchemaChanged
	}
	document, err := io.ReadAll(io.LimitReader(response.Body, config.maximumResponse+1))
	if err != nil {
		return aisnapshot.Provider{}, normalizeCollectionError(err)
	}
	if int64(len(document)) > config.maximumResponse {
		return aisnapshot.Provider{}, ErrSchemaChanged
	}
	var privateResponse privateUsageResponseV1
	if !utf8.Valid(document) || containsJSONNull(document) ||
		protocol.DecodeStrictDocumentLimit(
			document,
			int(config.maximumResponse),
			&privateResponse,
		) != nil || !validPrivateUsageResponse(privateResponse) {
		return aisnapshot.Provider{}, ErrSchemaChanged
	}
	return normalizeProvider(privateResponse, config.Now())
}

func containsJSONNull(document []byte) bool {
	var value any
	if protocol.DecodeStrictDocumentLimit(document, len(document), &value) != nil {
		return true
	}
	return valueContainsNull(value)
}

func valueContainsNull(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case map[string]any:
		for _, child := range typed {
			if valueContainsNull(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if valueContainsNull(child) {
				return true
			}
		}
	}
	return false
}

func validPrivateUsageResponse(response privateUsageResponseV1) bool {
	if response.BillingCycleStart <= 0 || response.BillingCycleEnd <= response.BillingCycleStart ||
		response.BillingCycleEnd > time.Date(9999, 12, 31, 23, 59, 59, 999_000_000, time.UTC).UnixMilli() ||
		response.DisplayThreshold != nil && (*response.DisplayThreshold < 0 || *response.DisplayThreshold > 100) ||
		!safePrivateText(response.DisplayMessage) || len(response.AutoBucketModels) > maximumBucketModels ||
		!safeOptionalPrivateText(response.AutoModelSelectedDisplayMessage) ||
		!safeOptionalPrivateText(response.NamedModelSelectedDisplayMessage) {
		return false
	}
	for _, model := range response.AutoBucketModels {
		if !safePrivateText(model) {
			return false
		}
	}
	if response.FreeBestOfNPromotion != nil &&
		(response.FreeBestOfNPromotion.TrialsUsed < 0 || response.FreeBestOfNPromotion.TrialsRemaining < 0) {
		return false
	}
	if response.PlanUsage != nil && !validPlanUsage(*response.PlanUsage) {
		return false
	}
	if response.SpendLimitUsage != nil && !validSpendLimitUsage(*response.SpendLimitUsage) {
		return false
	}
	return true
}

func validPlanUsage(usage privatePlanUsageV1) bool {
	if usage.TotalSpend < 0 || usage.IncludedSpend < 0 || usage.BonusSpend < 0 || usage.Limit < 0 ||
		!safeOptionalPrivateText(usage.BonusTooltip) {
		return false
	}
	for _, amount := range []*int32{usage.AutoSpend, usage.APISpend, usage.AutoLimit, usage.APILimit} {
		if amount != nil && *amount < 0 {
			return false
		}
	}
	for _, percentage := range []*float64{
		usage.AutoPercentUsed,
		usage.APIPercentUsed,
		usage.TotalPercentUsed,
	} {
		if percentage != nil && (math.IsNaN(*percentage) || math.IsInf(*percentage, 0) ||
			*percentage < 0 || *percentage > 1_000_000) {
			return false
		}
	}
	return true
}

func validSpendLimitUsage(usage privateSpendLimitUsageV1) bool {
	if usage.TotalSpend < 0 || usage.IndividualUsed < 0 || !safePrivateText(usage.LimitType) {
		return false
	}
	for _, amount := range []*int32{
		usage.PooledLimit,
		usage.PooledUsed,
		usage.IndividualLimit,
		usage.OverallLimit,
		usage.OverallUsed,
	} {
		if amount != nil && *amount < 0 {
			return false
		}
	}
	return true
}

func safeOptionalPrivateText(value *string) bool {
	return value == nil || safePrivateText(*value)
}

func safePrivateText(value string) bool {
	return utf8.ValidString(value) && len(value) <= maximumPrivateText
}

func normalizeProvider(response privateUsageResponseV1, now time.Time) (aisnapshot.Provider, error) {
	if !response.Enabled || response.PlanUsage == nil || response.PlanUsage.Limit <= 0 {
		return aisnapshot.Provider{}, ErrUnavailable
	}
	cycleDuration := response.BillingCycleEnd - response.BillingCycleStart
	windowMinutes := cycleDuration / int64(time.Minute/time.Millisecond)
	if cycleDuration%int64(time.Minute/time.Millisecond) != 0 || windowMinutes <= 0 || windowMinutes > 525_600 {
		return aisnapshot.Provider{}, ErrSchemaChanged
	}
	used := basisPoints(response.PlanUsage.IncludedSpend, response.PlanUsage.Limit)
	remaining := uint16(10_000 - used)
	minutes := uint32(windowMinutes)
	resetTime := time.UnixMilli(response.BillingCycleEnd).UTC()
	reset := resetTime.Format(time.RFC3339Nano)
	resetUnixMS := resetTime.UnixMilli()
	windows := []aisnapshot.QuotaWindow{{
		Name:                 "billing",
		UsedBasisPoints:      &used,
		RemainingBasisPoints: &remaining,
		WindowMinutes:        &minutes,
		ResetsAt:             &reset,
		ResetsAtUnixMS:       &resetUnixMS,
	}}
	if limitUsage := response.SpendLimitUsage; limitUsage != nil &&
		limitUsage.IndividualLimit != nil && *limitUsage.IndividualLimit > 0 {
		limitUsed := basisPoints(limitUsage.IndividualUsed, *limitUsage.IndividualLimit)
		limitRemaining := uint16(10_000 - limitUsed)
		windows = append(windows, aisnapshot.QuotaWindow{
			Name:                 "spend_limit",
			UsedBasisPoints:      &limitUsed,
			RemainingBasisPoints: &limitRemaining,
		})
	}
	updatedTime := now.UTC()
	updated := updatedTime.Format(time.RFC3339Nano)
	updatedUnixMS := updatedTime.UnixMilli()
	staleAfter := uint32(defaultStaleAfter / time.Second)
	return aisnapshot.Provider{
		SchemaVersion:     aisnapshot.SchemaVersion{Major: aisnapshot.SchemaMajor, Minor: aisnapshot.SchemaMinor},
		ID:                providerID,
		DisplayName:       providerName,
		Status:            aisnapshot.ProviderOK,
		Source:            aisnapshot.ProviderSourceCursorLocal,
		Confidence:        aisnapshot.ConfidenceInferred,
		Experimental:      true,
		UpdatedAt:         &updated,
		UpdatedAtUnixMS:   &updatedUnixMS,
		StaleAfterSeconds: &staleAfter,
		Windows:           windows,
	}, nil
}

func basisPoints(used, limit int32) uint16 {
	if used <= 0 || limit <= 0 {
		return 0
	}
	if used >= limit {
		return 10_000
	}
	return uint16((int64(used)*10_000 + int64(limit)/2) / int64(limit))
}

func normalizeCollectionError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, ErrNotLoggedIn):
		return ErrNotLoggedIn
	case errors.Is(err, ErrPermission):
		return ErrPermission
	case errors.Is(err, ErrDatabaseLocked):
		return ErrDatabaseLocked
	case errors.Is(err, ErrSchemaChanged):
		return ErrSchemaChanged
	default:
		return ErrUnavailable
	}
}
