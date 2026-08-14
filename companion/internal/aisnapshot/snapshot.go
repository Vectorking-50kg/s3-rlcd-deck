package aisnapshot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
)

const (
	SchemaMajor        = 1
	SchemaMinor        = 0
	maxSafeJSONInteger = uint64(9_007_199_254_740_991)
	maxProviders       = 8
	maxSessions        = 16
	maxWindows         = 4
	maxJSONNodes       = 2048
	maxForwardFields   = 16
)

var (
	ErrMalformedSnapshot  = errors.New("malformed AI Snapshot")
	ErrUnsupportedVersion = errors.New("unsupported AI Snapshot version")
	ErrPrivateData        = errors.New("private data in AI Snapshot")
)

var privateFieldNames = map[string]struct{}{
	"absolutepath":  {},
	"accesstoken":   {},
	"apikey":        {},
	"attributes":    {},
	"command":       {},
	"commandoutput": {},
	"credential":    {},
	"credentials":   {},
	"path":          {},
	"prompt":        {},
	"raw":           {},
	"reply":         {},
	"rawresponse":   {},
	"refreshtoken":  {},
	"response":      {},
	"toolarguments": {},
	"toolargs":      {},
	"toolparams":    {},
	"upstream":      {},
}

type SchemaVersion struct {
	Major uint16 `json:"major"`
	Minor uint16 `json:"minor"`
}

type ProviderStatus string

const (
	ProviderOK          ProviderStatus = "ok"
	ProviderDegraded    ProviderStatus = "degraded"
	ProviderUnavailable ProviderStatus = "unavailable"
)

type ProviderSource string

const (
	ProviderSourceCodexAppServer ProviderSource = "codex_app_server"
	ProviderSourceCursorLocal    ProviderSource = "cursor_local"
	ProviderSourceStructuredHTTP ProviderSource = "structured_http"
	ProviderSourceNone           ProviderSource = "none"
)

type Confidence string

const (
	ConfidenceVerified    Confidence = "verified"
	ConfidenceInferred    Confidence = "inferred"
	ConfidenceUnavailable Confidence = "unavailable"
)

type Money struct {
	AmountMicros uint64 `json:"amount_micros"`
	Currency     string `json:"currency"`
}

type QuotaWindow struct {
	Name                 string  `json:"name"`
	UsedBasisPoints      *uint16 `json:"used_basis_points"`
	RemainingBasisPoints *uint16 `json:"remaining_basis_points"`
	WindowMinutes        *uint32 `json:"window_minutes"`
	ResetsAt             *string `json:"resets_at"`
	ResetsAtUnixMS       *int64  `json:"-"`
}

type TokenUsage struct {
	Input       *uint64 `json:"input"`
	CachedInput *uint64 `json:"cached_input"`
	Output      *uint64 `json:"output"`
	Reasoning   *uint64 `json:"reasoning"`
	Total       *uint64 `json:"total"`
}

type ProviderErrorCode string

const (
	ProviderErrorAuthStale        ProviderErrorCode = "auth_stale"
	ProviderErrorPermissionDenied ProviderErrorCode = "permission_denied"
	ProviderErrorTimeout          ProviderErrorCode = "timeout"
	ProviderErrorProcessExited    ProviderErrorCode = "process_exited"
	ProviderErrorSchemaChanged    ProviderErrorCode = "schema_changed"
	ProviderErrorUnavailable      ProviderErrorCode = "unavailable"
)

type ProviderError struct {
	Code      ProviderErrorCode `json:"code"`
	Retryable bool              `json:"retryable"`
}

type Provider struct {
	SchemaVersion     SchemaVersion  `json:"schema_version"`
	ID                string         `json:"provider_id"`
	DisplayName       string         `json:"display_name"`
	Status            ProviderStatus `json:"status"`
	Source            ProviderSource `json:"source"`
	Confidence        Confidence     `json:"confidence"`
	Experimental      bool           `json:"experimental"`
	UpdatedAt         *string        `json:"updated_at"`
	UpdatedAtUnixMS   *int64         `json:"-"`
	StaleAfterSeconds *uint32        `json:"stale_after_seconds"`
	Balance           *Money         `json:"balance"`
	Windows           []QuotaWindow  `json:"windows"`
	Tokens            *TokenUsage    `json:"tokens"`
	Error             *ProviderError `json:"error"`
}

