#include "deck_peripheral_monitor.h"

#include <array>
#include <cassert>
#include <cstddef>
#include <cstdint>
#include <cstring>

namespace {

struct FakePeripheralBus {
    std::array<uint8_t, 7> rtc_registers;
    std::array<uint8_t, 6> shtc3_response;
    bool rtc_io_ok = true;
    bool sensor_io_ok = true;
};

bool rtc_transmit_receive(
    void *context,
    const uint8_t *write_data,
    size_t write_size,
    uint8_t *read_data,
    size_t read_size
)
{
    auto *bus = static_cast<FakePeripheralBus *>(context);
    assert(write_size == 1 && write_data[0] == 0x00);
    assert(read_size == bus->rtc_registers.size());
    if (bus->rtc_io_ok) {
        std::memcpy(read_data, bus->rtc_registers.data(), read_size);
    }
    return bus->rtc_io_ok;
}

bool shtc3_transmit(void *, const uint8_t *, size_t)
{
    return true;
}

bool shtc3_receive(void *context, uint8_t *data, size_t size)
{
    auto *bus = static_cast<FakePeripheralBus *>(context);
    assert(size == bus->shtc3_response.size());
    if (bus->sensor_io_ok) {
        std::memcpy(data, bus->shtc3_response.data(), size);
    }
    return bus->sensor_io_ok;
}

void no_delay(void *, uint32_t) {}

void invalid_rtc_does_not_hide_a_healthy_temperature_sensor()
{
    FakePeripheralBus bus{
        {0x00, 0x00, 0x00, 0x00, 0xc2, 0x17, 0x09},
        {0x64, 0x8b, 0xc7, 0xa1, 0x33, 0x1c},
    };
    const deck_peripheral_monitor_config_t config = {
        {nullptr, nullptr, rtc_transmit_receive, nullptr, &bus},
        {shtc3_transmit, shtc3_receive, nullptr, no_delay, &bus},
        -40,
        1'000,
        true,
    };
    deck_peripheral_monitor_t *monitor = deck_peripheral_monitor_create(&config);
    assert(monitor != nullptr);

    assert(deck_peripheral_monitor_sample(monitor, true, true, 0));
    deck_peripheral_snapshot_t snapshot{};
    assert(deck_peripheral_monitor_snapshot(monitor, &snapshot));
    assert(!snapshot.rtc_available);
    assert(snapshot.rtc_error_count == 0);
    assert(snapshot.sensor_available);
    assert(snapshot.raw_temperature_tenths_c == 237);
    assert(snapshot.calibrated_temperature_tenths_c == 197);
    assert(snapshot.humidity_tenths_percent == 630);
    assert(snapshot.sensor_error_count == 0);
    assert(snapshot.buttons_available);

    deck_peripheral_monitor_destroy(monitor);
}

deck_peripheral_monitor_t *create_monitor(FakePeripheralBus *bus, uint32_t poll_interval_ms)
{
    const deck_peripheral_monitor_config_t config = {
        {nullptr, nullptr, rtc_transmit_receive, nullptr, bus},
        {shtc3_transmit, shtc3_receive, nullptr, no_delay, bus},
        -40,
        poll_interval_ms,
        true,
    };
    return deck_peripheral_monitor_create(&config);
}

void failed_peripherals_recover_on_the_next_due_poll()
{
    FakePeripheralBus bus{
        {0x00, 0x00, 0x00, 0x00, 0x58, 0x59, 0x23},
        {0x64, 0x8b, 0x00, 0xa1, 0x33, 0x1c},
    };
    deck_peripheral_monitor_t *monitor = create_monitor(&bus, 1'000);
    assert(monitor != nullptr);

    assert(deck_peripheral_monitor_sample(monitor, true, true, 0));
    deck_peripheral_snapshot_t snapshot{};
    assert(deck_peripheral_monitor_snapshot(monitor, &snapshot));
    assert(snapshot.rtc_available);
    assert(!snapshot.sensor_available);
    assert(snapshot.sensor_error_count == 1);

    bus.shtc3_response = {0x64, 0x8b, 0xc7, 0xa1, 0x33, 0x1c};
    assert(!deck_peripheral_monitor_sample(monitor, true, true, 999));
    assert(deck_peripheral_monitor_sample(monitor, true, true, 1'000));
    assert(deck_peripheral_monitor_snapshot(monitor, &snapshot));
    assert(snapshot.sensor_available);
    assert(snapshot.sensor_error_count == 1);

    bus.rtc_io_ok = false;
    assert(deck_peripheral_monitor_sample(monitor, true, true, 2'000));
    assert(deck_peripheral_monitor_snapshot(monitor, &snapshot));
    assert(!snapshot.rtc_available);
    assert(snapshot.rtc_error_count == 1);
    assert(snapshot.sensor_available);

    deck_peripheral_monitor_destroy(monitor);
}

void key_and_boot_events_are_counted_independently()
{
    FakePeripheralBus bus{
        {0x00, 0x00, 0x00, 0x00, 0x58, 0x59, 0x23},
        {0x64, 0x8b, 0xc7, 0xa1, 0x33, 0x1c},
    };
    deck_peripheral_monitor_t *monitor = create_monitor(&bus, 100'000);
    assert(monitor != nullptr);
    assert(deck_peripheral_monitor_sample(monitor, true, true, 0));

    assert(!deck_peripheral_monitor_sample(monitor, false, true, 100));
    assert(!deck_peripheral_monitor_sample(monitor, false, true, 120));
    assert(!deck_peripheral_monitor_sample(monitor, true, true, 500));
    assert(deck_peripheral_monitor_sample(monitor, true, true, 520));

    assert(!deck_peripheral_monitor_sample(monitor, true, false, 1'000));
    assert(!deck_peripheral_monitor_sample(monitor, true, false, 1'020));
    assert(deck_peripheral_monitor_sample(monitor, true, false, 4'000));
    assert(!deck_peripheral_monitor_sample(monitor, true, false, 4'100));

    deck_peripheral_snapshot_t snapshot{};
    assert(deck_peripheral_monitor_snapshot(monitor, &snapshot));
    assert(snapshot.key_event == DECK_BUTTON_INPUT_SHORT_PRESS);
    assert(snapshot.key_event_count == 1);
    assert(snapshot.boot_event == DECK_BUTTON_INPUT_LONG_PRESS);
    assert(snapshot.boot_event_count == 1);

    deck_peripheral_monitor_destroy(monitor);
}

void input_sampling_is_separate_from_blocking_peripheral_measurement()
{
    FakePeripheralBus bus{
        {0x00, 0x00, 0x00, 0x00, 0x58, 0x59, 0x23},
        {0x64, 0x8b, 0xc7, 0xa1, 0x33, 0x1c},
    };
    deck_peripheral_monitor_t *monitor = create_monitor(&bus, 1'000);
    assert(monitor != nullptr);
    assert(deck_peripheral_monitor_poll_due(monitor, 0));

    deck_peripheral_measurement_t measurement{};
    assert(deck_peripheral_monitor_measure(monitor, &measurement));

    assert(!deck_peripheral_monitor_sample_inputs(monitor, false, true, 100));
    assert(!deck_peripheral_monitor_sample_inputs(monitor, false, true, 120));
    assert(!deck_peripheral_monitor_sample_inputs(monitor, true, true, 300));
    assert(deck_peripheral_monitor_sample_inputs(monitor, true, true, 320));
    assert(deck_peripheral_monitor_apply(monitor, &measurement));

    deck_peripheral_snapshot_t snapshot{};
    assert(deck_peripheral_monitor_snapshot(monitor, &snapshot));
    assert(snapshot.key_event == DECK_BUTTON_INPUT_SHORT_PRESS);
    assert(snapshot.key_event_count == 1);
    assert(snapshot.rtc_available);
    assert(snapshot.sensor_available);

    deck_peripheral_monitor_destroy(monitor);
}

void temperature_offset_updates_the_current_calibrated_snapshot()
{
    FakePeripheralBus bus{
        {0x00, 0x00, 0x00, 0x00, 0x58, 0x59, 0x23},
        {0x64, 0x8b, 0xc7, 0xa1, 0x33, 0x1c},
    };
    deck_peripheral_monitor_t *monitor = create_monitor(&bus, 1'000);
    assert(monitor != nullptr);
    assert(deck_peripheral_monitor_sample(monitor, true, true, 0));

    assert(deck_peripheral_monitor_set_temperature_offset(monitor, -35));
    deck_peripheral_snapshot_t snapshot{};
    assert(deck_peripheral_monitor_snapshot(monitor, &snapshot));
    assert(snapshot.raw_temperature_tenths_c == 237);
    assert(snapshot.calibrated_temperature_tenths_c == 202);
    deck_peripheral_monitor_destroy(monitor);
}

}  // namespace

int main()
{
    invalid_rtc_does_not_hide_a_healthy_temperature_sensor();
    failed_peripherals_recover_on_the_next_due_poll();
    key_and_boot_events_are_counted_independently();
    input_sampling_is_separate_from_blocking_peripheral_measurement();
    temperature_offset_updates_the_current_calibrated_snapshot();
    return 0;
}
