package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
)

const maximumSafeInteger = int64(9_007_199_254_740_991)

type initializeResult struct {
	CodexHome      string `json:"codexHome"`
	PlatformFamily string `json:"platformFamily"`
	PlatformOS     string `json:"platformOs"`
	UserAgent      string `json:"userAgent"`
}

type rawRateLimitResult struct {
	RateLimits            *rawRateLimitSnapshot           `json:"rateLimits"`
	RateLimitsByLimitID   map[string]rawRateLimitSnapshot `json:"rateLimitsByLimitId"`
	RateLimitResetCredits json.RawMessage                 `json:"rateLimitResetCredits"`
}

type rawRateLimitUpdated struct {
	RateLimits *rawRateLimitSnapshot `json:"rateLimits"`
}

type rawRateLimitSnapshot struct {
	Credits              json.RawMessage `json:"credits"`
	IndividualLimit      json.RawMessage `json:"individualLimit"`
	LimitID              *string         `json:"limitId"`
	LimitName            *string         `json:"limitName"`
	PlanType             *string         `json:"planType"`
	Primary              *rawRateWindow  `json:"primary"`
	RateLimitReachedType *string         `json:"rateLimitReachedType"`
	Secondary            *rawRateWindow  `json:"secondary"`
	SpendControlReached  *bool           `json:"spendControlReached"`
}

type rawRateWindow struct {
	ResetsAt          *int64 `json:"resetsAt"`
	UsedPercent       *int64 `json:"usedPercent"`
	WindowDurationMin *int64 `json:"windowDurationMins"`
}

type rawUsageResult struct {
	DailyUsageBuckets *[]rawDailyUsage `json:"dailyUsageBuckets"`
	Summary           *rawUsageSummary `json:"summary"`
}

type rawDailyUsage struct {
	StartDate string `json:"startDate"`
	Tokens    int64  `json:"tokens"`
}

type rawUsageSummary struct {
	CurrentStreakDays     *int64 `json:"currentStreakDays"`
	LifetimeTokens        *int64 `json:"lifetimeTokens"`
	LongestRunningTurnSec *int64 `json:"longestRunningTurnSec"`
	LongestStreakDays     *int64 `json:"longestStreakDays"`
	PeakDailyTokens       *int64 `json:"peakDailyTokens"`
}

func initialize(ctx context.Context, client *rpcClient, version string) error {
	var result initializeResult
	err := client.Call(ctx, "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    "s3-rlcd-deck-companion",
			"version": version,
		},
		"capabilities": map[string]any{
			"experimentalApi": false,
		},
	}, &result)
	if err != nil {
		return err
	}
	if result.CodexHome == "" || result.PlatformFamily == "" ||
		result.PlatformOS == "" || result.UserAgent == "" {
		return ErrSchemaChanged
	}
	// CodexHome and userAgent are deliberately discarded here. They may
	// contain a local path or environment identity and never enter our DTO.
	result = initializeResult{}
	return client.Notify(ctx, "initialized", nil)
}

func collectProvider(
	ctx context.Context,
	client *rpcClient,
	now time.Time,
) (aisnapshot.Provider, error) {
	type rateResult struct {
		value rawRateLimitResult
		err   error
	}
	type usageResult struct {
		value rawUsageResult
		err   error
	}
	rateChannel := make(chan rateResult, 1)
	usageChannel := make(chan usageResult, 1)
	go func() {
		var value rawRateLimitResult
		err := client.Call(ctx, "account/rateLimits/read", nil, &value)
		rateChannel <- rateResult{value: value, err: err}
	}()
	go func() {
		var value rawUsageResult
		err := client.Call(ctx, "account/usage/read", nil, &value)
		usageChannel <- usageResult{value: value, err: err}
	}()
	rate := <-rateChannel
	usage := <-usageChannel
	if rate.err != nil {
		return aisnapshot.Provider{}, fmt.Errorf("read rate limits: %w", rate.err)
	}
	if usage.err != nil {
		return aisnapshot.Provider{}, fmt.Errorf("read usage: %w", usage.err)
	}
	windows, err := normalizeRateLimits(rate.value)
	if err != nil {
		return aisnapshot.Provider{}, fmt.Errorf("normalize rate limits: %w", err)
	}
	tokens, err := normalizeUsage(usage.value)
	if err != nil {
		return aisnapshot.Provider{}, fmt.Errorf("normalize usage: %w", err)
	}
	updatedAt := canonicalTime(now)
	staleAfter := uint32(300)
	return aisnapshot.Provider{
		SchemaVersion:     aisnapshot.SchemaVersion{Major: 1, Minor: 0},
		ID:                providerID,
		DisplayName:       "Codex",
		Status:            aisnapshot.ProviderOK,
		Source:            aisnapshot.ProviderSourceCodexAppServer,
		Confidence:        aisnapshot.ConfidenceVerified,
		Experimental:      false,
		UpdatedAt:         &updatedAt,
		StaleAfterSeconds: &staleAfter,
		Windows:           windows,
		Tokens:            tokens,
	}, nil
}