type SessionState string

const (
	SessionRunning         SessionState = "running"
	SessionWaitingApproval SessionState = "waiting_approval"
	SessionWaitingInput    SessionState = "waiting_input"
	SessionCompleted       SessionState = "completed"
	SessionFailed          SessionState = "failed"
	SessionRecent          SessionState = "recent"
	SessionEnded           SessionState = "ended"
	SessionUnknown         SessionState = "unknown"
	SessionUnavailable     SessionState = "unavailable"
)

type SessionSource string

const (
	SessionSourceCodexAppServerOwned SessionSource = "codex_app_server_owned"
	SessionSourceProcessJSONL        SessionSource = "process_jsonl_observer"
	SessionSourceNone                SessionSource = "none"
)

type Session struct {
	SchemaVersion          SchemaVersion `json:"schema_version"`
	ID                     string        `json:"session_id"`
	ProviderID             string        `json:"provider_id"`
	DisplayName            *string       `json:"display_name"`
	State                  SessionState  `json:"state"`
	Source                 SessionSource `json:"source"`
	Confidence             Confidence    `json:"confidence"`
	StartedAt              *string       `json:"started_at"`
	StartedAtUnixMS        *int64        `json:"-"`
	LastActivityAt         *string       `json:"last_activity_at"`
	LastActivityAtUnixMS   *int64        `json:"-"`
	DurationSeconds        *uint32       `json:"duration_seconds"`
	TurnTokens             *uint64       `json:"turn_tokens"`
	ContextUsedBasisPoints *uint16       `json:"context_used_basis_points"`
}

type Snapshot struct {
	Type              string        `json:"type"`
	ProtocolVersion   uint32        `json:"protocol_version"`
	SchemaVersion     SchemaVersion `json:"schema_version"`
	GeneratedAt       string        `json:"generated_at"`
	GeneratedAtUnixMS int64         `json:"-"`
	Timezone          *string       `json:"timezone"`
	ProviderOrder     []string      `json:"provider_order"`
	Providers         []Provider    `json:"providers"`
	Sessions          []Session     `json:"sessions"`
	NextRefresh       uint32        `json:"next_refresh_seconds"`
}

// Retained owns the last document that passed the complete wire contract.
// Failed updates, including unknown schema majors, never mutate that value.
type Retained struct {
	mutex    sync.RWMutex
	document []byte
}

func (retained *Retained) Apply(document []byte) (Snapshot, error) {
	snapshot, err := Decode(document)
	if err != nil {
		return Snapshot{}, err
	}
	copyOfDocument := append([]byte(nil), document...)
	retained.mutex.Lock()
	retained.document = copyOfDocument
	retained.mutex.Unlock()
	return snapshot, nil
}

func (retained *Retained) Current() (Snapshot, bool) {
	retained.mutex.RLock()
	document := append([]byte(nil), retained.document...)
	retained.mutex.RUnlock()
	if len(document) == 0 {
		return Snapshot{}, false
	}
	snapshot, err := Decode(document)
	if err != nil {
		return Snapshot{}, false
	}
	return snapshot, true
}

// Encode is the only supported collector boundary for constructing a wire
// snapshot. It deliberately re-enters Decode so locally produced documents and
// remotely received documents cannot drift into different contracts.
func Encode(snapshot Snapshot) ([]byte, error) {
	document, err := json.Marshal(snapshot)
	if err != nil {
		return nil, errors.Join(ErrMalformedSnapshot, err)
	}
	if _, err = Decode(document); err != nil {
		return nil, err
	}
	return document, nil
}

