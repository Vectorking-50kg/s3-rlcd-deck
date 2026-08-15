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

typedef enum {
    DECK_DEVICE_HEARTBEAT_INVALID = 0,
    DECK_DEVICE_HEARTBEAT_VALID,
    DECK_DEVICE_HEARTBEAT_UNSUPPORTED_MAJOR,
} deck_device_heartbeat_result_t;

typedef enum {
    DECK_DEVICE_SERIAL_OWNER_REQUEST = 0,
    DECK_DEVICE_SERIAL_OWNER_ACTIVITY,
    DECK_DEVICE_SERIAL_HISTORY_REQUEST,
} deck_device_serial_control_kind_t;

typedef struct {
    deck_device_serial_control_kind_t kind;
    uint64_t session_id;
    uint64_t request_id;
    uint64_t lease_id;
    uint64_t after_sequence;
    bool enable;
} deck_device_serial_control_t;

typedef struct {
    uint64_t request_id;
} deck_device_diagnostics_request_t;

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

deck_device_heartbeat_result_t deck_device_protocol_parse_heartbeat(
    const char *message,
    size_t message_size,
    uint64_t previous_monotonic_ms,
    bool has_previous,
    deck_device_heartbeat_t *heartbeat
);

bool deck_device_protocol_parse_serial_control(
    const char *message,
    size_t message_size,
    deck_device_serial_control_t *control
);

bool deck_device_protocol_parse_diagnostics_request(
    const char *message,
    size_t message_size,
    deck_device_diagnostics_request_t *request
);

#ifdef __cplusplus
}
#endif
