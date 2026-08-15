#include "deck_serial_usb_bridge.h"

#include <atomic>
#include <cstring>
#include <limits>
#include <mutex>
#include <new>

struct deck_serial_usb_bridge {
    deck_serial_usb_io_adapter_t io;
    deck_serial_usb_memory_t memory;
    deck_serial_routed_block_t pending_output;
    size_t pending_offset;
    bool has_pending_output;
    uint8_t input[DECK_SERIAL_ROUTER_BLOCK_BYTES];
    std::mutex output_mutex;
    std::mutex input_mutex;
    std::atomic<uint64_t> output_bytes;
    std::atomic<uint64_t> output_blocks;
    std::atomic<uint64_t> output_backpressure;
    std::atomic<uint64_t> output_failures;
    std::atomic<uint64_t> last_output_sequence;
    std::atomic<uint64_t> input_bytes;
    std::atomic<uint64_t> input_blocks;
    std::atomic<uint64_t> input_backpressure;
    std::atomic<uint64_t> input_failures;
    std::atomic<uint64_t> input_submit_failures;
    std::atomic<uint64_t> disconnect_observations;
};

namespace {

bool valid_adapter(const deck_serial_usb_io_adapter_t &adapter)
{
    return adapter.connected != nullptr && adapter.take_output != nullptr &&
           adapter.write_output != nullptr && adapter.input_ready != nullptr &&
           adapter.input_authority_generation != nullptr &&
           adapter.read_input != nullptr && adapter.submit_input != nullptr;
}

uint64_t saturating_increment(std::atomic<uint64_t> &value, uint64_t amount = 1)
{
    uint64_t current = value.load(std::memory_order_relaxed);
    while (true) {
        const uint64_t next =
            current > std::numeric_limits<uint64_t>::max() - amount
                ? std::numeric_limits<uint64_t>::max()
                : current + amount;
        if (value.compare_exchange_weak(
                current,
                next,
                std::memory_order_relaxed,
                std::memory_order_relaxed
            )) {
            return next;
        }
    }
}

void clear_pending_output(deck_serial_usb_bridge_t *bridge)
{
    std::memset(&bridge->pending_output, 0, sizeof(bridge->pending_output));
    bridge->pending_offset = 0;
    bridge->has_pending_output = false;
}

}  // namespace

deck_serial_usb_bridge_t *deck_serial_usb_bridge_create(
    const deck_serial_usb_bridge_config_t *config
)
{
    if (config == nullptr || !valid_adapter(config->io) ||
        config->memory.allocate == nullptr ||
        config->memory.deallocate == nullptr) {
        return nullptr;
    }
    void *storage = config->memory.allocate(
        config->memory.context,
        sizeof(deck_serial_usb_bridge_t)
    );
    if (storage == nullptr) {
        return nullptr;
    }
    auto *bridge = new (storage) deck_serial_usb_bridge_t{};
    bridge->io = config->io;
    bridge->memory = config->memory;
    return bridge;
}

void deck_serial_usb_bridge_destroy(deck_serial_usb_bridge_t *bridge)
{
    if (bridge == nullptr) {
        return;
    }
    const deck_serial_usb_memory_t memory = bridge->memory;
    {
        std::lock_guard<std::mutex> output_lock(bridge->output_mutex);
        clear_pending_output(bridge);
    }
    {
        std::lock_guard<std::mutex> input_lock(bridge->input_mutex);
        std::memset(bridge->input, 0, sizeof(bridge->input));
    }
    bridge->~deck_serial_usb_bridge_t();
    memory.deallocate(memory.context, bridge);
}

