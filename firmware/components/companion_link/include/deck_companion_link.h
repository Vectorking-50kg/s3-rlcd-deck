#pragma once

#include <stdbool.h>
#include <stdint.h>

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

typedef enum {
    DECK_COMPANION_LINK_ERROR_NONE = 0,
    DECK_COMPANION_LINK_ERROR_TRANSPORT,
    DECK_COMPANION_LINK_ERROR_TLS_PIN_MISMATCH,
    DECK_COMPANION_LINK_ERROR_AUTH_REJECTED,
    DECK_COMPANION_LINK_ERROR_PROTOCOL_MAJOR_REJECTED,
    DECK_COMPANION_LINK_ERROR_PROTOCOL_INVALID,
    DECK_COMPANION_LINK_ERROR_HEARTBEAT_TIMEOUT,
    DECK_COMPANION_LINK_ERROR_INTERNAL,
} deck_companion_link_error_t;

typedef struct {
    deck_companion_link_state_t state;
    bool has_active_profile;
    char active_profile_id[DECK_COMPANION_PROFILE_ID_CAPACITY];
    uint32_t profile_generation;
    uint32_t reconnect_attempts;
    uint32_t error_count;
    deck_companion_link_error_t last_error;
    uint32_t error_generation;
    uint64_t last_heartbeat_monotonic_ms;
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

void deck_companion_link_stop(deck_companion_link_t *link);

#ifdef __cplusplus
}
#endif