func Decode(document []byte) (Snapshot, error) {
	envelope, err := protocol.ParseEnvelope(document)
	if err != nil || envelope.Type != "snapshot.ai" {
		return Snapshot{}, errors.Join(ErrMalformedSnapshot, err)
	}
	var privacyDocument any
	if err = json.Unmarshal(document, &privacyDocument); err != nil {
		return Snapshot{}, errors.Join(ErrMalformedSnapshot, err)
	}
	if jsonNodeCount(privacyDocument) > maxJSONNodes {
		return Snapshot{}, ErrMalformedSnapshot
	}
	if containsPrivateContent(privacyDocument) {
		return Snapshot{}, ErrPrivateData
	}
	var root map[string]json.RawMessage
	if err = json.Unmarshal(document, &root); err != nil {
		return Snapshot{}, errors.Join(ErrMalformedSnapshot, err)
	}
	if err = requireFields(root, []string{
		"type", "protocol_version", "schema_version", "generated_at", "timezone",
		"provider_order", "providers", "sessions", "next_refresh_seconds",
	}, []string{
		"type", "protocol_version", "schema_version", "generated_at",
		"provider_order", "providers", "sessions", "next_refresh_seconds",
	}); err != nil {
		return Snapshot{}, err
	}
	version, err := decodeSchemaVersion(root["schema_version"])
	if err != nil {
		return Snapshot{}, err
	}
	if err = validateObjectFields(root, version.Minor, stringSet(
		"type", "protocol_version", "schema_version", "generated_at", "timezone",
		"provider_order", "providers", "sessions", "next_refresh_seconds",
	)); err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	if err = json.Unmarshal(document, &snapshot); err != nil {
		return Snapshot{}, errors.Join(ErrMalformedSnapshot, err)
	}
	if snapshot.ProtocolVersion != protocol.CurrentVersion ||
		snapshot.NextRefresh == 0 || snapshot.NextRefresh > 3600 ||
		snapshot.ProviderOrder == nil || snapshot.Providers == nil || snapshot.Sessions == nil {
		return Snapshot{}, ErrMalformedSnapshot
	}
	generatedAt, parseErr := canonicalUTC(snapshot.GeneratedAt)
	if parseErr != nil {
		return Snapshot{}, ErrMalformedSnapshot
	}
	snapshot.GeneratedAtUnixMS = generatedAt.UnixMilli()
	if snapshot.Timezone != nil && !safeText(*snapshot.Timezone, 64) {
		return Snapshot{}, ErrMalformedSnapshot
	}
	if len(snapshot.Providers) > maxProviders || len(snapshot.ProviderOrder) > maxProviders ||
		len(snapshot.Sessions) > maxSessions {
		return Snapshot{}, ErrMalformedSnapshot
	}
	var providerDocuments []json.RawMessage
	if err = json.Unmarshal(root["providers"], &providerDocuments); err != nil ||
		len(providerDocuments) != len(snapshot.Providers) {
		return Snapshot{}, ErrMalformedSnapshot
	}
	providerIDs := make(map[string]struct{}, len(snapshot.Providers))
	for index := range snapshot.Providers {
		if err = validateProvider(
			providerDocuments[index], &snapshot.Providers[index], generatedAt,
		); err != nil {
			return Snapshot{}, fmt.Errorf("provider %d: %w", index, err)
		}
		providerID := snapshot.Providers[index].ID
		if _, duplicate := providerIDs[providerID]; duplicate ||
			index >= len(snapshot.ProviderOrder) || snapshot.ProviderOrder[index] != providerID {
			return Snapshot{}, ErrMalformedSnapshot
		}
		providerIDs[providerID] = struct{}{}
	}
	if len(snapshot.ProviderOrder) != len(snapshot.Providers) {
		return Snapshot{}, ErrMalformedSnapshot
	}
	var sessionDocuments []json.RawMessage
	if err = json.Unmarshal(root["sessions"], &sessionDocuments); err != nil ||
		len(sessionDocuments) != len(snapshot.Sessions) {
		return Snapshot{}, ErrMalformedSnapshot
	}
	sessionIDs := make(map[string]struct{}, len(snapshot.Sessions))
	for index := range snapshot.Sessions {
		if err = validateSession(
			sessionDocuments[index], &snapshot.Sessions[index], generatedAt,
		); err != nil {
			return Snapshot{}, fmt.Errorf("session %d: %w", index, err)
		}
		session := snapshot.Sessions[index]
		if _, duplicate := sessionIDs[session.ID]; duplicate {
			return Snapshot{}, ErrMalformedSnapshot
		}
		if _, providerExists := providerIDs[session.ProviderID]; !providerExists {
			return Snapshot{}, ErrMalformedSnapshot
		}
		sessionIDs[session.ID] = struct{}{}
	}
	return snapshot, nil
}

