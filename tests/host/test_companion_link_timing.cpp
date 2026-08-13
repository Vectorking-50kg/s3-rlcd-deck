#include "deck_companion_link_timing.h"

#include <cassert>
#include <cstdint>

namespace {

void reconnect_starts_with_a_fresh_server_heartbeat_window()
{
    deck_companion_link_timing_t timing{};
    deck_companion_link_timing_server_heartbeat(&timing, 1'000, 10'000);
    assert(deck_companion_link_timing_server_expired(&timing, 31'000, 30'000));

    deck_companion_link_timing_begin_connection(&timing);

    assert(!deck_companion_link_timing_server_expired(&timing, 61'000, 30'000));
    assert(!deck_companion_link_timing_client_due(&timing, 61'000));
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

}  // namespace

int main()
{
    reconnect_starts_with_a_fresh_server_heartbeat_window();
    accepted_heartbeat_schedules_client_heartbeat_and_bounds_server_liveness();
    retry_delay_is_exponential_and_bounded();
    return 0;
}
