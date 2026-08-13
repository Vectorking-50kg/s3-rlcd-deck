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
    deck_companion_link_timing_t *timing
)
{
    if (timing != nullptr) {
        *timing = {};
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
    timing->next_client_heartbeat_ms = deadline(now_ms, client_interval_ms);
}

bool deck_companion_link_timing_server_expired(
    const deck_companion_link_timing_t *timing,
    uint64_t now_ms,
    uint64_t timeout_ms
)
{
    return timing != nullptr && timing->last_server_heartbeat_ms != 0 &&
           timeout_ms != 0 && now_ms >= timing->last_server_heartbeat_ms &&
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