func jsonNodeCount(value any) int {
	switch typed := value.(type) {
	case map[string]any:
		count := 1
		for _, child := range typed {
			count += 1 + jsonNodeCount(child)
			if count > maxJSONNodes {
				return count
			}
		}
		return count
	case []any:
		count := 1
		for _, child := range typed {
			count += jsonNodeCount(child)
			if count > maxJSONNodes {
				return count
			}
		}
		return count
	default:
		return 1
	}
}

func containsPrivateContent(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for name, child := range typed {
			if _, private := privateFieldNames[normalizedFieldName(name)]; private ||
				containsPrivateContent(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsPrivateContent(child) {
				return true
			}
		}
	case string:
		return containsAbsolutePath(typed)
	}
	return false
}

func containsAbsolutePath(value string) bool {
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character != '/' && character != '\\' {
			continue
		}
		if index == 0 || pathBoundary(value[index-1]) ||
			(value[index-1] == '~' && (index == 1 || pathBoundary(value[index-2]))) {
			return true
		}
		if index >= 2 && value[index-1] == ':' && isASCIIAlpha(value[index-2]) &&
			(index == 2 || pathBoundary(value[index-3])) {
			return true
		}
	}
	return false
}

func pathBoundary(character byte) bool {
	switch character {
	case ' ', '\t', '\r', '\n', '(', '[', '{', '"', '\'', '=', ':', ';', ',':
		return true
	default:
		return false
	}
}

func isASCIIAlpha(character byte) bool {
	return (character >= 'A' && character <= 'Z') ||
		(character >= 'a' && character <= 'z')
}

func normalizedFieldName(value string) string {
	var normalized strings.Builder
	normalized.Grow(len(value))
	for _, char := range value {
		if char >= 'A' && char <= 'Z' {
			char += 'a' - 'A'
		}
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
			normalized.WriteRune(char)
		}
	}
	return normalized.String()
}

func validateProvider(document json.RawMessage, provider *Provider, generatedAt time.Time) error {
	var object map[string]json.RawMessage
	if json.Unmarshal(document, &object) != nil {
		return ErrMalformedSnapshot
	}
	if err := requireFields(object, []string{
		"schema_version", "provider_id", "display_name", "status", "source",
		"confidence", "experimental", "updated_at", "stale_after_seconds",
		"balance", "windows", "tokens", "error",
	}, []string{
		"schema_version", "provider_id", "display_name", "status", "source",
		"confidence", "experimental", "windows",
	}); err != nil {
		return err
	}
	version, err := decodeSchemaVersion(object["schema_version"])
	if err != nil {
		return err
	}
	if err = validateObjectFields(object, version.Minor, stringSet(
		"schema_version", "provider_id", "display_name", "status", "source",
		"confidence", "experimental", "updated_at", "stale_after_seconds",
		"balance", "windows", "tokens", "error",
	)); err != nil {
		return err
	}
	if !safeIdentifier(provider.ID, 32) || !safeText(provider.DisplayName, 48) ||
		!validProviderStatus(provider.Status) || !validProviderSource(provider.Source) ||
		!validConfidence(provider.Confidence) || len(provider.Windows) > maxWindows ||
		(provider.StaleAfterSeconds != nil && (*provider.StaleAfterSeconds == 0 ||
			*provider.StaleAfterSeconds > 86_400)) {
		return ErrMalformedSnapshot
	}
	if (provider.Source == ProviderSourceNone) !=
		(provider.Confidence == ConfidenceUnavailable) ||
		(provider.Status == ProviderUnavailable && provider.Source != ProviderSourceNone) ||
		(provider.Source == ProviderSourceCodexAppServer && provider.Confidence != ConfidenceVerified) ||
		(provider.Source == ProviderSourceCursorLocal && provider.Confidence != ConfidenceInferred) {
		return ErrMalformedSnapshot
	}
	if provider.UpdatedAt != nil {
		parsed, parseErr := canonicalUTC(*provider.UpdatedAt)
		if parseErr != nil || parsed.After(generatedAt) {
			return ErrMalformedSnapshot
		}
		provider.UpdatedAtUnixMS = unixMS(parsed)
	}
	if err = validateMoney(object["balance"], provider.Balance, SchemaMinor); err != nil {
		return err
	}
	var windows []json.RawMessage
	if json.Unmarshal(object["windows"], &windows) != nil || len(windows) != len(provider.Windows) {
		return ErrMalformedSnapshot
	}
	for index := range provider.Windows {
		if err = validateWindow(windows[index], &provider.Windows[index], SchemaMinor); err != nil {
			return err
		}
	}
	if err = validateTokens(object["tokens"], provider.Tokens, SchemaMinor); err != nil {
		return err
	}
	if err = validateProviderError(object["error"], provider.Error, SchemaMinor); err != nil {
		return err
	}
	if (provider.Status == ProviderOK && provider.Error != nil) ||
		(provider.Status != ProviderOK && provider.Error == nil) {
		return ErrMalformedSnapshot
	}
	return nil
}

