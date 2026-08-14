#include "deck_companion_link_message.h"

deck_companion_server_message_result_t deck_companion_link_accept_server_message(
    deck_ai_snapshot_store_t *snapshots,
    const char *message,
    size_t message_size,
    uint64_t trusted_utc_ms,
    uint64_t previous_server_monotonic_ms,
    bool has_previous_server_monotonic,
    deck_device_heartbeat_t *heartbeat
)
{
    if (snapshots == nullptr || message == nullptr || message_size == 0 ||
        heartbeat == nullptr) {
        return DECK_COMPANION_SERVER_INVALID_MESSAGE;
    }
    if (deck_device_protocol_parse_heartbeat(
            message,
            message_size,
            previous_server_monotonic_ms,
            has_previous_server_monotonic,
            heartbeat
        )) {
        return DECK_COMPANION_SERVER_HEARTBEAT;
    }
    const deck_ai_snapshot_store_update_result_t update =
        deck_ai_snapshot_store_apply(
            snapshots,
            message,
            message_size,
            trusted_utc_ms
        );
    if (update == DECK_AI_SNAPSHOT_STORE_ACCEPTED_STORAGE_FAILURE) {
        return DECK_COMPANION_SERVER_AI_SNAPSHOT_STORAGE_DEGRADED;
    }
    if (update == DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY ||
        update == DECK_AI_SNAPSHOT_STORE_ACCEPTED_PERSISTED ||
        update == DECK_AI_SNAPSHOT_STORE_UNCHANGED) {
        return DECK_COMPANION_SERVER_AI_SNAPSHOT;
    }
    return DECK_COMPANION_SERVER_INVALID_MESSAGE;
}
