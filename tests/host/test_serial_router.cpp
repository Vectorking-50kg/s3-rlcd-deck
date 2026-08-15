#include "deck_serial_router.h"

#include <assert.h>
#include <stdlib.h>
#include <string.h>

#include <limits>

namespace {

struct Memory {
    size_t allocations = 0;
    size_t frees = 0;
    size_t external_bytes = 0;
    size_t fail_after = std::numeric_limits<size_t>::max();
};

void *allocate(void *context, size_t size, bool external)
{
    auto *memory = static_cast<Memory *>(context);
    if (memory->allocations >= memory->fail_after) {
        return nullptr;
    }
    ++memory->allocations;
    if (external) {
        memory->external_bytes += size;
    }
    return calloc(1, size);
}

void deallocate(void *context, void *value)
{
    ++static_cast<Memory *>(context)->frees;
    free(value);
}

deck_serial_router_t *make_router(
    Memory *memory,
    size_t capacity = DECK_SERIAL_HISTORY_MIN_BYTES,
    uint64_t initial_sequence = 0
)
{
    const deck_serial_router_config_t config = {
        17,
        capacity,
        initial_sequence,
        {allocate, deallocate, memory},
    };
    return deck_serial_router_create(&config);
}

deck_serial_input_block_t input(uint8_t marker, uint64_t time, uint16_t length = 8)
{
    deck_serial_input_block_t block{};
    block.monotonic_ms = time;
    block.length = length;
    for (uint16_t index = 0; index < length; ++index) {
        block.bytes[index] = static_cast<uint8_t>(marker + index);
    }
    return block;
}

deck_serial_sink_stats_t sink_stats(
    deck_serial_router_t *router,
    deck_serial_sink_id_t sink
)
{
    deck_serial_sink_stats_t stats{};
    assert(deck_serial_router_sink_stats(router, sink, &stats));
    return stats;
}

bool register_sink(
    deck_serial_router_t *router,
    deck_serial_sink_id_t id,
    size_t capacity,
    bool retained
)
{
    const deck_serial_sink_config_t config = {id, capacity, retained};
    return deck_serial_router_register_sink(router, &config);
}

void independent_sinks_never_block_or_overwrite_each_other()
{
    Memory memory;
    deck_serial_router_t *router = make_router(&memory);
    assert(router != nullptr);
    assert(memory.external_bytes > DECK_SERIAL_HISTORY_MIN_BYTES);
    assert(register_sink(router, DECK_SERIAL_SINK_USB, 512, false));
    assert(register_sink(router, DECK_SERIAL_SINK_WSS, 512, false));
    assert(register_sink(
        router,
        DECK_SERIAL_SINK_HISTORY,
        DECK_SERIAL_HISTORY_MIN_BYTES,
        true
    ));
    assert(register_sink(router, DECK_SERIAL_SINK_STATS, 256, false));

    for (uint16_t index = 0; index < 260; ++index) {
        const auto block = input(static_cast<uint8_t>(index), index, 256);
        uint64_t sequence = 0;
        assert(deck_serial_router_submit(router, &block, &sequence));
        assert(sequence == static_cast<uint64_t>(index) + 1U);

        deck_serial_routed_block_t delivered{};
        assert(
            deck_serial_router_take(router, DECK_SERIAL_SINK_USB, &delivered) ==
            DECK_SERIAL_ROUTER_COPY_OK
        );
        assert(delivered.session_id == 17);
        assert(delivered.sequence == sequence);
        assert(delivered.monotonic_ms == index);
        assert(delivered.length == block.length);
        assert(memcmp(delivered.bytes, block.bytes, block.length) == 0);
    }

    const auto usb = sink_stats(router, DECK_SERIAL_SINK_USB);
    const auto wss = sink_stats(router, DECK_SERIAL_SINK_WSS);
    const auto history = sink_stats(router, DECK_SERIAL_SINK_HISTORY);
    const auto stats = sink_stats(router, DECK_SERIAL_SINK_STATS);
    assert(usb.overwritten_blocks == 0);
    assert(usb.delivered_blocks == 260);
    assert(wss.overwritten_blocks == 258);
    assert(wss.queued_blocks == 2);
    assert(history.overwritten_blocks == 4);
    assert(history.queued_blocks == 256);
    assert(stats.overwritten_blocks == 259);
    assert(stats.queued_blocks == 1);

    deck_serial_routed_block_t recovered{};
    assert(
        deck_serial_router_copy_after(
            router,
            DECK_SERIAL_SINK_HISTORY,
            0,
            &recovered
        ) == DECK_SERIAL_ROUTER_COPY_OK
    );
    assert(recovered.sequence == 5);
    assert(
        deck_serial_router_copy_after(
            router,
            DECK_SERIAL_SINK_HISTORY,
            1,
            &recovered
        ) == DECK_SERIAL_ROUTER_COPY_GAP
    );
    assert(recovered.sequence == 5);

    deck_serial_router_destroy(router);
    assert(memory.allocations == memory.frees);
}

void wrap_reconnect_and_error_counters_are_stable()
{
    Memory memory;
    deck_serial_router_t *router = make_router(
        &memory,
        DECK_SERIAL_HISTORY_MIN_BYTES,
        std::numeric_limits<uint64_t>::max() - 1U
    );
    assert(router != nullptr);
    assert(register_sink(
        router,
        DECK_SERIAL_SINK_HISTORY,
        DECK_SERIAL_HISTORY_MIN_BYTES,
        true
    ));

    auto first = input(0x10, 100, 3);
    auto second = input(0x20, 200, 4);
    uint64_t first_sequence = 0;
    uint64_t second_sequence = 0;
    assert(deck_serial_router_submit(router, &first, &first_sequence));
    assert(deck_serial_router_submit(router, &second, &second_sequence));
    assert(first_sequence == std::numeric_limits<uint64_t>::max());
    assert(second_sequence == 1);

    deck_serial_routed_block_t recovered{};
    assert(
        deck_serial_router_copy_after(
            router,
            DECK_SERIAL_SINK_HISTORY,
            first_sequence,
            &recovered
        ) == DECK_SERIAL_ROUTER_COPY_OK
    );
    assert(recovered.sequence == second_sequence);
    assert(recovered.monotonic_ms == 200);
    assert(recovered.length == 4);
    assert(memcmp(recovered.bytes, second.bytes, second.length) == 0);

    deck_serial_router_note_uart_error(router, DECK_SERIAL_UART_FIFO_OVERFLOW);
    deck_serial_router_note_uart_error(router, DECK_SERIAL_UART_DRIVER_BUFFER_FULL);
    deck_serial_router_note_uart_error(router, DECK_SERIAL_UART_ROUTER_STARVED);
    deck_serial_router_stats_t stats{};
    assert(deck_serial_router_stats(router, &stats));
    assert(stats.accepted_blocks == 2);
    assert(stats.accepted_bytes == 7);
    assert(stats.last_sequence == 1);
    assert(stats.uart_fifo_overflows == 1);
    assert(stats.uart_driver_buffer_full == 1);
    assert(stats.router_starvations == 1);

    assert(deck_serial_router_clear_sink(router, DECK_SERIAL_SINK_HISTORY));
    const auto cleared = sink_stats(router, DECK_SERIAL_SINK_HISTORY);
    assert(cleared.queued_blocks == 0);
    assert(cleared.cleared_blocks == 2);
    assert(cleared.cleared_bytes == 7);
    deck_serial_router_destroy(router);
    assert(memory.allocations == memory.frees);
}

void invalid_configuration_and_oom_fail_without_leaks()
{
    Memory memory;
    auto *router = make_router(&memory, 257);
    assert(router == nullptr);
    assert(memory.allocations == memory.frees);

    memory = {};
    memory.fail_after = 1;
    router = make_router(&memory);
    assert(router == nullptr);
    assert(memory.allocations == memory.frees);

    memory = {};
    router = make_router(&memory);
    assert(router != nullptr);
    assert(!register_sink(
        router,
        DECK_SERIAL_SINK_HISTORY,
        DECK_SERIAL_HISTORY_MIN_BYTES - DECK_SERIAL_ROUTER_BLOCK_BYTES,
        true
    ));
    assert(!register_sink(router, DECK_SERIAL_SINK_USB, 256, true));
    memory.fail_after = memory.allocations;
    const deck_serial_sink_config_t sink = {
        DECK_SERIAL_SINK_HISTORY,
        DECK_SERIAL_HISTORY_MIN_BYTES,
        true,
    };
    assert(!deck_serial_router_register_sink(router, &sink));
    deck_serial_router_destroy(router);
    assert(memory.allocations == memory.frees);
}

void stalled_sinks_do_not_allocate_or_slow_the_drained_sink()
{
    Memory memory;
    deck_serial_router_t *router = make_router(&memory);
    assert(router != nullptr);
    assert(register_sink(router, DECK_SERIAL_SINK_USB, 256, false));
    assert(register_sink(router, DECK_SERIAL_SINK_WSS, 256, false));
    assert(register_sink(
        router,
        DECK_SERIAL_SINK_HISTORY,
        DECK_SERIAL_HISTORY_MIN_BYTES,
        true
    ));
    assert(register_sink(router, DECK_SERIAL_SINK_STATS, 256, false));
    const size_t fixed_allocations = memory.allocations;

    constexpr uint32_t kStressBlocks = 20'000;
    for (uint32_t index = 0; index < kStressBlocks; ++index) {
        const auto block = input(static_cast<uint8_t>(index), index, 256);
        assert(deck_serial_router_submit(router, &block, nullptr));
        deck_serial_routed_block_t delivered{};
        assert(
            deck_serial_router_take(router, DECK_SERIAL_SINK_USB, &delivered) ==
            DECK_SERIAL_ROUTER_COPY_OK
        );
        assert(delivered.sequence == static_cast<uint64_t>(index) + 1U);
    }
    assert(memory.allocations == fixed_allocations);
    assert(sink_stats(router, DECK_SERIAL_SINK_USB).overwritten_blocks == 0);
    assert(
        sink_stats(router, DECK_SERIAL_SINK_WSS).overwritten_blocks ==
        kStressBlocks - 1U
    );
    assert(
        sink_stats(router, DECK_SERIAL_SINK_STATS).overwritten_blocks ==
        kStressBlocks - 1U
    );
    assert(
        sink_stats(router, DECK_SERIAL_SINK_HISTORY).overwritten_blocks ==
        kStressBlocks -
            DECK_SERIAL_HISTORY_MIN_BYTES / DECK_SERIAL_ROUTER_BLOCK_BYTES
    );
    deck_serial_router_destroy(router);
    assert(memory.allocations == memory.frees);
}

}  // namespace

int main()
{
    independent_sinks_never_block_or_overwrite_each_other();
    wrap_reconnect_and_error_counters_are_stable();
    invalid_configuration_and_oom_fail_without_leaks();
    stalled_sinks_do_not_allocate_or_slow_the_drained_sink();
    return 0;
}
