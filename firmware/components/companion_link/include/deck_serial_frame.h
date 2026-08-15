#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define DECK_SERIAL_FRAME_HEADER_BYTES 32U
#define DECK_SERIAL_FRAME_MAX_PAYLOAD_BYTES 256U
#define DECK_SERIAL_FRAME_MAX_BYTES \
    (DECK_SERIAL_FRAME_HEADER_BYTES + DECK_SERIAL_FRAME_MAX_PAYLOAD_BYTES)

typedef enum {
    DECK_SERIAL_FRAME_TARGET_RX = 1,
    DECK_SERIAL_FRAME_WEB_TX = 2,
} deck_serial_frame_channel_t;

typedef struct {
    deck_serial_frame_channel_t channel;
    uint64_t session_id;
    uint64_t sequence;
    uint64_t monotonic_ms;
    const uint8_t *payload;
    uint16_t payload_size;
} deck_serial_frame_view_t;

typedef struct {
    uint64_t session_id;
    uint64_t sequence;
    uint64_t monotonic_ms;
    bool has_value;
} deck_serial_frame_order_t;

size_t deck_serial_frame_encode(
    deck_serial_frame_channel_t channel,
    uint64_t session_id,
    uint64_t sequence,
    uint64_t monotonic_ms,
    const uint8_t *payload,
    size_t payload_size,
    uint8_t *output,
    size_t output_capacity
);

bool deck_serial_frame_decode(
    const uint8_t *document,
    size_t document_size,
    deck_serial_frame_view_t *frame
);

void deck_serial_frame_order_reset(deck_serial_frame_order_t *order);

bool deck_serial_frame_order_accepts(
    const deck_serial_frame_order_t *order,
    const deck_serial_frame_view_t *frame
);

void deck_serial_frame_order_commit(
    deck_serial_frame_order_t *order,
    const deck_serial_frame_view_t *frame
);

#ifdef __cplusplus
}
#endif