func validateRateLimitNotification(document json.RawMessage) error {
	var update rawRateLimitUpdated
	if err := strictDecode(document, &update); err != nil || update.RateLimits == nil {
		return ErrSchemaChanged
	}
	_, err := normalizeSnapshots([]namedRateSnapshot{{name: "codex", value: *update.RateLimits}})
	return err
}

type namedRateSnapshot struct {
	name  string
	value rawRateLimitSnapshot
}

func normalizeRateLimits(result rawRateLimitResult) ([]aisnapshot.QuotaWindow, error) {
	if result.RateLimits == nil {
		return nil, ErrSchemaChanged
	}
	var snapshots []namedRateSnapshot
	if len(result.RateLimitsByLimitID) == 0 {
		snapshots = append(snapshots, namedRateSnapshot{name: "codex", value: *result.RateLimits})
	} else {
		keys := make([]string, 0, len(result.RateLimitsByLimitID))
		for key := range result.RateLimitsByLimitID {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			snapshots = append(snapshots, namedRateSnapshot{
				name:  key,
				value: result.RateLimitsByLimitID[key],
			})
		}
	}
	return normalizeSnapshots(snapshots)
}

func normalizeSnapshots(snapshots []namedRateSnapshot) ([]aisnapshot.QuotaWindow, error) {
	windows := make([]aisnapshot.QuotaWindow, 0, len(snapshots)*2)
	usedNames := make(map[string]int)
	for _, snapshot := range snapshots {
		base := safeWindowBase(snapshot.value, snapshot.name)
		for _, candidate := range []struct {
			suffix string
			window *rawRateWindow
		}{
			{suffix: "primary", window: snapshot.value.Primary},
			{suffix: "secondary", window: snapshot.value.Secondary},
		} {
			if candidate.window == nil {
				continue
			}
			window, err := normalizeWindow(
				uniqueWindowName(base, candidate.suffix, usedNames),
				*candidate.window,
			)
			if err != nil {
				return nil, err
			}
			windows = append(windows, window)
			if len(windows) > 4 {
				return nil, ErrSchemaChanged
			}
		}
	}
	return windows, nil
}

func normalizeWindow(name string, raw rawRateWindow) (aisnapshot.QuotaWindow, error) {
	if raw.UsedPercent == nil || *raw.UsedPercent < 0 || *raw.UsedPercent > 100 {
		return aisnapshot.QuotaWindow{}, ErrSchemaChanged
	}
	used := uint16(*raw.UsedPercent * 100)
	remaining := uint16(10_000 - used)
	window := aisnapshot.QuotaWindow{
		Name:                 name,
		UsedBasisPoints:      &used,
		RemainingBasisPoints: &remaining,
	}
	if raw.WindowDurationMin != nil {
		if *raw.WindowDurationMin < 1 || *raw.WindowDurationMin > 525_600 {
			return aisnapshot.QuotaWindow{}, ErrSchemaChanged
		}
		minutes := uint32(*raw.WindowDurationMin)
		window.WindowMinutes = &minutes
	}
	if raw.ResetsAt != nil {
		if *raw.ResetsAt < 0 || *raw.ResetsAt > maximumSafeInteger {
			return aisnapshot.QuotaWindow{}, ErrSchemaChanged
		}
		reset := time.Unix(*raw.ResetsAt, 0).UTC()
		if reset.Year() < 1970 || reset.Year() > 9999 {
			return aisnapshot.QuotaWindow{}, ErrSchemaChanged
		}
		formatted := reset.Format(time.RFC3339)
		window.ResetsAt = &formatted
	}
	return window, nil
}

