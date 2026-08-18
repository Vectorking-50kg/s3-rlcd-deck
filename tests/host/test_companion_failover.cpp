#include "deck_companion_failover.h"
#include "deck_companion_transport_authority.h"

#include <cassert>
#include <cstring>

namespace {

void copy_id(char *output, char value)
{
    std::memcpy(output, "sha256:", 7);
    std::memset(output + 7, value, 64);
    output[71] = '\0';
}

deck_companion_profiles_snapshot_t profiles()
{
    deck_companion_profiles_snapshot_t value{};
    value.generation = 7;
    value.has_active = true;
    value.count = 3;
    copy_id(value.profiles[0].profile_id, 'a');
    value.profiles[0].priority = 10;
    copy_id(value.profiles[1].profile_id, 'b');
    value.profiles[1].priority = 30;
    copy_id(value.profiles[2].profile_id, 'c');
    value.profiles[2].priority = 20;
    std::memcpy(
        value.active_profile_id,
        value.profiles[0].profile_id,
        sizeof(value.active_profile_id)
    );
    return value;
}

void flapping_active_requires_one_continuous_offline_window()
{
    deck_companion_failover_t failover{};
    deck_companion_failover_action_t action{};
    const deck_companion_profiles_snapshot_t current = profiles();

    assert(deck_companion_failover_advance(
        &failover,
        &current,
        1'000,
        DECK_COMPANION_FAILOVER_PROFILES_OBSERVED,
        &action
    ));
    assert(action.kind == DECK_COMPANION_FAILOVER_CONNECT);
    assert(std::strcmp(action.profile_id, current.active_profile_id) == 0);

    assert(deck_companion_failover_advance(
        &failover,
        &current,
        1'001,
        DECK_COMPANION_FAILOVER_TRANSPORT_FAILED,
        &action
    ));
    assert(action.kind == DECK_COMPANION_FAILOVER_NONE);
    assert(deck_companion_failover_advance(
        &failover,
        &current,
        30'999,
        DECK_COMPANION_FAILOVER_TICK,
        &action
    ));
    assert(action.kind == DECK_COMPANION_FAILOVER_NONE);

    assert(deck_companion_failover_advance(
        &failover,
        &current,
        31'000,
        DECK_COMPANION_FAILOVER_ONLINE,
        &action
    ));
    assert(action.kind == DECK_COMPANION_FAILOVER_NONE);
    assert(deck_companion_failover_advance(
        &failover,
        &current,
        40'000,
        DECK_COMPANION_FAILOVER_TRANSPORT_FAILED,
        &action
    ));
    assert(deck_companion_failover_advance(
        &failover,
        &current,
        69'999,
        DECK_COMPANION_FAILOVER_TICK,
        &action
    ));
    assert(action.kind == DECK_COMPANION_FAILOVER_NONE);
    assert(deck_companion_failover_advance(
        &failover,
        &current,
        70'000,
        DECK_COMPANION_FAILOVER_TICK,
        &action
    ));
    assert(action.kind == DECK_COMPANION_FAILOVER_CONNECT);
    assert(std::strcmp(action.profile_id, current.profiles[1].profile_id) == 0);
    assert(action.profile_generation == current.generation);
}

void first_heartbeat_only_activates_a_generation_fenced_candidate()
{
    deck_companion_profiles_snapshot_t current = profiles();
    assert(deck_companion_failover_classify_target(
               &current,
               current.active_profile_id,
               current.generation
           ) == DECK_COMPANION_FAILOVER_TARGET_ACTIVE);
    assert(deck_companion_failover_classify_target(
               &current,
               current.profiles[1].profile_id,
               current.generation
           ) == DECK_COMPANION_FAILOVER_TARGET_CANDIDATE);
    assert(deck_companion_failover_classify_target(
               &current,
               current.profiles[1].profile_id,
               current.generation - 1
           ) == DECK_COMPANION_FAILOVER_TARGET_STALE_GENERATION);
    assert(deck_companion_failover_classify_target(
               &current,
               "sha256:missing",
               current.generation
           ) == DECK_COMPANION_FAILOVER_TARGET_INVALID);
}

void all_offline_rotates_once_then_returns_to_the_sticky_active()
{
    deck_companion_failover_t failover{};
    deck_companion_failover_action_t action{};
    const deck_companion_profiles_snapshot_t current = profiles();

    assert(deck_companion_failover_advance(
        &failover,
        &current,
        0,
        DECK_COMPANION_FAILOVER_PROFILES_OBSERVED,
        &action
    ));
    assert(deck_companion_failover_advance(
        &failover,
        &current,
        30'000,
        DECK_COMPANION_FAILOVER_TICK,
        &action
    ));
    assert(action.kind == DECK_COMPANION_FAILOVER_CONNECT);
    assert(std::strcmp(action.profile_id, current.profiles[1].profile_id) == 0);

    assert(deck_companion_failover_advance(
        &failover,
        &current,
        31'000,
        DECK_COMPANION_FAILOVER_TRANSPORT_FAILED,
        &action
    ));
    assert(action.kind == DECK_COMPANION_FAILOVER_CONNECT);
    assert(std::strcmp(action.profile_id, current.profiles[2].profile_id) == 0);

    assert(deck_companion_failover_advance(
        &failover,
        &current,
        32'000,
        DECK_COMPANION_FAILOVER_TRANSPORT_FAILED,
        &action
    ));
    assert(action.kind == DECK_COMPANION_FAILOVER_CONNECT);
    assert(std::strcmp(action.profile_id, current.active_profile_id) == 0);

    assert(deck_companion_failover_advance(
        &failover,
        &current,
        61'999,
        DECK_COMPANION_FAILOVER_TICK,
        &action
    ));
    assert(action.kind == DECK_COMPANION_FAILOVER_NONE);
    assert(deck_companion_failover_advance(
        &failover,
        &current,
        62'000,
        DECK_COMPANION_FAILOVER_TICK,
        &action
    ));
    assert(action.kind == DECK_COMPANION_FAILOVER_CONNECT);
    assert(std::strcmp(action.profile_id, current.profiles[1].profile_id) == 0);
}

void successful_candidate_stays_sticky_but_manual_selection_wins()
{
    deck_companion_failover_t failover{};
    deck_companion_failover_action_t action{};
    deck_companion_profiles_snapshot_t current = profiles();
    assert(deck_companion_failover_advance(
        &failover,
        &current,
        0,
        DECK_COMPANION_FAILOVER_PROFILES_OBSERVED,
        &action
    ));
    assert(deck_companion_failover_advance(
        &failover,
        &current,
        30'000,
        DECK_COMPANION_FAILOVER_TICK,
        &action
    ));
    assert(std::strcmp(action.profile_id, current.profiles[1].profile_id) == 0);

    current.generation = 8;
    std::memcpy(
        current.active_profile_id,
        current.profiles[1].profile_id,
        sizeof(current.active_profile_id)
    );
    assert(deck_companion_failover_advance(
        &failover,
        &current,
        31'000,
        DECK_COMPANION_FAILOVER_ONLINE,
        &action
    ));
    assert(action.kind == DECK_COMPANION_FAILOVER_NONE);
    assert(deck_companion_failover_advance(
        &failover,
        &current,
        600'000,
        DECK_COMPANION_FAILOVER_TICK,
        &action
    ));
    assert(action.kind == DECK_COMPANION_FAILOVER_NONE);

    current.generation = 9;
    std::memcpy(
        current.active_profile_id,
        current.profiles[2].profile_id,
        sizeof(current.active_profile_id)
    );
    assert(deck_companion_failover_advance(
        &failover,
        &current,
        600'001,
        DECK_COMPANION_FAILOVER_PROFILES_OBSERVED,
        &action
    ));
    assert(action.kind == DECK_COMPANION_FAILOVER_CONNECT);
    assert(std::strcmp(action.profile_id, current.profiles[2].profile_id) == 0);

    deck_companion_failover_t restarted{};
    assert(deck_companion_failover_advance(
        &restarted,
        &current,
        0,
        DECK_COMPANION_FAILOVER_PROFILES_OBSERVED,
        &action
    ));
    assert(action.kind == DECK_COMPANION_FAILOVER_CONNECT);
    assert(std::strcmp(action.profile_id, current.profiles[2].profile_id) == 0);
}

void generation_changes_cancel_the_old_round_and_ties_are_stable()
{
    deck_companion_failover_t failover{};
    deck_companion_failover_action_t action{};
    deck_companion_profiles_snapshot_t current = profiles();
    current.profiles[1].priority = 20;
    current.profiles[2].priority = 20;
    current.profiles[1].last_success_unix_ms = 900;
    current.profiles[2].last_success_unix_ms = 900;
    assert(deck_companion_failover_advance(
        &failover,
        &current,
        0,
        DECK_COMPANION_FAILOVER_PROFILES_OBSERVED,
        &action
    ));
    assert(deck_companion_failover_advance(
        &failover,
        &current,
        30'000,
        DECK_COMPANION_FAILOVER_TICK,
        &action
    ));
    assert(std::strcmp(action.profile_id, current.profiles[1].profile_id) == 0);

    // A revoke/address/manual transaction changes generation and cancels the
    // in-flight target before a delayed transport result can act on it.
    current.generation = 8;
    current.count = 2;
    current.profiles[1] = current.profiles[2];
    std::memcpy(
        current.active_profile_id,
        current.profiles[1].profile_id,
        sizeof(current.active_profile_id)
    );
    assert(deck_companion_failover_advance(
        &failover,
        &current,
        31'000,
        DECK_COMPANION_FAILOVER_TRANSPORT_FAILED,
        &action
    ));
    assert(action.kind == DECK_COMPANION_FAILOVER_CONNECT);
    assert(action.profile_generation == 8);
    assert(std::strcmp(action.profile_id, current.active_profile_id) == 0);
}

void manual_selection_after_candidate_commit_replaces_the_candidate_transport()
{
    deck_companion_failover_t failover{};
    deck_companion_failover_action_t action{};
    deck_companion_profiles_snapshot_t current = profiles();
    assert(deck_companion_failover_advance(
        &failover,
        &current,
        0,
        DECK_COMPANION_FAILOVER_PROFILES_OBSERVED,
        &action
    ));
    assert(deck_companion_failover_advance(
        &failover,
        &current,
        30'000,
        DECK_COMPANION_FAILOVER_TICK,
        &action
    ));
    assert(std::strcmp(action.profile_id, current.profiles[1].profile_id) == 0);

    // The candidate commit completed, then a manual selection completed
    // before Link read the new snapshot. The later generation is authoritative.
    current.generation = 9;
    std::memcpy(
        current.active_profile_id,
        current.profiles[2].profile_id,
        sizeof(current.active_profile_id)
    );
    assert(deck_companion_failover_advance(
        &failover,
        &current,
        31'000,
        DECK_COMPANION_FAILOVER_ONLINE,
        &action
    ));
    assert(action.kind == DECK_COMPANION_FAILOVER_CONNECT);
    assert(std::strcmp(action.profile_id, current.profiles[2].profile_id) == 0);
}

void one_profile_never_restarts_its_transport_at_the_failover_boundary()
{
    deck_companion_failover_t failover{};
    deck_companion_failover_action_t action{};
    deck_companion_profiles_snapshot_t current = profiles();
    current.count = 1;
    assert(deck_companion_failover_advance(
        &failover,
        &current,
        0,
        DECK_COMPANION_FAILOVER_PROFILES_OBSERVED,
        &action
    ));
    assert(deck_companion_failover_advance(
        &failover,
        &current,
        30'000,
        DECK_COMPANION_FAILOVER_TICK,
        &action
    ));
    assert(action.kind == DECK_COMPANION_FAILOVER_NONE);
    assert(deck_companion_failover_advance(
        &failover,
        &current,
        60'000,
        DECK_COMPANION_FAILOVER_TICK,
        &action
    ));
    assert(action.kind == DECK_COMPANION_FAILOVER_NONE);
}

void fake_transport_uses_the_production_authority_gate()
{
    deck_companion_transport_authority_t transport{};
    assert(deck_companion_transport_begin(&transport, 7));
    assert(deck_companion_transport_allows(
        &transport, 7, DECK_COMPANION_TRANSPORT_HEARTBEAT
    ));
    assert(!deck_companion_transport_allows(
        &transport, 7, DECK_COMPANION_TRANSPORT_AI_SNAPSHOT
    ));
    assert(!deck_companion_transport_allows(
        &transport, 7, DECK_COMPANION_TRANSPORT_SERIAL_CONTROL
    ));
    assert(!deck_companion_transport_allows(
        &transport, 7, DECK_COMPANION_TRANSPORT_SERIAL_BINARY
    ));

    assert(deck_companion_transport_activate(&transport, 7));
    assert(deck_companion_transport_allows(
        &transport, 7, DECK_COMPANION_TRANSPORT_AI_SNAPSHOT
    ));
    assert(!deck_companion_transport_snapshot_current(&transport, 7));
    assert(deck_companion_transport_accept_snapshot(&transport, 7));
    assert(deck_companion_transport_snapshot_current(&transport, 7));

    assert(deck_companion_transport_begin(&transport, 8));
    assert(!deck_companion_transport_allows(
        &transport, 7, DECK_COMPANION_TRANSPORT_HEARTBEAT
    ));
    assert(!deck_companion_transport_snapshot_current(&transport, 8));
    assert(!deck_companion_transport_accept_snapshot(&transport, 7));
}

void fake_clock_and_transport_follow_manual_generation_changes()
{
    deck_companion_profiles_snapshot_t current = profiles();
    current.count = 2;
    deck_companion_failover_t failover{};
    deck_companion_failover_action_t action{};
    deck_companion_transport_authority_t transport{};

    assert(deck_companion_failover_advance(
        &failover,
        &current,
        0,
        DECK_COMPANION_FAILOVER_PROFILES_OBSERVED,
        &action
    ));
    assert(action.kind == DECK_COMPANION_FAILOVER_CONNECT);
    assert(std::strcmp(action.profile_id, current.active_profile_id) == 0);
    assert(deck_companion_transport_begin(&transport, 10));
    assert(deck_companion_transport_activate(&transport, 10));
    assert(deck_companion_transport_accept_snapshot(&transport, 10));

    // A manual select is a new Profile transaction. The production policy
    // chooses that exact profile and the production transport gate makes the
    // previous source stale before the replacement can publish anything.
    current.generation += 1;
    std::memcpy(
        current.active_profile_id,
        current.profiles[1].profile_id,
        sizeof(current.active_profile_id)
    );
    assert(deck_companion_failover_advance(
        &failover,
        &current,
        1,
        DECK_COMPANION_FAILOVER_PROFILES_OBSERVED,
        &action
    ));
    assert(action.kind == DECK_COMPANION_FAILOVER_CONNECT);
    assert(std::strcmp(action.profile_id, current.profiles[1].profile_id) == 0);
    assert(deck_companion_transport_begin(&transport, 11));
    assert(!deck_companion_transport_snapshot_current(&transport, 11));
    assert(!deck_companion_transport_allows(
        &transport, 11, DECK_COMPANION_TRANSPORT_SERIAL_CONTROL
    ));
    assert(!deck_companion_transport_allows(
        &transport, 10, DECK_COMPANION_TRANSPORT_HEARTBEAT
    ));

    // Revoking the selected Profile is another generation change and cannot
    // revive queued events from either older transport.
    current.generation += 1;
    current.count = 1;
    std::memcpy(
        current.active_profile_id,
        current.profiles[0].profile_id,
        sizeof(current.active_profile_id)
    );
    assert(deck_companion_failover_advance(
        &failover,
        &current,
        2,
        DECK_COMPANION_FAILOVER_PROFILES_OBSERVED,
        &action
    ));
    assert(action.kind == DECK_COMPANION_FAILOVER_CONNECT);
    assert(std::strcmp(action.profile_id, current.profiles[0].profile_id) == 0);
}

}  // namespace

int main()
{
    first_heartbeat_only_activates_a_generation_fenced_candidate();
    flapping_active_requires_one_continuous_offline_window();
    all_offline_rotates_once_then_returns_to_the_sticky_active();
    successful_candidate_stays_sticky_but_manual_selection_wins();
    generation_changes_cancel_the_old_round_and_ties_are_stable();
    manual_selection_after_candidate_commit_replaces_the_candidate_transport();
    one_profile_never_restarts_its_transport_at_the_failover_boundary();
    fake_transport_uses_the_production_authority_gate();
    fake_clock_and_transport_follow_manual_generation_changes();
    return 0;
}
