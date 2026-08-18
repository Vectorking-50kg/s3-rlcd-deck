#pragma once

#include <stdbool.h>
#include <stdint.h>

#include "deck_companion_profiles.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef enum {
    DECK_COMPANION_FAILOVER_PROFILES_OBSERVED = 0,
    DECK_COMPANION_FAILOVER_TRANSPORT_FAILED,
    DECK_COMPANION_FAILOVER_ONLINE,
    DECK_COMPANION_FAILOVER_TICK,
} deck_companion_failover_event_t;

typedef enum {
    DECK_COMPANION_FAILOVER_NONE = 0,
    DECK_COMPANION_FAILOVER_CONNECT,
} deck_companion_failover_action_kind_t;

typedef struct {
    deck_companion_failover_action_kind_t kind;
    uint32_t profile_generation;
    char profile_id[DECK_COMPANION_PROFILE_ID_CAPACITY];
} deck_companion_failover_action_t;

typedef struct {
    bool initialized;
    bool online;
    bool offline;
    bool round_active;
    uint8_t attempted_mask;
    uint32_t profile_generation;
    uint64_t offline_since_ms;
    char stored_active_profile_id[DECK_COMPANION_PROFILE_ID_CAPACITY];
    char target_profile_id[DECK_COMPANION_PROFILE_ID_CAPACITY];
} deck_companion_failover_t;

typedef enum {
    DECK_COMPANION_FAILOVER_TARGET_INVALID = 0,
    DECK_COMPANION_FAILOVER_TARGET_STALE_GENERATION,
    DECK_COMPANION_FAILOVER_TARGET_ACTIVE,
    DECK_COMPANION_FAILOVER_TARGET_CANDIDATE,
} deck_companion_failover_target_t;

/*
 * Classifies the generation-fenced transport target at its first heartbeat.
 * Reconnecting the already Active Profile must not create a new transaction;
 * only a different candidate needs activation.
 */
deck_companion_failover_target_t deck_companion_failover_classify_target(
    const deck_companion_profiles_snapshot_t *profiles,
    const char *profile_id,
    uint32_t expected_generation
);

/*
 * Advances the sticky failover policy using one monotonic event. Profile order
 * is the stable tie-break after descending priority and last-success.
 */
bool deck_companion_failover_advance(
    deck_companion_failover_t *failover,
    const deck_companion_profiles_snapshot_t *profiles,
    uint64_t now_ms,
    deck_companion_failover_event_t event,
    deck_companion_failover_action_t *action
);

#ifdef __cplusplus
}
#endif
