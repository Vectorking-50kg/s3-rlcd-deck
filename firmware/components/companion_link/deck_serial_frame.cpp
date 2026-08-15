#include "deck_serial_frame.h"

#include <cstring>

namespace {

constexpr uint8_t kMagic[] = {'S', 'R', 'D', '1'};

void write_u16(uint8_t *output, uint16_t value)
{
    output[0] = static_cast<uint8_t>(value >> 8U);
    output[1] = static_cast<uint8_t>(value);
}

void write_u64(uint8_t *output, uint64_t value)
{
    for (size_t index = 0; index < 8; ++index) {
        output[index] = static_cast<uint8_t>(value >> ((7U - index) * 8U));
    }
}

uint16_t read_u16(const uint8_t *input)
{
    return static_cast<uint16_t>(
        (static_cast<uint16_t>(input[0]) << 8U) | input[1]
    );
}

uint64_t read_u64(const uint8_t *input)
{
    uint64_t value = 0;
    for (size_t index = 0; index < 8; ++index) {
        value = (value << 8U) | input[index];
    }
    return value;
}

bool valid_channel(uint8_t channel)
{
    return channel == DECK_SERIAL_FRAME_TARGET_RX ||
           channel == DECK_SERIAL_FRAME_WEB_TX;
}

bool sequence_after(uint64_t candidate, uint64_t previous)
{
    const uint64_t difference = candidate - previous;
    return difference != 0 && difference < (UINT64_C(1) << 63U);
}

}  // namespace

size_t deck_serial_frame_encode(
    deck_serial_frame_channel_t channel,
    uint64_t session_id,
    uint64_t sequence,
    uint64_t monotonic_ms,
    const uint8_t *payload,
    size_t payload_size,
    uint8_t *output,
    size_t output_capacity
)
{
    if (!valid_channel(static_cast<uint8_t>(channel)) || session_id == 0 ||
        sequence == 0 || payload == nullptr || payload_size == 0 ||
        payload_size > DECK_SERIAL_FRAME_MAX_PAYLOAD_BYTES || output == nullptr ||
        output_capacity < DECK_SERIAL_FRAME_HEADER_BYTES + payload_size) {
        return 0;
    }
    std::memcpy(output, kMagic, sizeof(kMagic));
    output[4] = static_cast<uint8_t>(channel);
    output[5] = 0;
    write_u16(output + 6, static_cast<uint16_t>(payload_size));
    write_u64(output + 8, session_id);
    write_u64(output + 16, sequence);
    write_u64(output + 24, monotonic_ms);
    std::memcpy(output + DECK_SERIAL_FRAME_HEADER_BYTES, payload, payload_size);
    return DECK_SERIAL_FRAME_HEADER_BYTES + payload_size;
}

bool deck_serial_frame_decode(
    const uint8_t *document,
    size_t document_size,
    deck_serial_frame_view_t *frame
)
{
    if (document == nullptr || frame == nullptr ||
        document_size < DECK_SERIAL_FRAME_HEADER_BYTES ||
        std::memcmp(document, kMagic, sizeof(kMagic)) != 0 ||
        !valid_channel(document[4]) || document[5] != 0) {
        return false;
    }
    const uint16_t payload_size = read_u16(document + 6);
    const uint64_t session_id = read_u64(document + 8);
    const uint64_t sequence = read_u64(document + 16);
    if (payload_size == 0 || payload_size > DECK_SERIAL_FRAME_MAX_PAYLOAD_BYTES ||
        document_size != DECK_SERIAL_FRAME_HEADER_BYTES + payload_size ||
        session_id == 0 || sequence == 0) {
        return false;
    }
    *frame = {
        static_cast<deck_serial_frame_channel_t>(document[4]),
        session_id,
        sequence,
        read_u64(document + 24),
        document + DECK_SERIAL_FRAME_HEADER_BYTES,
        payload_size,
    };
    return true;
}

void deck_serial_frame_order_reset(deck_serial_frame_order_t *order)
{
    if (order != nullptr) {
        *order = {};
    }
}

bool deck_serial_frame_order_accepts(
    const deck_serial_frame_order_t *order,
    const deck_serial_frame_view_t *frame
)
{
    if (order == nullptr || frame == nullptr || frame->session_id == 0 ||
        frame->sequence == 0) {
        return false;
    }
    return !order->has_value || order->session_id != frame->session_id ||
           (sequence_after(frame->sequence, order->sequence) &&
            frame->monotonic_ms >= order->monotonic_ms);
}

void deck_serial_frame_order_commit(
    deck_serial_frame_order_t *order,
    const deck_serial_frame_view_t *frame
)
{
    if (order == nullptr || frame == nullptr) {
        return;
    }
    order->session_id = frame->session_id;
    order->sequence = frame->sequence;
    order->monotonic_ms = frame->monotonic_ms;
    order->has_value = true;
}
