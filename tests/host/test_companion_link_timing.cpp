#include "deck_companion_link_timing.h"

#include <cassert>
#include <cstdint>

namespace {

void reconnect_starts_with_a_fresh_server_heartbeat_window()
{
    deck_companion_link_timing_t timing{};
    deck_companion_link_timing_server_heartbeat(&timing, 1'000, 10'000);
    assert(deck_companion_link_timing_server_expired(&timing, 31'000, 30'000));

    deck_companion_link_timing_begin_connection(&timing, 30'000, 30'000);

    assert(!deck_companion_link_timing_server_expired(&timing, 59'999, 30'000));
    assert(deck_companion_link_timing_server_expired(&timing, 60'000, 30'000));
    assert(!deck_companion_link_timing_client_due(&timing, 60'000));
}

void first_server_heartbeat_has_the_same_liveness_deadline()
{
    deck_companion_link_timing_t timing{};
    deck_companion_link_timing_begin_connection(&timing, 5'000, 30'000);

    assert(!deck_companion_link_timing_server_expired(&timing, 34'999, 30'000));
    assert(deck_companion_link_timing_server_expired(&timing, 35'000, 30'000));
}

void accepted_heartbeat_schedules_client_heartbeat_and_bounds_server_liveness()
{
    deck_companion_link_timing_t timing{};
    deck_companion_link_timing_server_heartbeat(&timing, 5'000, 10'000);

    assert(!deck_companion_link_timing_client_due(&timing, 14'999));
    assert(deck_companion_link_timing_client_due(&timing, 15'000));
    assert(!deck_companion_link_timing_server_expired(&timing, 34'999, 30'000));
    assert(deck_companion_link_timing_server_expired(&timing, 35'000, 30'000));
}

void retry_delay_is_exponential_and_bounded()
{
    assert(deck_companion_link_retry_delay_ms(1) == 1'000);
    assert(deck_companion_link_retry_delay_ms(2) == 2'000);
    assert(deck_companion_link_retry_delay_ms(6) == 30'000);
    assert(deck_companion_link_retry_delay_ms(UINT32_MAX) == 30'000);
}

void trusted_utc_never_moves_backward_and_latches_real_rollback()
{
    deck_companion_trusted_clock_t clock{};
    assert(deck_companion_trusted_clock_accept(&clock, 1'000'000, 100));
    uint64_t utc = 0;
    assert(deck_companion_trusted_clock_current(&clock, 200, &utc));
    assert(utc == 1'000'100U);

    // Network jitter may make a later sample trail the extrapolated clock,
    // but a non-decreasing server sample is clamped without moving backward.
    assert(deck_companion_trusted_clock_accept(&clock, 1'000'090, 200));
    assert(deck_companion_trusted_clock_current(&clock, 200, &utc));
    assert(utc == 1'000'100U);

    assert(!deck_companion_trusted_clock_accept(&clock, 999'000, 300));
    assert(!deck_companion_trusted_clock_current(&clock, 300, &utc));
    assert(clock.rollback_latched);

    assert(!deck_companion_trusted_clock_accept(&clock, 1'000'199, 400));
    assert(deck_companion_trusted_clock_accept(&clock, 1'000'200, 500));
    assert(deck_companion_trusted_clock_current(&clock, 500, &utc));
    assert(utc == 1'000'200U);
    assert(!clock.rollback_latched);
}

}  // namespace

int main()
{
    reconnect_starts_with_a_fresh_server_heartbeat_window();
    first_server_heartbeat_has_the_same_liveness_deadline();
    accepted_heartbeat_schedules_client_heartbeat_and_bounds_server_liveness();
    retry_delay_is_exponential_and_bounded();
    trusted_utc_never_moves_backward_and_latches_real_rollback();
    return 0;
}
