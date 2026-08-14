#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "deck_ai_snapshot_store.h"
#include "deck_device_protocol.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef enum {
    DECK_COMPANION_SERVER_HEARTBEAT = 0,
    DECK_COMPANION_SERVER_AI_SNAPSHOT,
    DECK_COMPANION_SERVER_AI_SNAPSHOT_STORAGE_DEGRADED,
    DECK_COMPANION_SERVER_INVALID_MESSAGE,
} deck_companion_server_message_result_t;

/*
 * The one production dispatch seam for authenticated server text messages.
 * A Snapshot result is returned only after the Store accepts the full shared
 * contract; malformed input never reaches retained state.
 */
deck_companion_server_message_result_t deck_companion_link_accept_server_message(
    deck_ai_snapshot_store_t *snapshots,
    const char *message,
    size_t message_size,
    uint64_t trusted_utc_ms,
    uint64_t previous_server_monotonic_ms,
    bool has_previous_server_monotonic,
    deck_device_heartbeat_t *heartbeat
);

#ifdef __cplusplus
}
#endif
