#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct {
    char *buffer;
    size_t capacity;
    size_t message_size;
    size_t frame_size;
    int frame_payload_length;
    uint8_t frame_opcode;
    uint8_t message_opcode;
    bool active;
    bool frame_active;
    bool awaiting_continuation;
} deck_companion_link_frame_t;

typedef enum {
    DECK_COMPANION_LINK_FRAME_INVALID = 0,
    DECK_COMPANION_LINK_FRAME_PARTIAL,
    DECK_COMPANION_LINK_FRAME_COMPLETE,
} deck_companion_link_frame_result_t;

void deck_companion_link_frame_init(
    deck_companion_link_frame_t *frame,
    char *buffer,
    size_t capacity
);
void deck_companion_link_frame_reset(deck_companion_link_frame_t *frame);

/*
 * Reassembles both transport chunks inside one WebSocket frame and RFC 6455
 * continuation frames. `final` is consulted only after the current frame's
 * payload_offset/data_size reaches payload_length; esp_websocket_client
 * repeats the frame FIN flag on every transport chunk.
 */
deck_companion_link_frame_result_t deck_companion_link_frame_accept(
    deck_companion_link_frame_t *frame,
    int payload_length,
    int payload_offset,
    uint8_t opcode,
    bool final,
    const char *data,
    size_t data_size
);

#ifdef __cplusplus
}
#endif
