#include "deck_companion_link_frame.h"

#include <cstring>

namespace {

constexpr uint8_t kContinuationOpcode = 0;
constexpr uint8_t kTextOpcode = 1;
constexpr uint8_t kBinaryOpcode = 2;

bool start_transport_frame(
    deck_companion_link_frame_t *frame,
    int payload_length,
    uint8_t opcode
)
{
    if (!frame->active) {
        if (opcode != kTextOpcode && opcode != kBinaryOpcode) {
            return false;
        }
        frame->active = true;
        frame->message_opcode = opcode;
        frame->message_size = 0;
    } else if (!frame->awaiting_continuation || opcode != kContinuationOpcode) {
        return false;
    }
    frame->awaiting_continuation = false;
    frame->frame_active = true;
    frame->frame_payload_length = payload_length;
    frame->frame_opcode = opcode;
    frame->frame_size = 0;
    return true;
}

}  // namespace

void deck_companion_link_frame_init(
    deck_companion_link_frame_t *frame,
    char *buffer,
    size_t capacity
)
{
    if (frame == nullptr) {
        return;
    }
    *frame = {};
    frame->buffer = buffer;
    frame->capacity = capacity;
}

void deck_companion_link_frame_reset(deck_companion_link_frame_t *frame)
{
    if (frame == nullptr) {
        return;
    }
    char *buffer = frame->buffer;
    const size_t capacity = frame->capacity;
    *frame = {};
    frame->buffer = buffer;
    frame->capacity = capacity;
}

deck_companion_link_frame_result_t deck_companion_link_frame_accept(
    deck_companion_link_frame_t *frame,
    int payload_length,
    int payload_offset,
    uint8_t opcode,
    bool final,
    const char *data,
    size_t data_size
)
{
    if (frame == nullptr || frame->buffer == nullptr || frame->capacity == 0 ||
        payload_length <= 0 || payload_offset < 0 ||
        static_cast<size_t>(payload_offset) + data_size >
            static_cast<size_t>(payload_length) ||
        (data_size != 0 && data == nullptr)) {
        return DECK_COMPANION_LINK_FRAME_INVALID;
    }
    if (payload_offset == 0) {
        if (frame->frame_active ||
            !start_transport_frame(frame, payload_length, opcode)) {
            return DECK_COMPANION_LINK_FRAME_INVALID;
        }
    } else if (!frame->frame_active ||
               frame->frame_payload_length != payload_length ||
               frame->frame_opcode != opcode ||
               static_cast<size_t>(payload_offset) != frame->frame_size) {
        return DECK_COMPANION_LINK_FRAME_INVALID;
    }
    if (data_size > frame->capacity - frame->message_size) {
        return DECK_COMPANION_LINK_FRAME_INVALID;
    }
    if (data_size != 0) {
        std::memcpy(frame->buffer + frame->message_size, data, data_size);
    }
    frame->message_size += data_size;
    frame->frame_size += data_size;
    if (frame->frame_size != static_cast<size_t>(frame->frame_payload_length)) {
        return DECK_COMPANION_LINK_FRAME_PARTIAL;
    }
    frame->frame_active = false;
    frame->frame_size = 0;
    frame->frame_payload_length = 0;
    frame->frame_opcode = 0;
    if (!final) {
        frame->awaiting_continuation = true;
        return DECK_COMPANION_LINK_FRAME_PARTIAL;
    }
    frame->awaiting_continuation = false;
    return DECK_COMPANION_LINK_FRAME_COMPLETE;
}
