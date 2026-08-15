#include "deck_companion_link_timing.h"

#include <cstdint>

namespace {

constexpr uint64_t kInitialRetryMs = 1'000;
constexpr uint64_t kMaximumRetryMs = 30'000;

uint64_t deadline(uint64_t now_ms, uint64_t delay_ms)
{
    return delay_ms > UINT64_MAX - now_ms ? UINT64_MAX : now_ms + delay_ms;
}

}  // namespace

void deck_companion_link_timing_begin_connection(
    deck_companion_link_timing_t *timing,
    uint64_t now_ms,
    uint64_t timeout_ms
)
{
    if (timing != nullptr) {
        *timing = {};
        timing->server_heartbeat_deadline_ms = deadline(now_ms, timeout_ms);
    }
}

void deck_companion_link_timing_server_heartbeat(
    deck_companion_link_timing_t *timing,
    uint64_t now_ms,
    uint64_t client_interval_ms
)
{
    if (timing == nullptr || client_interval_ms == 0) {
        return;
    }
    timing->last_server_heartbeat_ms = now_ms;
    timing->server_heartbeat_deadline_ms = 0;
    timing->next_client_heartbeat_ms = deadline(now_ms, client_interval_ms);
}

bool deck_companion_link_timing_server_expired(
    const deck_companion_link_timing_t *timing,
    uint64_t now_ms,
    uint64_t timeout_ms
)
{
    if (timing == nullptr || timeout_ms == 0) {
        return false;
    }
    if (timing->last_server_heartbeat_ms == 0) {
        return timing->server_heartbeat_deadline_ms != 0 &&
               now_ms >= timing->server_heartbeat_deadline_ms;
    }
    return now_ms >= timing->last_server_heartbeat_ms &&
           now_ms - timing->last_server_heartbeat_ms >= timeout_ms;
}

bool deck_companion_link_timing_client_due(
    const deck_companion_link_timing_t *timing,
    uint64_t now_ms
)
{
    return timing != nullptr && timing->next_client_heartbeat_ms != 0 &&
           now_ms >= timing->next_client_heartbeat_ms;
}

void deck_companion_link_timing_client_sent(
    deck_companion_link_timing_t *timing,
    uint64_t now_ms,
    uint64_t client_interval_ms
)
{
    if (timing != nullptr && client_interval_ms != 0) {
        timing->next_client_heartbeat_ms = deadline(now_ms, client_interval_ms);
    }
}

uint64_t deck_companion_link_retry_delay_ms(uint32_t attempt)
{
    if (attempt <= 1U) {
        return kInitialRetryMs;
    }
    if (attempt >= 6U) {
        return kMaximumRetryMs;
    }
    const uint64_t delay = kInitialRetryMs << (attempt - 1U);
    return delay > kMaximumRetryMs ? kMaximumRetryMs : delay;
}

bool deck_companion_trusted_clock_current(
    const deck_companion_trusted_clock_t *clock,
    uint64_t now_monotonic_ms,
    uint64_t *utc_ms
)
{
    if (clock == nullptr || utc_ms == nullptr || !clock->has_time ||
        clock->rollback_latched || now_monotonic_ms < clock->base_monotonic_ms) {
        return false;
    }
    const uint64_t elapsed = now_monotonic_ms - clock->base_monotonic_ms;
    if (elapsed > UINT64_MAX - clock->base_utc_ms) {
        return false;
    }
    *utc_ms = clock->base_utc_ms + elapsed;
    return true;
}

bool deck_companion_trusted_clock_accept(
    deck_companion_trusted_clock_t *clock,
    uint64_t server_utc_ms,
    uint64_t now_monotonic_ms
)
{
    if (clock == nullptr) {
        return false;
    }
    if (clock->rollback_latched) {
        if (server_utc_ms < clock->recovery_utc_floor_ms) {
            return false;
        }
    } else if (clock->has_time && server_utc_ms < clock->server_utc_high_water_ms) {
        uint64_t recovery_floor = clock->server_utc_high_water_ms;
        uint64_t current_utc_ms = 0;
        if (deck_companion_trusted_clock_current(
                clock,
                now_monotonic_ms,
                &current_utc_ms
            ) && current_utc_ms > recovery_floor) {
            recovery_floor = current_utc_ms;
        }
        clock->recovery_utc_floor_ms = recovery_floor;
        clock->rollback_latched = true;
        return false;
    }

    uint64_t monotonic_utc_ms = 0;
    const bool was_latched = clock->rollback_latched;
    const bool had_monotonic_time =
        !was_latched && deck_companion_trusted_clock_current(
                            clock,
                            now_monotonic_ms,
                            &monotonic_utc_ms
                        );
    clock->base_utc_ms = had_monotonic_time && monotonic_utc_ms > server_utc_ms
                             ? monotonic_utc_ms
                             : server_utc_ms;
    clock->base_monotonic_ms = now_monotonic_ms;
    if (!clock->has_time || server_utc_ms > clock->server_utc_high_water_ms) {
        clock->server_utc_high_water_ms = server_utc_ms;
    }
    clock->has_time = true;
    clock->rollback_latched = false;
    clock->recovery_utc_floor_ms = 0;
    return true;
}