deck_serial_usb_pump_result_t deck_serial_usb_bridge_pump_output(
    deck_serial_usb_bridge_t *bridge
)
{
    if (bridge == nullptr) {
        return DECK_SERIAL_USB_ERROR;
    }
    std::lock_guard<std::mutex> lock(bridge->output_mutex);
    if (!bridge->io.connected(bridge->io.context)) {
        saturating_increment(bridge->disconnect_observations);
        return DECK_SERIAL_USB_IDLE;
    }
    if (!bridge->has_pending_output) {
        const deck_serial_router_copy_result_t result =
            bridge->io.take_output(
                bridge->io.context,
                &bridge->pending_output
            );
        if (result == DECK_SERIAL_ROUTER_COPY_EMPTY) {
            return DECK_SERIAL_USB_IDLE;
        }
        if (result != DECK_SERIAL_ROUTER_COPY_OK ||
            bridge->pending_output.length == 0 ||
            bridge->pending_output.length > DECK_SERIAL_ROUTER_BLOCK_BYTES) {
            saturating_increment(bridge->output_failures);
            clear_pending_output(bridge);
            return DECK_SERIAL_USB_ERROR;
        }
        bridge->has_pending_output = true;
    }
    const size_t remaining =
        static_cast<size_t>(bridge->pending_output.length) -
        bridge->pending_offset;
    const int written = bridge->io.write_output(
        bridge->io.context,
        bridge->pending_output.bytes + bridge->pending_offset,
        remaining
    );
    if (written < 0 || static_cast<size_t>(written) > remaining) {
        saturating_increment(bridge->output_failures);
        return DECK_SERIAL_USB_ERROR;
    }
    if (written == 0) {
        saturating_increment(bridge->output_backpressure);
        return DECK_SERIAL_USB_BACKPRESSURE;
    }
    bridge->pending_offset += static_cast<size_t>(written);
    if (bridge->pending_offset == bridge->pending_output.length) {
        saturating_increment(
            bridge->output_bytes,
            bridge->pending_output.length
        );
        saturating_increment(bridge->output_blocks);
        bridge->last_output_sequence.store(
            bridge->pending_output.sequence,
            std::memory_order_relaxed
        );
        clear_pending_output(bridge);
    }
    return DECK_SERIAL_USB_PROGRESS;
}

deck_serial_usb_pump_result_t deck_serial_usb_bridge_pump_input(
    deck_serial_usb_bridge_t *bridge
)
{
    if (bridge == nullptr) {
        return DECK_SERIAL_USB_ERROR;
    }
    std::lock_guard<std::mutex> lock(bridge->input_mutex);
    if (!bridge->io.connected(bridge->io.context)) {
        saturating_increment(bridge->disconnect_observations);
        return DECK_SERIAL_USB_IDLE;
    }
    if (!bridge->io.input_ready(bridge->io.context)) {
        saturating_increment(bridge->input_backpressure);
        return DECK_SERIAL_USB_BACKPRESSURE;
    }
    const uint64_t authority_generation =
        bridge->io.input_authority_generation(bridge->io.context);
    const int received = bridge->io.read_input(
        bridge->io.context,
        bridge->input,
        sizeof(bridge->input)
    );
    if (received < 0 ||
        static_cast<size_t>(received) > sizeof(bridge->input)) {
        saturating_increment(bridge->input_failures);
        std::memset(bridge->input, 0, sizeof(bridge->input));
        return DECK_SERIAL_USB_ERROR;
    }
    if (received == 0) {
        return DECK_SERIAL_USB_IDLE;
    }
    const size_t size = static_cast<size_t>(received);
    const bool submitted = bridge->io.submit_input(
        bridge->io.context,
        bridge->input,
        size,
        authority_generation
    );
    std::memset(bridge->input, 0, sizeof(bridge->input));
    if (!submitted) {
        saturating_increment(bridge->input_submit_failures);
        return DECK_SERIAL_USB_ERROR;
    }
    saturating_increment(bridge->input_bytes, size);
    saturating_increment(bridge->input_blocks);
    return DECK_SERIAL_USB_PROGRESS;
}

bool deck_serial_usb_bridge_stats(
    const deck_serial_usb_bridge_t *bridge,
    deck_serial_usb_bridge_stats_t *stats
)
{
    if (bridge == nullptr || stats == nullptr) {
        return false;
    }
    *stats = {
        bridge->output_bytes.load(std::memory_order_relaxed),
        bridge->output_blocks.load(std::memory_order_relaxed),
        bridge->output_backpressure.load(std::memory_order_relaxed),
        bridge->output_failures.load(std::memory_order_relaxed),
        bridge->last_output_sequence.load(std::memory_order_relaxed),
        bridge->input_bytes.load(std::memory_order_relaxed),
        bridge->input_blocks.load(std::memory_order_relaxed),
        bridge->input_backpressure.load(std::memory_order_relaxed),
        bridge->input_failures.load(std::memory_order_relaxed),
        bridge->input_submit_failures.load(std::memory_order_relaxed),
        bridge->disconnect_observations.load(std::memory_order_relaxed),
    };
    return true;
}
