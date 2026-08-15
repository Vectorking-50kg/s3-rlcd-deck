#pragma once

#include <stdbool.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef enum {
    DECK_COMPANION_TRANSPORT_HEARTBEAT = 0,
    DECK_COMPANION_TRANSPORT_AI_SNAPSHOT,
    DECK_COMPANION_TRANSPORT_SERIAL_CONTROL,
    DECK_COMPANION_TRANSPORT_SERIAL_BINARY,
} deck_companion_transport_message_t;

typedef struct {
    uint32_t generation;
    bool active_authority;
    bool snapshot_current;
} deck_companion_transport_authority_t;

/*
 * Starts a new authenticated transport in probe-only mode. Until the first
 * heartbeat is transactionally made Active, no retained data or Serial
 * control is authoritative. A new generation also makes the old Snapshot
 * stale until this exact transport publishes one.
 */
bool deck_companion_transport_begin(
    deck_companion_transport_authority_t *authority,
    uint32_t generation
);
bool deck_companion_transport_allows(
    const deck_companion_transport_authority_t *authority,
    uint32_t generation,
    deck_companion_transport_message_t message
);
bool deck_companion_transport_activate(
    deck_companion_transport_authority_t *authority,
    uint32_t generation
);
bool deck_companion_transport_accept_snapshot(
    deck_companion_transport_authority_t *authority,
    uint32_t generation
);
bool deck_companion_transport_snapshot_current(
    const deck_companion_transport_authority_t *authority,
    uint32_t generation
);
void deck_companion_transport_invalidate(
    deck_companion_transport_authority_t *authority
);

#ifdef __cplusplus
}
#endif
