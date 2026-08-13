#pragma once

#include <stdbool.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct {
    uint64_t last_server_heartbeat_ms;
    uint64_t next_client_heartbeat_ms;
} deck_companion_link_timing_t;

/* Starts a transport attempt without inheriting liveness from an old session. */
void deck_companion_link_timing_begin_connection(
    deck_companion_link_timing_t *timing
);

void deck_companion_link_timing_server_heartbeat(
    deck_companion_link_timing_t *timing,
    uint64_t now_ms,
    uint64_t client_interval_ms
);

bool deck_companion_link_timing_server_expired(
    const deck_companion_link_timing_t *timing,
    uint64_t now_ms,
    uint64_t timeout_ms
);

bool deck_companion_link_timing_client_due(
    const deck_companion_link_timing_t *timing,
    uint64_t now_ms
);

void deck_companion_link_timing_client_sent(
    deck_companion_link_timing_t *timing,
    uint64_t now_ms,
    uint64_t client_interval_ms
);

uint64_t deck_companion_link_retry_delay_ms(uint32_t attempt);

#ifdef __cplusplus
}
#endif