func validateSession(document json.RawMessage, session *Session, generatedAt time.Time) error {
	var object map[string]json.RawMessage
	if json.Unmarshal(document, &object) != nil {
		return ErrMalformedSnapshot
	}
	if err := requireFields(object, []string{
		"schema_version", "session_id", "provider_id", "display_name", "state", "source",
		"confidence", "started_at", "last_activity_at", "duration_seconds", "turn_tokens",
		"context_used_basis_points",
	}, []string{"schema_version", "session_id", "provider_id", "state", "source", "confidence"}); err != nil {
		return err
	}
	version, err := decodeSchemaVersion(object["schema_version"])
	if err != nil {
		return err
	}
	if err = validateObjectFields(object, version.Minor, stringSet(
		"schema_version", "session_id", "provider_id", "display_name", "state", "source",
		"confidence", "started_at", "last_activity_at", "duration_seconds", "turn_tokens",
		"context_used_basis_points",
	)); err != nil {
		return err
	}
	if !safeOpaqueID(session.ID, 64) || !safeIdentifier(session.ProviderID, 32) ||
		!validSessionState(session.State) || !validSessionSource(session.Source) ||
		!validConfidence(session.Confidence) ||
		(session.DisplayName != nil && !safeText(*session.DisplayName, 48)) ||
		(session.DurationSeconds != nil && *session.DurationSeconds > 31_536_000) ||
		(session.TurnTokens != nil && *session.TurnTokens > maxSafeJSONInteger) ||
		(session.ContextUsedBasisPoints != nil && *session.ContextUsedBasisPoints > 10_000) {
		return ErrMalformedSnapshot
	}
	if (session.Source == SessionSourceNone) != (session.Confidence == ConfidenceUnavailable) ||
		(session.Source == SessionSourceCodexAppServerOwned && session.Confidence != ConfidenceVerified) ||
		(session.Source == SessionSourceProcessJSONL && session.Confidence != ConfidenceInferred) ||
		(session.Source == SessionSourceNone && session.State != SessionUnavailable) {
		return ErrMalformedSnapshot
	}
	if session.Source == SessionSourceCodexAppServerOwned &&
		!oneOfSessionState(session.State, SessionRunning, SessionWaitingApproval,
			SessionWaitingInput, SessionCompleted, SessionFailed) {
		return ErrMalformedSnapshot
	}
	if session.Source == SessionSourceProcessJSONL &&
		!oneOfSessionState(session.State, SessionRunning, SessionRecent, SessionEnded, SessionUnknown) {
		return ErrMalformedSnapshot
	}
	var startedAt, lastActivityAt *time.Time
	if session.StartedAt != nil {
		parsed, parseErr := canonicalUTC(*session.StartedAt)
		if parseErr != nil || parsed.After(generatedAt) {
			return ErrMalformedSnapshot
		}
		startedAt = &parsed
		session.StartedAtUnixMS = unixMS(parsed)
	}
	if session.LastActivityAt != nil {
		parsed, parseErr := canonicalUTC(*session.LastActivityAt)
		if parseErr != nil || parsed.After(generatedAt) {
			return ErrMalformedSnapshot
		}
		lastActivityAt = &parsed
		session.LastActivityAtUnixMS = unixMS(parsed)
	}
	if startedAt != nil && lastActivityAt != nil && lastActivityAt.Before(*startedAt) {
		return ErrMalformedSnapshot
	}
	return nil
}

