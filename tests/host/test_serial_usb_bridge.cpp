#include "deck_serial_usb_bridge.h"

#include <assert.h>
#include <stdlib.h>
#include <string.h>

#include <algorithm>
#include <array>
#include <limits>
#include <vector>

namespace {

struct Memory {
    size_t allocations = 0;
    size_t frees = 0;
    bool fail = false;
};

void *allocate(void *context, size_t size)
{
    auto *memory = static_cast<Memory *>(context);
    if (memory->fail) {
        return nullptr;
    }
    ++memory->allocations;
    return calloc(1, size);
}

void deallocate(void *context, void *value)
{
    ++static_cast<Memory *>(context)->frees;
    free(value);
}

struct FakeIo {
    deck_serial_router_t *router = nullptr;
    bool connected = false;
    bool input_ready = true;
    uint64_t input_authority_generation = 1;
    bool submit_succeeds = true;
    bool change_authority_during_read = false;
    uint64_t authority_after_read_start = 0;
    size_t write_limit = std::numeric_limits<size_t>::max();
    size_t write_calls = 0;
    size_t read_calls = 0;
    std::vector<uint8_t> output;
    std::vector<uint8_t> input;
    std::vector<uint8_t> submitted;
    std::vector<uint64_t> submitted_authority;
};

bool connected(void *context)
{
    return static_cast<FakeIo *>(context)->connected;
}

deck_serial_router_copy_result_t take_output(
    void *context,
    deck_serial_routed_block_t *block
)
{
    auto *io = static_cast<FakeIo *>(context);
    return deck_serial_router_take(io->router, DECK_SERIAL_SINK_USB, block);
}

int write_output(void *context, const uint8_t *bytes, size_t size)
{
    auto *io = static_cast<FakeIo *>(context);
    ++io->write_calls;
    const size_t written = std::min(size, io->write_limit);
    io->output.insert(io->output.end(), bytes, bytes + written);
    return static_cast<int>(written);
}

bool input_ready(void *context)
{
    return static_cast<FakeIo *>(context)->input_ready;
}

uint64_t input_authority_generation(void *context)
{
    return static_cast<FakeIo *>(context)->input_authority_generation;
}

int read_input(void *context, uint8_t *bytes, size_t capacity)
{
    auto *io = static_cast<FakeIo *>(context);
    ++io->read_calls;
    if (io->change_authority_during_read) {
        io->input_authority_generation = io->authority_after_read_start;
    }
    const size_t read = std::min(capacity, io->input.size());
    std::copy_n(io->input.begin(), read, bytes);
    io->input.erase(io->input.begin(), io->input.begin() + static_cast<ptrdiff_t>(read));
    return static_cast<int>(read);
}

bool submit_input(
    void *context,
    const uint8_t *bytes,
    size_t size,
    uint64_t authority_generation
)
{
    auto *io = static_cast<FakeIo *>(context);
    if (!io->submit_succeeds) {
        return false;
    }
    io->submitted.insert(io->submitted.end(), bytes, bytes + size);
    io->submitted_authority.push_back(authority_generation);
    return true;
}

void *router_allocate(void *, size_t size, bool)
{
    return calloc(1, size);
}

void router_deallocate(void *, void *value)
{
    free(value);
}

deck_serial_router_t *make_router(
    size_t usb_capacity = DECK_SERIAL_ROUTER_BLOCK_BYTES
)
{
    const deck_serial_router_config_t router_config = {
        9,
        DECK_SERIAL_HISTORY_MIN_BYTES,
        0,
        {router_allocate, router_deallocate, nullptr},
    };
    deck_serial_router_t *router = deck_serial_router_create(&router_config);
    assert(router != nullptr);
    const deck_serial_sink_config_t usb = {
        DECK_SERIAL_SINK_USB,
        usb_capacity,
        false,
    };
    assert(deck_serial_router_register_sink(router, &usb));
    return router;
}

deck_serial_usb_bridge_t *make_bridge(Memory *memory, FakeIo *io);
void submit_bytes(
    deck_serial_router_t *router,
    const std::array<uint8_t, 256> &bytes
);

void long_burst_preserves_every_byte_under_partial_progress()
{
    Memory memory;
    FakeIo io;
    constexpr size_t kBurstBytes = 16U * 1024U;
    io.router = make_router(kBurstBytes);
    deck_serial_usb_bridge_t *bridge = make_bridge(&memory, &io);
    assert(bridge != nullptr);
    io.connected = true;
    io.write_limit = 7;

    std::vector<uint8_t> expected;
    expected.reserve(kBurstBytes);
    for (size_t offset = 0; offset < kBurstBytes; offset += 256U) {
        std::array<uint8_t, 256> block{};
        for (size_t index = 0; index < block.size(); ++index) {
            block[index] = static_cast<uint8_t>((offset + index) & 0xffU);
        }
        submit_bytes(io.router, block);
        expected.insert(expected.end(), block.begin(), block.end());
    }
    while (io.output.size() != expected.size()) {
        assert(deck_serial_usb_bridge_pump_output(bridge) == DECK_SERIAL_USB_PROGRESS);
    }
    assert(io.output == expected);

    deck_serial_usb_bridge_stats_t stats{};
    assert(deck_serial_usb_bridge_stats(bridge, &stats));
    assert(stats.output_bytes == kBurstBytes);
    assert(stats.output_blocks == kBurstBytes / 256U);
    deck_serial_usb_bridge_destroy(bridge);
    deck_serial_router_destroy(io.router);
    assert(memory.allocations == memory.frees);
}

deck_serial_usb_bridge_t *make_bridge(Memory *memory, FakeIo *io)
{
    const deck_serial_usb_bridge_config_t config = {
        {connected, take_output, write_output, input_ready,
         input_authority_generation,
         read_input, submit_input, io},
        {allocate, deallocate, memory},
    };
    return deck_serial_usb_bridge_create(&config);
}

void submit_bytes(deck_serial_router_t *router, const std::array<uint8_t, 256> &bytes)
{
    deck_serial_input_block_t input{};
    input.monotonic_ms = 123;
    input.length = static_cast<uint16_t>(bytes.size());
    memcpy(input.bytes, bytes.data(), bytes.size());
    assert(deck_serial_router_submit(router, &input, nullptr));
}

void output_survives_disconnect_zero_write_and_partial_write()
{
    Memory memory;
    FakeIo io;
    io.router = make_router();
    deck_serial_usb_bridge_t *bridge = make_bridge(&memory, &io);
    assert(bridge != nullptr);

    std::array<uint8_t, 256> expected{};
    for (size_t index = 0; index < expected.size(); ++index) {
        expected[index] = static_cast<uint8_t>(index);
    }
    submit_bytes(io.router, expected);

    assert(deck_serial_usb_bridge_pump_output(bridge) == DECK_SERIAL_USB_IDLE);
    deck_serial_sink_stats_t sink{};
    assert(deck_serial_router_sink_stats(io.router, DECK_SERIAL_SINK_USB, &sink));
    assert(sink.queued_blocks == 1);
    assert(io.write_calls == 0);

    io.connected = true;
    io.write_limit = 17;
    assert(deck_serial_usb_bridge_pump_output(bridge) == DECK_SERIAL_USB_PROGRESS);
    assert(io.output.size() == 17);
    assert(deck_serial_router_sink_stats(io.router, DECK_SERIAL_SINK_USB, &sink));
    assert(sink.queued_blocks == 0);

    io.connected = false;
    assert(deck_serial_usb_bridge_pump_output(bridge) == DECK_SERIAL_USB_IDLE);
    io.connected = true;
    io.write_limit = 0;
    assert(deck_serial_usb_bridge_pump_output(bridge) == DECK_SERIAL_USB_BACKPRESSURE);
    io.write_limit = 31;
    while (io.output.size() != expected.size()) {
        assert(deck_serial_usb_bridge_pump_output(bridge) == DECK_SERIAL_USB_PROGRESS);
    }
    assert(io.output == std::vector<uint8_t>(expected.begin(), expected.end()));

    deck_serial_usb_bridge_stats_t stats{};
    assert(deck_serial_usb_bridge_stats(bridge, &stats));
    assert(stats.output_blocks == 1);
    assert(stats.output_bytes == expected.size());
    assert(stats.output_backpressure == 1);
    assert(stats.disconnect_observations >= 2);

    deck_serial_usb_bridge_destroy(bridge);
    deck_serial_router_destroy(io.router);
    assert(memory.allocations == memory.frees);
}

void exit_discards_a_partially_written_output_block()
{
    Memory memory;
    FakeIo io;
    io.router = make_router();
    deck_serial_usb_bridge_t *bridge = make_bridge(&memory, &io);
    assert(bridge != nullptr);
    io.connected = true;
    io.write_limit = 13;

    std::array<uint8_t, 256> expected{};
    for (size_t index = 0; index < expected.size(); ++index) {
        expected[index] = static_cast<uint8_t>(index);
    }
    submit_bytes(io.router, expected);
    assert(deck_serial_usb_bridge_pump_output(bridge) == DECK_SERIAL_USB_PROGRESS);
    assert(io.output.size() == 13);

    io.connected = false;
    assert(deck_serial_usb_bridge_pump_output(bridge) == DECK_SERIAL_USB_IDLE);
    deck_serial_usb_bridge_destroy(bridge);
    deck_serial_router_destroy(io.router);
    assert(io.output.size() == 13);
    assert(memory.allocations == memory.frees);
}

void input_is_raw_and_waits_for_owner_queue_capacity()
{
    Memory memory;
    FakeIo io;
    io.router = make_router();
    deck_serial_usb_bridge_t *bridge = make_bridge(&memory, &io);
    assert(bridge != nullptr);
    io.connected = true;
    io.input_ready = false;
    for (size_t index = 0; index < 256; ++index) {
        io.input.push_back(static_cast<uint8_t>(255U - index));
    }

    assert(deck_serial_usb_bridge_pump_input(bridge) == DECK_SERIAL_USB_BACKPRESSURE);
    assert(io.read_calls == 0);
    io.input_ready = true;
    io.input_authority_generation = 41;
    assert(deck_serial_usb_bridge_pump_input(bridge) == DECK_SERIAL_USB_PROGRESS);
    assert(io.submitted.size() == 256);
    for (size_t index = 0; index < 256; ++index) {
        assert(io.submitted[index] == static_cast<uint8_t>(255U - index));
    }
    assert(io.submitted_authority.size() == 1);
    assert(io.submitted_authority[0] == 41);

    io.input = {0x00, 0xff, 0xc3, 0x28};
    io.input_authority_generation = 43;
    io.submit_succeeds = false;
    assert(deck_serial_usb_bridge_pump_input(bridge) == DECK_SERIAL_USB_ERROR);
    deck_serial_usb_bridge_stats_t stats{};
    assert(deck_serial_usb_bridge_stats(bridge, &stats));
    assert(stats.input_blocks == 1);
    assert(stats.input_bytes == 256);
    assert(stats.input_backpressure == 1);
    assert(stats.input_submit_failures == 1);

    deck_serial_usb_bridge_destroy(bridge);
    deck_serial_router_destroy(io.router);
    assert(memory.allocations == memory.frees);
}

void input_carries_the_generation_captured_before_a_blocking_read()
{
    Memory memory;
    FakeIo io;
    io.router = make_router();
    deck_serial_usb_bridge_t *bridge = make_bridge(&memory, &io);
    assert(bridge != nullptr);
    io.connected = true;
    io.input_authority_generation = 71;
    io.change_authority_during_read = true;
    io.authority_after_read_start = 73;
    io.input = {0x00, 0xff, 0xc3, 0x28};

    assert(deck_serial_usb_bridge_pump_input(bridge) == DECK_SERIAL_USB_PROGRESS);
    assert(io.submitted == std::vector<uint8_t>({0x00, 0xff, 0xc3, 0x28}));
    assert(io.submitted_authority == std::vector<uint64_t>({71}));

    deck_serial_usb_bridge_destroy(bridge);
    deck_serial_router_destroy(io.router);
    assert(memory.allocations == memory.frees);
}

void invalid_adapter_and_oom_fail_without_leaks()
{
    Memory memory;
    FakeIo io;
    io.router = make_router();
    deck_serial_usb_bridge_config_t invalid{};
    assert(deck_serial_usb_bridge_create(&invalid) == nullptr);

    memory.fail = true;
    assert(make_bridge(&memory, &io) == nullptr);
    assert(memory.allocations == memory.frees);
    deck_serial_router_destroy(io.router);
}

}  // namespace

int main()
{
    output_survives_disconnect_zero_write_and_partial_write();
    exit_discards_a_partially_written_output_block();
    long_burst_preserves_every_byte_under_partial_progress();
    input_is_raw_and_waits_for_owner_queue_capacity();
    input_carries_the_generation_captured_before_a_blocking_read();
    invalid_adapter_and_oom_fail_without_leaks();
    return 0;
}
