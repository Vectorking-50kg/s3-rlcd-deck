#pragma once

#include <stddef.h>
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

#ifdef __cplusplus
}
#endif
