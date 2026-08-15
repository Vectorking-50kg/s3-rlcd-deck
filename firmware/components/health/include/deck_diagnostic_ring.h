#pragma once

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define DECK_DIAGNOSTIC_RING_CAPACITY 64

typedef enum {
    DECK_DIAGNOSTIC_LEVEL_INFO = 0,
    DECK_DIAGNOSTIC_LEVEL_WARNING,
    DECK_DIAGNOSTIC_LEVEL_ERROR,
} deck_diagnostic_level_t;

typedef enum {
    DECK_DIAGNOSTIC_COMPONENT_SYSTEM = 0,
    DECK_DIAGNOSTIC_COMPONENT_DISPLAY,
    DECK_DIAGNOSTIC_COMPONENT_WIFI,
    DECK_DIAGNOSTIC_COMPONENT_SETUP,
    DECK_DIAGNOSTIC_COMPONENT_SENSOR,
    DECK_DIAGNOSTIC_COMPONENT_DEVICE_LINK,
    DECK_DIAGNOSTIC_COMPONENT_SERIAL,
    DECK_DIAGNOSTIC_COMPONENT_OTA,
} deck_diagnostic_component_t;

typedef enum {
    DECK_DIAGNOSTIC_CODE_BOOT = 0,
    DECK_DIAGNOSTIC_CODE_READY,
    DECK_DIAGNOSTIC_CODE_UNAVAILABLE,
    DECK_DIAGNOSTIC_CODE_CONNECTED,
    DECK_DIAGNOSTIC_CODE_DISCONNECTED,
    DECK_DIAGNOSTIC_CODE_STORAGE_ERROR,
    DECK_DIAGNOSTIC_CODE_PROTOCOL_ERROR,
    DECK_DIAGNOSTIC_CODE_TIMEOUT,
    DECK_DIAGNOSTIC_CODE_OWNER_CHANGED,
    DECK_DIAGNOSTIC_CODE_UPDATE_STARTED,
    DECK_DIAGNOSTIC_CODE_UPDATE_FAILED,
    DECK_DIAGNOSTIC_CODE_ROLLBACK,
    DECK_DIAGNOSTIC_CODE_QUEUE_OVERFLOW,
} deck_diagnostic_code_t;

typedef struct {
    uint64_t monotonic_ms;
    deck_diagnostic_level_t level;
    deck_diagnostic_component_t component;
    deck_diagnostic_code_t code;
    uint32_t value;
} deck_diagnostic_event_t;

typedef struct {
    deck_diagnostic_event_t events[DECK_DIAGNOSTIC_RING_CAPACITY];
    size_t count;
    uint32_t dropped;
} deck_diagnostic_snapshot_t;

/* Release-safe fixed ring. It stores no caller strings or payload bytes. */
void deck_diagnostic_ring_reset(void);

bool deck_diagnostic_ring_record(
    uint64_t monotonic_ms,
    deck_diagnostic_level_t level,
    deck_diagnostic_component_t component,
    deck_diagnostic_code_t code,
    uint32_t value
);

void deck_diagnostic_ring_snapshot(deck_diagnostic_snapshot_t *snapshot);

const char *deck_diagnostic_level_name(deck_diagnostic_level_t level);
const char *deck_diagnostic_component_name(deck_diagnostic_component_t component);
const char *deck_diagnostic_code_name(deck_diagnostic_code_t code);

bool deck_diagnostic_snapshot_format(
    const deck_diagnostic_snapshot_t *snapshot,
    uint64_t request_id,
    char *output,
    size_t capacity,
    size_t *output_size
);

#ifdef __cplusplus
}
#endif
