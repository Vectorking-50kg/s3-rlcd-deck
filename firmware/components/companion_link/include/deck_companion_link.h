#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "deck_ai_snapshot_store.h"
#include "deck_companion_profiles.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef struct deck_companion_link deck_companion_link_t;

typedef enum {
    DECK_COMPANION_LINK_UNPAIRED = 0,
    DECK_COMPANION_LINK_OFFLINE,
    DECK_COMPANION_LINK_CONNECTING,
    DECK_COMPANION_LINK_ONLINE,
} deck_companion_link_state_t;

typedef struct {
    deck_companion_link_state_t state;
    bool has_active_profile;
    char active_profile_id[DECK_COMPANION_PROFILE_ID_CAPACITY];
    uint32_t profile_generation;
    uint32_t reconnect_attempts;
    uint32_t error_count;
    uint64_t last_heartbeat_monotonic_ms;
    bool has_trusted_utc;
    uint64_t trusted_utc_ms;
} deck_companion_link_snapshot_t;

/*
 * Starts the lifetime Device Link owner. The Profiles object is borrowed and
 * must outlive the returned Link. All credentials stay inside this module.
 */
deck_companion_link_t *deck_companion_link_start(
    deck_companion_profiles_t *profiles,
    const char *firmware_version
);

bool deck_companion_link_snapshot(
    const deck_companion_link_t *link,
    deck_companion_link_snapshot_t *snapshot
);

bool deck_companion_link_copy_ai_snapshot(
    const deck_companion_link_t *link,
    uint64_t now_utc_ms,
    char *document,
    size_t document_capacity,
    size_t *document_size,
    deck_ai_snapshot_store_snapshot_t *snapshot
);

/*
 * Bounded, idempotent shutdown. False retains the complete Link/Store/NVS
 * owner so the caller can retry after a stalled storage driver recovers.
 */
bool deck_companion_link_stop(deck_companion_link_t *link);

#ifdef __cplusplus
}
#endif
