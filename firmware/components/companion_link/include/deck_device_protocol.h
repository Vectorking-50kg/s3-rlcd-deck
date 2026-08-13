#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define DECK_DEVICE_PROTOCOL_VERSION 1
#define DECK_DEVICE_PROTOCOL_MAX_CONTROL_BYTES (16 * 1024)

typedef struct {
    uint64_t utc_unix_ms;
    uint64_t monotonic_ms;
} deck_device_heartbeat_t;

/* Constant-time comparison after validating the canonical lowercase wire form. */
bool deck_device_protocol_fingerprint_matches_sha256(
    const uint8_t digest[32],
    const char *fingerprint
);

bool deck_device_protocol_validate_hello(
    const char *message,
    size_t message_size,
    const char *authenticated_device_id
);

bool deck_device_protocol_parse_heartbeat(
    const char *message,
    size_t message_size,
    uint64_t previous_monotonic_ms,
    bool has_previous,
    deck_device_heartbeat_t *heartbeat
);

#ifdef __cplusplus
}
#endif