func validateWindow(document json.RawMessage, window *QuotaWindow, minor uint16) error {
	var object map[string]json.RawMessage
	if json.Unmarshal(document, &object) != nil || requireFields(object,
		[]string{"name", "used_basis_points", "remaining_basis_points", "window_minutes", "resets_at"},
		[]string{"name"}) != nil {
		return ErrMalformedSnapshot
	}
	if err := validateObjectFields(object, minor, stringSet(
		"name", "used_basis_points", "remaining_basis_points", "window_minutes", "resets_at",
	)); err != nil {
		return err
	}
	if !safeIdentifier(window.Name, 24) ||
		(window.UsedBasisPoints != nil && *window.UsedBasisPoints > 10_000) ||
		(window.RemainingBasisPoints != nil && *window.RemainingBasisPoints > 10_000) ||
		(window.WindowMinutes != nil && (*window.WindowMinutes == 0 || *window.WindowMinutes > 525_600)) {
		return ErrMalformedSnapshot
	}
	if window.UsedBasisPoints != nil && window.RemainingBasisPoints != nil &&
		uint32(*window.UsedBasisPoints)+uint32(*window.RemainingBasisPoints) != 10_000 {
		return ErrMalformedSnapshot
	}
	if window.ResetsAt != nil {
		parsed, err := canonicalUTC(*window.ResetsAt)
		if err != nil {
			return ErrMalformedSnapshot
		}
		window.ResetsAtUnixMS = unixMS(parsed)
	}
	return nil
}

func validateMoney(document json.RawMessage, money *Money, minor uint16) error {
	if isNull(document) {
		return nil
	}
	if money == nil {
		return ErrMalformedSnapshot
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(document, &object) != nil || requireFields(object,
		[]string{"amount_micros", "currency"}, []string{"amount_micros", "currency"}) != nil {
		return ErrMalformedSnapshot
	}
	if err := validateObjectFields(object, minor, stringSet("amount_micros", "currency")); err != nil {
		return err
	}
	if money.AmountMicros > maxSafeJSONInteger || len(money.Currency) != 3 {
		return ErrMalformedSnapshot
	}
	for _, char := range money.Currency {
		if char < 'A' || char > 'Z' {
			return ErrMalformedSnapshot
		}
	}
	return nil
}

func validateTokens(document json.RawMessage, tokens *TokenUsage, minor uint16) error {
	if isNull(document) {
		return nil
	}
	if tokens == nil {
		return ErrMalformedSnapshot
	}
	var object map[string]json.RawMessage
	fields := []string{"input", "cached_input", "output", "reasoning", "total"}
	if json.Unmarshal(document, &object) != nil || requireFields(object, fields, nil) != nil {
		return ErrMalformedSnapshot
	}
	if err := validateObjectFields(object, minor, stringSet(fields...)); err != nil {
		return err
	}
	for _, value := range []*uint64{tokens.Input, tokens.CachedInput, tokens.Output, tokens.Reasoning, tokens.Total} {
		if value != nil && *value > maxSafeJSONInteger {
			return ErrMalformedSnapshot
		}
	}
	if tokens.Input != nil && tokens.CachedInput != nil && *tokens.CachedInput > *tokens.Input {
		return ErrMalformedSnapshot
	}
	if tokens.Input != nil && tokens.Output != nil && tokens.Reasoning != nil && tokens.Total != nil &&
		*tokens.Total != *tokens.Input+*tokens.Output+*tokens.Reasoning {
		return ErrMalformedSnapshot
	}
	return nil
}

func validateProviderError(document json.RawMessage, providerError *ProviderError, minor uint16) error {
	if isNull(document) {
		return nil
	}
	if providerError == nil {
		return ErrMalformedSnapshot
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(document, &object) != nil || requireFields(object,
		[]string{"code", "retryable"}, []string{"code", "retryable"}) != nil {
		return ErrMalformedSnapshot
	}
	if err := validateObjectFields(object, minor, stringSet("code", "retryable")); err != nil {
		return err
	}
	switch providerError.Code {
	case ProviderErrorAuthStale, ProviderErrorPermissionDenied, ProviderErrorTimeout,
		ProviderErrorProcessExited, ProviderErrorSchemaChanged, ProviderErrorUnavailable:
		return nil
	default:
		return ErrMalformedSnapshot
	}
}

func decodeSchemaVersion(document json.RawMessage) (SchemaVersion, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(document, &object) != nil ||
		requireFields(object, []string{"major", "minor"}, []string{"major", "minor"}) != nil ||
		len(object) != 2 {
		return SchemaVersion{}, ErrMalformedSnapshot
	}
	var version SchemaVersion
	if json.Unmarshal(document, &version) != nil {
		return SchemaVersion{}, ErrMalformedSnapshot
	}
	if version.Major != SchemaMajor {
		return SchemaVersion{}, ErrUnsupportedVersion
	}
	return version, nil
}

func requireFields(object map[string]json.RawMessage, fields, nonNull []string) error {
	for _, name := range fields {
		if _, present := object[name]; !present {
			return ErrMalformedSnapshot
		}
	}
	for _, name := range nonNull {
		if isNull(object[name]) {
			return ErrMalformedSnapshot
		}
	}
	return nil
}

func validateObjectFields(object map[string]json.RawMessage, minor uint16, known map[string]struct{}) error {
	forwardFields := 0
	for name, value := range object {
		if _, recognized := known[name]; recognized {
			continue
		}
		if minor == SchemaMinor {
			return ErrMalformedSnapshot
		}
		if !safeIdentifier(name, 32) {
			return ErrMalformedSnapshot
		}
		forwardFields++
		if forwardFields > maxForwardFields {
			return ErrMalformedSnapshot
		}
		if !safeForwardScalar(value) {
			return ErrPrivateData
		}
	}
	return nil
}

func safeForwardScalar(document json.RawMessage) bool {
	trimmed := bytes.TrimSpace(document)
	if bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("true")) ||
		bytes.Equal(trimmed, []byte("false")) {
		return true
	}
	if len(trimmed) == 0 || trimmed[0] == '"' || trimmed[0] == '{' || trimmed[0] == '[' ||
		bytes.ContainsAny(trimmed, ".eE") {
		return false
	}
	if trimmed[0] == '-' {
		value, err := strconv.ParseInt(string(trimmed), 10, 64)
		return err == nil && value >= -int64(maxSafeJSONInteger)
	}
	value, err := strconv.ParseUint(string(trimmed), 10, 64)
	return err == nil && value <= maxSafeJSONInteger
}

func canonicalUTC(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || !strings.HasSuffix(value, "Z") || parsed.Location() != time.UTC ||
		parsed.Year() < 1970 || parsed.Format(time.RFC3339Nano) != value {
		return time.Time{}, fmt.Errorf("non-canonical UTC timestamp")
	}
	return parsed, nil
}

func unixMS(value time.Time) *int64 {
	result := value.UnixMilli()
	return &result
}

func stringSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func isNull(value json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(value), []byte("null")) }

