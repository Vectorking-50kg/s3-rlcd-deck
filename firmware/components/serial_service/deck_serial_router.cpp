#include "deck_serial_router.h"

#include <cstring>
#include <limits>
#include <mutex>
#include <new>

namespace {

constexpr uint32_t kNoBlock = std::numeric_limits<uint32_t>::max();

struct BlockMetadata {
    uint64_t sequence;
    uint64_t monotonic_ms;
    uint32_t next_free;
    uint16_t length;
    uint8_t references;
};

struct Sink {
    deck_serial_sink_config_t config;
    uint32_t *blocks;
    size_t capacity_blocks;
    size_t head;
    size_t count;
    size_t queued_bytes;
    uint64_t overwritten_bytes;
    uint64_t overwritten_blocks;
    uint64_t delivered_bytes;
    uint64_t delivered_blocks;
    uint64_t cleared_bytes;
    uint64_t cleared_blocks;
    bool registered;
};

uint64_t saturating_add(uint64_t left, uint64_t right)
{
    return left > std::numeric_limits<uint64_t>::max() - right
               ? std::numeric_limits<uint64_t>::max()
               : left + right;
}

size_t saturating_add_size(size_t left, size_t right)
{
    return left > std::numeric_limits<size_t>::max() - right
               ? std::numeric_limits<size_t>::max()
               : left + right;
}

bool valid_sink_id(deck_serial_sink_id_t id)
{
    return static_cast<unsigned>(id) < DECK_SERIAL_ROUTER_MAX_SINKS;
}

uint64_t next_nonzero(uint64_t sequence)
{
    ++sequence;
    return sequence == 0 ? 1 : sequence;
}

}  // namespace

struct deck_serial_router {
    deck_serial_router_memory_t memory;
    uint64_t session_id;
    size_t pool_capacity_bytes;
    size_t pool_blocks;
    uint8_t *payloads;
    BlockMetadata *metadata;
    uint32_t free_head;
    Sink sinks[DECK_SERIAL_ROUTER_MAX_SINKS];
    deck_serial_router_stats_t stats;
    std::mutex mutex;
};

namespace {

Sink *find_sink(deck_serial_router_t *router, deck_serial_sink_id_t id)
{
    if (!valid_sink_id(id)) {
        return nullptr;
    }
    Sink *sink = &router->sinks[static_cast<unsigned>(id)];
    return sink->registered ? sink : nullptr;
}

const uint8_t *payload(
    const deck_serial_router_t *router,
    uint32_t block_index
)
{
    return router->payloads +
           static_cast<size_t>(block_index) * DECK_SERIAL_ROUTER_BLOCK_BYTES;
}

uint8_t *payload(deck_serial_router_t *router, uint32_t block_index)
{
    return router->payloads +
           static_cast<size_t>(block_index) * DECK_SERIAL_ROUTER_BLOCK_BYTES;
}

void return_block(deck_serial_router_t *router, uint32_t block_index)
{
    BlockMetadata &block = router->metadata[block_index];
    block.sequence = 0;
    block.monotonic_ms = 0;
    block.length = 0;
    block.references = 0;
    block.next_free = router->free_head;
    std::memset(payload(router, block_index), 0, DECK_SERIAL_ROUTER_BLOCK_BYTES);
    router->free_head = block_index;
}

void release_reference(deck_serial_router_t *router, uint32_t block_index)
{
    BlockMetadata &block = router->metadata[block_index];
    if (block.references == 0) {
        return;
    }
    --block.references;
    if (block.references == 0) {
        return_block(router, block_index);
    }
}

void remove_front(
    deck_serial_router_t *router,
    Sink *sink,
    bool overwritten,
    bool cleared,
    bool delivered
)
{
    if (sink->count == 0) {
        return;
    }
    const uint32_t block_index = sink->blocks[sink->head];
    const size_t length = router->metadata[block_index].length;
    sink->head = (sink->head + 1U) % sink->capacity_blocks;
    --sink->count;
    sink->queued_bytes -= length;
    if (overwritten) {
        sink->overwritten_bytes = saturating_add(sink->overwritten_bytes, length);
        sink->overwritten_blocks = saturating_add(sink->overwritten_blocks, 1);
    }
    if (cleared) {
        sink->cleared_bytes = saturating_add(sink->cleared_bytes, length);
        sink->cleared_blocks = saturating_add(sink->cleared_blocks, 1);
    }
    if (delivered) {
        sink->delivered_bytes = saturating_add(sink->delivered_bytes, length);
        sink->delivered_blocks = saturating_add(sink->delivered_blocks, 1);
    }
    release_reference(router, block_index);
}

void clear_sink_locked(deck_serial_router_t *router, Sink *sink)
{
    while (sink->count != 0) {
        remove_front(router, sink, false, true, false);
    }
    sink->head = 0;
}

void copy_block(
    const deck_serial_router_t *router,
    uint32_t block_index,
    deck_serial_routed_block_t *output
)
{
    const BlockMetadata &block = router->metadata[block_index];
    output->session_id = router->session_id;
    output->sequence = block.sequence;
    output->monotonic_ms = block.monotonic_ms;
    output->length = block.length;
    std::memcpy(output->bytes, payload(router, block_index), block.length);
    if (block.length < DECK_SERIAL_ROUTER_BLOCK_BYTES) {
        std::memset(
            output->bytes + block.length,
            0,
            DECK_SERIAL_ROUTER_BLOCK_BYTES - block.length
        );
    }
}

uint32_t sink_block_at(const Sink *sink, size_t offset)
{
    return sink->blocks[(sink->head + offset) % sink->capacity_blocks];
}

void fill_sink_stats(
    const deck_serial_router_t *router,
    const Sink *sink,
    deck_serial_sink_stats_t *stats
)
{
    *stats = {
        sink->registered,
        sink->config.retained,
        sink->config.capacity_bytes,
        sink->queued_bytes,
        sink->count,
        0,
        0,
        sink->overwritten_bytes,
        sink->overwritten_blocks,
        sink->delivered_bytes,
        sink->delivered_blocks,
        sink->cleared_bytes,
        sink->cleared_blocks,
    };
    if (sink->count != 0) {
        stats->oldest_sequence =
            router->metadata[sink_block_at(sink, 0)].sequence;
        stats->newest_sequence =
            router->metadata[sink_block_at(sink, sink->count - 1U)].sequence;
    }
}

}  // namespace

