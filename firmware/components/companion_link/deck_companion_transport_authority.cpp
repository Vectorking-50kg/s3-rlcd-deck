#include "deck_companion_transport_authority.h"

bool deck_companion_transport_begin(
    deck_companion_transport_authority_t *authority,
    uint32_t generation
)
{
    if (authority == nullptr || generation == 0) {
        return false;
    }
    *authority = {generation, false, false};
    return true;
}

bool deck_companion_transport_allows(
    const deck_companion_transport_authority_t *authority,
    uint32_t generation,
    deck_companion_transport_message_t message
)
{
    if (authority == nullptr || generation == 0 ||
        generation != authority->generation) {
        return false;
    }
    return message == DECK_COMPANION_TRANSPORT_HEARTBEAT ||
           authority->active_authority;
}

bool deck_companion_transport_activate(
    deck_companion_transport_authority_t *authority,
    uint32_t generation
)
{
    if (authority == nullptr || generation == 0 ||
        generation != authority->generation) {
        return false;
    }
    authority->active_authority = true;
    return true;
}

bool deck_companion_transport_accept_snapshot(
    deck_companion_transport_authority_t *authority,
    uint32_t generation
)
{
    if (authority == nullptr || generation == 0 ||
        generation != authority->generation || !authority->active_authority) {
        return false;
    }
    authority->snapshot_current = true;
    return true;
}

bool deck_companion_transport_snapshot_current(
    const deck_companion_transport_authority_t *authority,
    uint32_t generation
)
{
    return authority != nullptr && generation != 0 &&
           generation == authority->generation && authority->active_authority &&
           authority->snapshot_current;
}

void deck_companion_transport_invalidate(
    deck_companion_transport_authority_t *authority
)
{
    if (authority != nullptr) {
        *authority = {};
    }
}