func safeIdentifier(value string, maximum int) bool {
	if len(value) == 0 || len(value) > maximum || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for index := 1; index < len(value); index++ {
		char := value[index]
		if !((char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') ||
			char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func safeOpaqueID(value string, maximum int) bool {
	if len(value) < 8 || len(value) > maximum {
		return false
	}
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func safeText(value string, maximum int) bool {
	length := utf8.RuneCountInString(value)
	if !utf8.ValidString(value) || length == 0 || length > maximum {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func validProviderStatus(status ProviderStatus) bool {
	return status == ProviderOK || status == ProviderDegraded || status == ProviderUnavailable
}

func validProviderSource(source ProviderSource) bool {
	return source == ProviderSourceCodexAppServer || source == ProviderSourceCursorLocal ||
		source == ProviderSourceStructuredHTTP || source == ProviderSourceNone
}

func validConfidence(confidence Confidence) bool {
	return confidence == ConfidenceVerified || confidence == ConfidenceInferred ||
		confidence == ConfidenceUnavailable
}

func validSessionSource(source SessionSource) bool {
	return source == SessionSourceCodexAppServerOwned || source == SessionSourceProcessJSONL ||
		source == SessionSourceNone
}

func oneOfSessionState(value SessionState, allowed ...SessionState) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func validSessionState(state SessionState) bool {
	switch state {
	case SessionRunning, SessionWaitingApproval, SessionWaitingInput,
		SessionCompleted, SessionFailed, SessionRecent, SessionEnded,
		SessionUnknown, SessionUnavailable:
		return true
	default:
		return false
	}
}