func normalizeUsage(result rawUsageResult) (*aisnapshot.TokenUsage, error) {
	if result.Summary == nil {
		return nil, ErrSchemaChanged
	}
	values := []*int64{
		result.Summary.CurrentStreakDays,
		result.Summary.LifetimeTokens,
		result.Summary.LongestRunningTurnSec,
		result.Summary.LongestStreakDays,
		result.Summary.PeakDailyTokens,
	}
	for _, value := range values {
		if value != nil && (*value < 0 || *value > maximumSafeInteger) {
			return nil, ErrSchemaChanged
		}
	}
	if result.DailyUsageBuckets != nil {
		for _, bucket := range *result.DailyUsageBuckets {
			if bucket.Tokens < 0 || bucket.Tokens > maximumSafeInteger {
				return nil, ErrSchemaChanged
			}
			parsed, err := time.Parse("2006-01-02", bucket.StartDate)
			if err != nil || parsed.Format("2006-01-02") != bucket.StartDate {
				return nil, ErrSchemaChanged
			}
		}
	}
	tokens := &aisnapshot.TokenUsage{}
	if result.Summary.LifetimeTokens != nil {
		total := uint64(*result.Summary.LifetimeTokens)
		tokens.Total = &total
	}
	return tokens, nil
}

func safeWindowBase(snapshot rawRateLimitSnapshot, fallback string) string {
	for _, candidate := range []*string{snapshot.LimitName, snapshot.LimitID, &fallback} {
		if candidate == nil || !safeUpstreamLabel(*candidate) {
			continue
		}
		if normalized := identifier(*candidate); normalized != "" {
			return normalized
		}
	}
	return "codex"
}

func safeUpstreamLabel(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '_' || character == '-' || character == '.') {
			return false
		}
	}
	return true
}

func identifier(value string) string {
	var output strings.Builder
	lastUnderscore := false
	for _, character := range strings.ToLower(value) {
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') {
			output.WriteRune(character)
			lastUnderscore = false
		} else if output.Len() != 0 && !lastUnderscore {
			output.WriteByte('_')
			lastUnderscore = true
		}
	}
	normalized := strings.Trim(output.String(), "_")
	if normalized != "" && normalized[0] >= '0' && normalized[0] <= '9' {
		normalized = "w_" + normalized
	}
	return normalized
}

func uniqueWindowName(base string, suffix string, used map[string]int) string {
	maximumBase := 24 - len(suffix) - 1
	if len(base) > maximumBase {
		base = strings.Trim(base[:maximumBase], "_")
	}
	if base == "" {
		base = "codex"
	}
	name := base + "_" + suffix
	if used[name] == 0 {
		used[name] = 1
		return name
	}
	for sequence := 2; ; sequence++ {
		extra := fmt.Sprintf("_%d", sequence)
		candidate := name
		if len(candidate)+len(extra) > 24 {
			candidate = candidate[:24-len(extra)]
		}
		candidate += extra
		if used[candidate] == 0 {
			used[candidate] = 1
			return candidate
		}
	}
}

func canonicalTime(value time.Time) string {
	return value.UTC().Truncate(time.Second).Format(time.RFC3339)
}

func classifyFailure(err error) aisnapshot.ProviderError {
	problem := aisnapshot.ProviderError{
		Code:      aisnapshot.ProviderErrorUnavailable,
		Retryable: true,
	}
	if errors.Is(err, context.DeadlineExceeded) {
		problem.Code = aisnapshot.ProviderErrorTimeout
		return problem
	}
	if errors.Is(err, ErrSchemaChanged) {
		problem.Code = aisnapshot.ProviderErrorSchemaChanged
		problem.Retryable = false
		return problem
	}
	if errors.Is(err, ErrProcessExited) {
		problem.Code = aisnapshot.ProviderErrorProcessExited
		return problem
	}
	var methodProblem *methodError
	if !errors.As(err, &methodProblem) {
		return problem
	}
	message := strings.ToLower(methodProblem.message)
	switch {
	case methodProblem.code == -32601 || methodProblem.code == -32602:
		problem.Code = aisnapshot.ProviderErrorSchemaChanged
		problem.Retryable = false
	case strings.Contains(message, "not logged in") ||
		strings.Contains(message, "unauthorized") ||
		strings.Contains(message, "authentication"):
		problem.Code = aisnapshot.ProviderErrorAuthStale
		problem.Retryable = false
	case strings.Contains(message, "permission") || strings.Contains(message, "forbidden"):
		problem.Code = aisnapshot.ProviderErrorPermissionDenied
		problem.Retryable = false
	}
	return problem
}

func degradedProvider(err error) aisnapshot.Provider {
	problem := classifyFailure(err)
	return aisnapshot.Provider{
		SchemaVersion: aisnapshot.SchemaVersion{Major: 1, Minor: 0},
		ID:            providerID,
		DisplayName:   "Codex",
		Status:        aisnapshot.ProviderDegraded,
		Source:        aisnapshot.ProviderSourceNone,
		Confidence:    aisnapshot.ConfidenceUnavailable,
		Windows:       []aisnapshot.QuotaWindow{},
		Error:         &problem,
	}
}