deck_serial_router_t *deck_serial_router_create(
    const deck_serial_router_config_t *config
)
{
    if (config == nullptr || config->session_id == 0 ||
        config->pool_capacity_bytes == 0 ||
        config->pool_capacity_bytes % DECK_SERIAL_ROUTER_BLOCK_BYTES != 0 ||
        config->pool_capacity_bytes > DECK_SERIAL_HISTORY_MAX_BYTES ||
        config->memory.allocate == nullptr ||
        config->memory.deallocate == nullptr) {
        return nullptr;
    }
    const size_t block_count =
        config->pool_capacity_bytes / DECK_SERIAL_ROUTER_BLOCK_BYTES;
    if (block_count == 0 || block_count > kNoBlock) {
        return nullptr;
    }
    auto *router = new (std::nothrow) deck_serial_router_t{};
    if (router == nullptr) {
        return nullptr;
    }
    router->memory = config->memory;
    router->session_id = config->session_id;
    router->pool_capacity_bytes = config->pool_capacity_bytes;
    router->pool_blocks = block_count;
    router->stats.last_sequence = config->initial_sequence;
    router->metadata = static_cast<BlockMetadata *>(router->memory.allocate(
        router->memory.context,
        block_count * sizeof(BlockMetadata),
        true
    ));
    router->payloads = static_cast<uint8_t *>(router->memory.allocate(
        router->memory.context,
        config->pool_capacity_bytes,
        true
    ));
    if (router->metadata == nullptr || router->payloads == nullptr) {
        if (router->metadata != nullptr) {
            router->memory.deallocate(router->memory.context, router->metadata);
        }
        if (router->payloads != nullptr) {
            router->memory.deallocate(router->memory.context, router->payloads);
        }
        delete router;
        return nullptr;
    }
    std::memset(router->metadata, 0, block_count * sizeof(BlockMetadata));
    std::memset(router->payloads, 0, config->pool_capacity_bytes);
    for (size_t index = 0; index < block_count; ++index) {
        router->metadata[index].next_free =
            index + 1U < block_count ? static_cast<uint32_t>(index + 1U)
                                     : kNoBlock;
    }
    router->free_head = 0;
    return router;
}

void deck_serial_router_destroy(deck_serial_router_t *router)
{
    if (router == nullptr) {
        return;
    }
    for (Sink &sink : router->sinks) {
        if (sink.registered) {
            clear_sink_locked(router, &sink);
        }
        if (sink.blocks != nullptr) {
            router->memory.deallocate(router->memory.context, sink.blocks);
        }
    }
    std::memset(router->payloads, 0, router->pool_capacity_bytes);
    router->memory.deallocate(router->memory.context, router->payloads);
    std::memset(router->metadata, 0, router->pool_blocks * sizeof(BlockMetadata));
    router->memory.deallocate(router->memory.context, router->metadata);
    delete router;
}

