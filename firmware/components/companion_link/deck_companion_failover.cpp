#include "deck_companion_failover.h"

#include <cstring>

namespace {

constexpr uint64_t kOfflineWindowMs = 30'000;

bool copy_id(char *output, const char *input)
{
    if (output == nullptr || input == nullptr) {
        return false;
    }
    const size_t size = std::strlen(input);
    if (size >= DECK_COMPANION_PROFILE_ID_CAPACITY) {
        return false;
    }
    std::memcpy(output, input, size + 1);
    return true;
}

void clear_action(deck_companion_failover_action_t *action)
{
    *action = {};
    action->kind = DECK_COMPANION_FAILOVER_NONE;
}

bool connect_to(
    deck_companion_failover_t *failover,
    const deck_companion_profiles_snapshot_t *profiles,
    const char *profile_id,
    deck_companion_failover_action_t *action
)
{
    if (!copy_id(failover->target_profile_id, profile_id) ||
        !copy_id(action->profile_id, profile_id)) {
        return false;
    }
    action->kind = DECK_COMPANION_FAILOVER_CONNECT;
    action->profile_generation = profiles->generation;
    failover->online = false;
    return true;
}

int profile_index(
    const deck_companion_profiles_snapshot_t *profiles,
    const char *profile_id
)
{
    for (size_t index = 0; index < profiles->count; ++index) {
        if (std::strcmp(profiles->profiles[index].profile_id, profile_id) == 0) {
            return static_cast<int>(index);
        }
    }
    return -1;
}

int best_unattempted(const deck_companion_failover_t *failover,
                     const deck_companion_profiles_snapshot_t *profiles)
{
    int best = -1;
    for (size_t index = 0; index < profiles->count; ++index) {
        if ((failover->attempted_mask & (1U << index)) != 0U ||
            std::strcmp(
                profiles->profiles[index].profile_id,
                failover->stored_active_profile_id
            ) == 0) {
            continue;
        }
        if (best < 0 ||
            profiles->profiles[index].priority >
                profiles->profiles[static_cast<size_t>(best)].priority ||
            (profiles->profiles[index].priority ==
                 profiles->profiles[static_cast<size_t>(best)].priority &&
             profiles->profiles[index].last_success_unix_ms >
                 profiles->profiles[static_cast<size_t>(best)].last_success_unix_ms)) {
            best = static_cast<int>(index);
        }
    }
    return best;
}

bool advance_round(
    deck_companion_failover_t *failover,
    const deck_companion_profiles_snapshot_t *profiles,
    uint64_t now_ms,
    deck_companion_failover_action_t *action
)
{
    const int next = best_unattempted(failover, profiles);
    if (next >= 0) {
        failover->attempted_mask |= static_cast<uint8_t>(1U << next);
        return connect_to(
            failover,
            profiles,
            profiles->profiles[static_cast<size_t>(next)].profile_id,
            action
        );
    }
    const bool returning_from_candidate = std::strcmp(
        failover->target_profile_id,
        failover->stored_active_profile_id
    ) != 0;
    failover->round_active = false;
    failover->attempted_mask = 0;
    failover->offline_since_ms = now_ms;
    if (!returning_from_candidate) {
        return true;
    }
    return connect_to(
        failover,
        profiles,
        failover->stored_active_profile_id,
        action
    );
}

bool profiles_changed(
    deck_companion_failover_t *failover,
    const deck_companion_profiles_snapshot_t *profiles,
    uint64_t now_ms,
    deck_companion_failover_event_t event,
    deck_companion_failover_action_t *action
)
{
    const bool accepted_target_became_active =
        event == DECK_COMPANION_FAILOVER_ONLINE && profiles->has_active &&
        failover->target_profile_id[0] != '\0' &&
        std::strcmp(
            failover->target_profile_id,
            profiles->active_profile_id
        ) == 0;
    failover->initialized = true;
    failover->profile_generation = profiles->generation;
    failover->online = accepted_target_became_active;
    failover->offline = profiles->has_active && !accepted_target_became_active;
    failover->round_active = false;
    failover->attempted_mask = 0;
    failover->offline_since_ms = now_ms;
    failover->stored_active_profile_id[0] = '\0';
    failover->target_profile_id[0] = '\0';
    if (!profiles->has_active || profiles->count == 0) {
        return true;
    }
    if (!copy_id(
            failover->stored_active_profile_id,
            profiles->active_profile_id
        )) {
        return false;
    }
    if (accepted_target_became_active) {
        return copy_id(failover->target_profile_id, profiles->active_profile_id);
    }
    return connect_to(
               failover,
               profiles,
               profiles->active_profile_id,
               action
           );
}

}  // namespace

bool deck_companion_failover_advance(
    deck_companion_failover_t *failover,
    const deck_companion_profiles_snapshot_t *profiles,
    uint64_t now_ms,
    deck_companion_failover_event_t event,
    deck_companion_failover_action_t *action
)
{
    if (failover == nullptr || profiles == nullptr || action == nullptr ||
        profiles->count > DECK_COMPANION_MAX_PROFILES) {
        return false;
    }
    clear_action(action);
    if (!failover->initialized ||
        failover->profile_generation != profiles->generation) {
        return profiles_changed(failover, profiles, now_ms, event, action);
    }
    if (event == DECK_COMPANION_FAILOVER_ONLINE) {
        failover->online = true;
        failover->offline = false;
        failover->round_active = false;
        failover->attempted_mask = 0;
        return true;
    }
    if (event == DECK_COMPANION_FAILOVER_TRANSPORT_FAILED) {
        failover->online = false;
        if (!failover->offline) {
            failover->offline = true;
            failover->offline_since_ms = now_ms;
        }
        if (failover->round_active) {
            return advance_round(failover, profiles, now_ms, action);
        }
        return true;
    }
    if (event != DECK_COMPANION_FAILOVER_TICK || !failover->offline ||
        failover->round_active || now_ms < failover->offline_since_ms ||
        now_ms - failover->offline_since_ms < kOfflineWindowMs) {
        return true;
    }

    const int active = profile_index(profiles, failover->stored_active_profile_id);
    if (active >= 0) {
        failover->attempted_mask |= static_cast<uint8_t>(1U << active);
    }
    failover->round_active = true;
    return advance_round(failover, profiles, now_ms, action);
}
