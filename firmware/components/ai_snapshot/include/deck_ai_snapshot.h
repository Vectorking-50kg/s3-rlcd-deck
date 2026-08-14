#pragma once

#include <stddef.h>
#include <stdbool.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define DECK_AI_SNAPSHOT_SCHEMA_MAJOR 1U
#define DECK_AI_SNAPSHOT_SCHEMA_MINOR 0U
#define DECK_AI_SNAPSHOT_MAX_BYTES (16U * 1024U)
#define DECK_AI_SNAPSHOT_MAX_PROVIDERS 8U
#define DECK_AI_SNAPSHOT_MAX_SESSIONS 16U
#define DECK_AI_SNAPSHOT_MAX_WINDOWS 4U
#define DECK_AI_SNAPSHOT_DISPLAY_TEXT_CAPACITY (48U * 4U + 1U)
#define DECK_AI_SNAPSHOT_WINDOW_NAME_CAPACITY 25U
#define DECK_AI_SNAPSHOT_SESSION_ID_CAPACITY 65U
#define DECK_AI_SNAPSHOT_TIMEZONE_CAPACITY 65U

typedef enum {
    DECK_AI_SNAPSHOT_ACCEPTED = 0,
    DECK_AI_SNAPSHOT_MALFORMED,
    DECK_AI_SNAPSHOT_UNSUPPORTED_VERSION,
    DECK_AI_SNAPSHOT_PRIVATE_DATA,
} deck_ai_snapshot_result_t;

typedef struct {
    uint16_t schema_minor;
    uint64_t generated_at_unix_ms;
    uint8_t provider_count;
    uint8_t session_count;
    uint32_t next_refresh_seconds;
} deck_ai_snapshot_metadata_t;

typedef struct {
    char *document;
    size_t capacity;
    size_t document_size;
    deck_ai_snapshot_metadata_t metadata;
    bool has_snapshot;
} deck_ai_snapshot_retained_t;

typedef enum {
    DECK_AI_SNAPSHOT_PROVIDER_OK = 0,
    DECK_AI_SNAPSHOT_PROVIDER_DEGRADED,
    DECK_AI_SNAPSHOT_PROVIDER_UNAVAILABLE,
} deck_ai_snapshot_provider_status_t;

typedef enum {
    DECK_AI_SNAPSHOT_CONFIDENCE_VERIFIED = 0,
    DECK_AI_SNAPSHOT_CONFIDENCE_INFERRED,
    DECK_AI_SNAPSHOT_CONFIDENCE_UNAVAILABLE,
} deck_ai_snapshot_confidence_t;

typedef enum {
    DECK_AI_SNAPSHOT_SESSION_RUNNING = 0,
    DECK_AI_SNAPSHOT_SESSION_WAITING_APPROVAL,
    DECK_AI_SNAPSHOT_SESSION_WAITING_INPUT,
    DECK_AI_SNAPSHOT_SESSION_COMPLETED,
    DECK_AI_SNAPSHOT_SESSION_FAILED,
    DECK_AI_SNAPSHOT_SESSION_RECENT,
    DECK_AI_SNAPSHOT_SESSION_ENDED,
    DECK_AI_SNAPSHOT_SESSION_UNKNOWN,
    DECK_AI_SNAPSHOT_SESSION_UNAVAILABLE,
} deck_ai_snapshot_session_state_t;

typedef struct {
    char name[DECK_AI_SNAPSHOT_WINDOW_NAME_CAPACITY];
    bool has_used_basis_points;
    uint16_t used_basis_points;
    bool has_remaining_basis_points;
    uint16_t remaining_basis_points;
    bool has_window_minutes;
    uint32_t window_minutes;
    bool has_resets_at;
    uint64_t resets_at_unix_ms;
} deck_ai_snapshot_quota_projection_t;

typedef struct {
    bool present;
    char session_id[DECK_AI_SNAPSHOT_SESSION_ID_CAPACITY];
    bool has_display_name;
    char display_name[DECK_AI_SNAPSHOT_DISPLAY_TEXT_CAPACITY];
    deck_ai_snapshot_session_state_t state;
    deck_ai_snapshot_confidence_t confidence;
    bool has_last_activity_at;
    uint64_t last_activity_at_unix_ms;
    bool has_duration_seconds;
    uint32_t duration_seconds;
    bool has_turn_tokens;
    uint64_t turn_tokens;
    bool has_context_used_basis_points;
    uint16_t context_used_basis_points;
} deck_ai_snapshot_session_projection_t;

typedef struct {
    bool has_timezone;
    char timezone[DECK_AI_SNAPSHOT_TIMEZONE_CAPACITY];
    bool provider_present;
    char provider_display_name[DECK_AI_SNAPSHOT_DISPLAY_TEXT_CAPACITY];
    deck_ai_snapshot_provider_status_t provider_status;
    deck_ai_snapshot_confidence_t provider_confidence;
    bool has_provider_updated_at;
    uint64_t provider_updated_at_unix_ms;
    uint8_t window_count;
    deck_ai_snapshot_quota_projection_t windows[DECK_AI_SNAPSHOT_MAX_WINDOWS];
    bool has_total_tokens;
    uint64_t total_tokens;
    uint8_t session_count;
    deck_ai_snapshot_session_projection_t featured_session;
} deck_ai_snapshot_codex_projection_t;

/*
 * Validates the privacy-safe normalized wire contract without retaining the
 * document. Unknown major versions are rejected. A higher minor may add only
 * bounded integer, boolean, or null fields; strings and containers remain
 * fail-closed so future fields cannot smuggle private content.
 */
deck_ai_snapshot_result_t deck_ai_snapshot_validate(
    const char *document,
    size_t document_size,
    deck_ai_snapshot_metadata_t *metadata
);

/*
 * Projects a validated document into the bounded, display-safe Codex fields.
 * No raw JSON pointer escapes this call and missing numeric fields stay absent.
 */
bool deck_ai_snapshot_project_codex(
    const char *document,
    size_t document_size,
    deck_ai_snapshot_codex_projection_t *projection
);

/* Caller-owned storage keeps allocation and PSRAM policy outside the parser. */
bool deck_ai_snapshot_retained_init(
    deck_ai_snapshot_retained_t *retained,
    char *storage,
    size_t storage_capacity
);

/* Validation failure never changes the retained document or metadata. */
deck_ai_snapshot_result_t deck_ai_snapshot_retained_apply(
    deck_ai_snapshot_retained_t *retained,
    const char *document,
    size_t document_size
);

bool deck_ai_snapshot_retained_current(
    const deck_ai_snapshot_retained_t *retained,
    const char **document,
    size_t *document_size,
    deck_ai_snapshot_metadata_t *metadata
);

#ifdef __cplusplus
}
#endif