bool deck_serial_router_register_sink(
    deck_serial_router_t *router,
    const deck_serial_sink_config_t *config
)
{
    if (router == nullptr || config == nullptr || !valid_sink_id(config->id) ||
        config->capacity_bytes == 0 ||
        config->capacity_bytes % DECK_SERIAL_ROUTER_BLOCK_BYTES != 0 ||
        config->capacity_bytes > router->pool_capacity_bytes ||
        config->retained != (config->id == DECK_SERIAL_SINK_HISTORY) ||
        (config->id == DECK_SERIAL_SINK_HISTORY &&
         config->capacity_bytes < DECK_SERIAL_HISTORY_MIN_BYTES)) {
        return false;
    }
    std::lock_guard<std::mutex> lock(router->mutex);
    Sink &sink = router->sinks[static_cast<unsigned>(config->id)];
    if (sink.registered) {
        return false;
    }
    const size_t capacity_blocks =
        config->capacity_bytes / DECK_SERIAL_ROUTER_BLOCK_BYTES;
    auto *blocks = static_cast<uint32_t *>(router->memory.allocate(
        router->memory.context,
        capacity_blocks * sizeof(uint32_t),
        true
    ));
    if (blocks == nullptr) {
        return false;
    }
    std::memset(blocks, 0, capacity_blocks * sizeof(uint32_t));
    sink.config = *config;
    sink.blocks = blocks;
    sink.capacity_blocks = capacity_blocks;
    sink.registered = true;
    return true;
}

bool deck_serial_router_unregister_sink(
    deck_serial_router_t *router,
    deck_serial_sink_id_t sink_id
)
{
    if (router == nullptr || !valid_sink_id(sink_id)) {
        return false;
    }
    std::lock_guard<std::mutex> lock(router->mutex);
    Sink *sink = find_sink(router, sink_id);
    if (sink == nullptr) {
        return false;
    }
    clear_sink_locked(router, sink);
    router->memory.deallocate(router->memory.context, sink->blocks);
    *sink = {};
    return true;
}

bool deck_serial_router_submit(
    deck_serial_router_t *router,
    const deck_serial_input_block_t *input,
    uint64_t *sequence
)
{
    if (router == nullptr || input == nullptr || input->length == 0 ||
        input->length > DECK_SERIAL_ROUTER_BLOCK_BYTES) {
        return false;
    }
    std::lock_guard<std::mutex> lock(router->mutex);
    for (Sink &sink : router->sinks) {
        if (sink.registered && sink.count == sink.capacity_blocks) {
            remove_front(router, &sink, true, false, false);
        }
    }
    if (router->free_head == kNoBlock) {
        router->stats.pool_exhaustions =
            saturating_add(router->stats.pool_exhaustions, 1);
        return false;
    }
    const uint32_t block_index = router->free_head;
    BlockMetadata &block = router->metadata[block_index];
    router->free_head = block.next_free;
    block.next_free = kNoBlock;
    block.sequence = next_nonzero(router->stats.last_sequence);
    block.monotonic_ms = input->monotonic_ms;
    block.length = input->length;
    block.references = 0;
    std::memcpy(payload(router, block_index), input->bytes, input->length);
    if (input->length < DECK_SERIAL_ROUTER_BLOCK_BYTES) {
        std::memset(
            payload(router, block_index) + input->length,
            0,
            DECK_SERIAL_ROUTER_BLOCK_BYTES - input->length
        );
    }
    for (Sink &sink : router->sinks) {
        if (!sink.registered) {
            continue;
        }
        const size_t tail = (sink.head + sink.count) % sink.capacity_blocks;
        sink.blocks[tail] = block_index;
        ++sink.count;
        sink.queued_bytes = saturating_add_size(sink.queued_bytes, input->length);
        ++block.references;
    }
    router->stats.accepted_bytes =
        saturating_add(router->stats.accepted_bytes, input->length);
    router->stats.accepted_blocks =
        saturating_add(router->stats.accepted_blocks, 1);
    router->stats.last_sequence = block.sequence;
    if (sequence != nullptr) {
        *sequence = block.sequence;
    }
    if (block.references == 0) {
        return_block(router, block_index);
    }
    return true;
}

deck_serial_router_copy_result_t deck_serial_router_take(
    deck_serial_router_t *router,
    deck_serial_sink_id_t sink_id,
    deck_serial_routed_block_t *output
)
{
    if (router == nullptr || output == nullptr || !valid_sink_id(sink_id)) {
        return DECK_SERIAL_ROUTER_COPY_INVALID;
    }
    std::lock_guard<std::mutex> lock(router->mutex);
    Sink *sink = find_sink(router, sink_id);
    if (sink == nullptr || sink->config.retained) {
        return DECK_SERIAL_ROUTER_COPY_INVALID;
    }
    if (sink->count == 0) {
        return DECK_SERIAL_ROUTER_COPY_EMPTY;
    }
    copy_block(router, sink_block_at(sink, 0), output);
    remove_front(router, sink, false, false, true);
    return DECK_SERIAL_ROUTER_COPY_OK;
}

deck_serial_router_copy_result_t deck_serial_router_copy_after(
    deck_serial_router_t *router,
    deck_serial_sink_id_t sink_id,
    uint64_t after_sequence,
    deck_serial_routed_block_t *output
)
{
    if (router == nullptr || output == nullptr || !valid_sink_id(sink_id)) {
        return DECK_SERIAL_ROUTER_COPY_INVALID;
    }
    std::lock_guard<std::mutex> lock(router->mutex);
    Sink *sink = find_sink(router, sink_id);
    if (sink == nullptr || !sink->config.retained) {
        return DECK_SERIAL_ROUTER_COPY_INVALID;
    }
    if (sink->count == 0) {
        return DECK_SERIAL_ROUTER_COPY_EMPTY;
    }
    size_t selected = 0;
    deck_serial_router_copy_result_t result = DECK_SERIAL_ROUTER_COPY_OK;
    if (after_sequence != 0) {
        bool found = false;
        for (size_t offset = 0; offset < sink->count; ++offset) {
            const uint32_t index = sink_block_at(sink, offset);
            if (router->metadata[index].sequence == after_sequence) {
                found = true;
                if (offset + 1U == sink->count) {
                    return DECK_SERIAL_ROUTER_COPY_EMPTY;
                }
                selected = offset + 1U;
                break;
            }
        }
        if (!found) {
            result = DECK_SERIAL_ROUTER_COPY_GAP;
        }
    }
    const uint32_t block_index = sink_block_at(sink, selected);
    copy_block(router, block_index, output);
    const size_t length = router->metadata[block_index].length;
    sink->delivered_bytes = saturating_add(sink->delivered_bytes, length);
    sink->delivered_blocks = saturating_add(sink->delivered_blocks, 1);
    return result;
}

bool deck_serial_router_clear_sink(
    deck_serial_router_t *router,
    deck_serial_sink_id_t sink_id
)
{
    if (router == nullptr || !valid_sink_id(sink_id)) {
        return false;
    }
    std::lock_guard<std::mutex> lock(router->mutex);
    Sink *sink = find_sink(router, sink_id);
    if (sink == nullptr) {
        return false;
    }
    clear_sink_locked(router, sink);
    return true;
}

void deck_serial_router_note_uart_error(
    deck_serial_router_t *router,
    deck_serial_uart_error_t error
)
{
    if (router == nullptr) {
        return;
    }
    std::lock_guard<std::mutex> lock(router->mutex);
    switch (error) {
        case DECK_SERIAL_UART_FIFO_OVERFLOW:
            router->stats.uart_fifo_overflows =
                saturating_add(router->stats.uart_fifo_overflows, 1);
            break;
        case DECK_SERIAL_UART_DRIVER_BUFFER_FULL:
            router->stats.uart_driver_buffer_full =
                saturating_add(router->stats.uart_driver_buffer_full, 1);
            break;
        case DECK_SERIAL_UART_ROUTER_STARVED:
            router->stats.router_starvations =
                saturating_add(router->stats.router_starvations, 1);
            break;
    }
}

bool deck_serial_router_sink_stats(
    deck_serial_router_t *router,
    deck_serial_sink_id_t sink_id,
    deck_serial_sink_stats_t *stats
)
{
    if (router == nullptr || stats == nullptr || !valid_sink_id(sink_id)) {
        return false;
    }
    std::lock_guard<std::mutex> lock(router->mutex);
    Sink *sink = find_sink(router, sink_id);
    if (sink == nullptr) {
        return false;
    }
    fill_sink_stats(router, sink, stats);
    return true;
}

bool deck_serial_router_stats(
    deck_serial_router_t *router,
    deck_serial_router_stats_t *stats
)
{
    if (router == nullptr || stats == nullptr) {
        return false;
    }
    std::lock_guard<std::mutex> lock(router->mutex);
    *stats = router->stats;
    return true;
}
