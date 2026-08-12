#include "deck_shtc3.h"

#include <array>
#include <cassert>
#include <cstddef>
#include <cstdint>
#include <cstring>
#include <limits>
#include <vector>

namespace {

struct FakeShtc3Device {
    std::vector<std::array<uint8_t, 6>> responses;
    std::vector<std::array<uint8_t, 2>> commands;
    std::vector<uint32_t> delays_us;
    size_t receive_index;
    size_t failing_command_index = std::numeric_limits<size_t>::max();
};

bool transmit(void *context, const uint8_t *data, size_t size)
{
    auto *device = static_cast<FakeShtc3Device *>(context);
    assert(size == 2);
    const size_t command_index = device->commands.size();
    device->commands.push_back({data[0], data[1]});
    return command_index != device->failing_command_index;
}

bool receive(void *context, uint8_t *data, size_t size)
{
    auto *device = static_cast<FakeShtc3Device *>(context);
    assert(device->receive_index < device->responses.size());
    const auto &response = device->responses[device->receive_index++];
    assert(size == response.size());
    std::memcpy(data, response.data(), size);
    return true;
}

void delay_us(void *context, uint32_t duration_us)
{
    auto *device = static_cast<FakeShtc3Device *>(context);
    device->delays_us.push_back(duration_us);
}

void datasheet_sample_is_converted_and_sensor_returns_to_sleep()
{
    FakeShtc3Device device{{{0x64, 0x8b, 0xc7, 0xa1, 0x33, 0x1c}}, {}, {}, 0};
    const deck_i2c_device_t adapter = {transmit, receive, nullptr, delay_us, &device};
    deck_shtc3_sample_t sample{};

    assert(deck_shtc3_measure(adapter, 1, &sample) == DECK_SHTC3_OK);
    assert(sample.raw_temperature_tenths_c == 237);
    assert(sample.humidity_tenths_percent == 630);
    const std::vector<std::array<uint8_t, 2>> expected_commands = {
        {0x35, 0x17},
        {0x78, 0x66},
        {0xb0, 0x98},
    };
    assert(device.commands == expected_commands);
    assert((device.delays_us == std::vector<uint32_t>{250, 12'100}));
}

void crc_failure_sleeps_before_retrying_the_full_measurement()
{
    FakeShtc3Device device{
        {
            {0x64, 0x8b, 0x00, 0xa1, 0x33, 0x1c},
            {0x64, 0x8b, 0xc7, 0xa1, 0x33, 0x1c},
        },
        {},
        {},
        0,
    };
    const deck_i2c_device_t adapter = {transmit, receive, nullptr, delay_us, &device};
    deck_shtc3_sample_t sample{};

    assert(deck_shtc3_measure(adapter, 2, &sample) == DECK_SHTC3_OK);
    assert(sample.raw_temperature_tenths_c == 237);
    const std::vector<std::array<uint8_t, 2>> expected_commands = {
        {0x35, 0x17}, {0x78, 0x66}, {0xb0, 0x98},
        {0x35, 0x17}, {0x78, 0x66}, {0xb0, 0x98},
    };
    assert(device.commands == expected_commands);
}

void measurement_failure_still_attempts_sleep()
{
    FakeShtc3Device device{{}, {}, {}, 0, 1};
    const deck_i2c_device_t adapter = {transmit, receive, nullptr, delay_us, &device};
    deck_shtc3_sample_t sample{};

    assert(deck_shtc3_measure(adapter, 1, &sample) == DECK_SHTC3_IO_ERROR);
    const std::vector<std::array<uint8_t, 2>> expected_commands = {
        {0x35, 0x17},
        {0x78, 0x66},
        {0xb0, 0x98},
    };
    assert(device.commands == expected_commands);
}

void sleep_failure_is_reported_after_a_valid_sample()
{
    FakeShtc3Device device{{{0x64, 0x8b, 0xc7, 0xa1, 0x33, 0x1c}}, {}, {}, 0, 2};
    const deck_i2c_device_t adapter = {transmit, receive, nullptr, delay_us, &device};
    deck_shtc3_sample_t sample{};

    assert(deck_shtc3_measure(adapter, 1, &sample) == DECK_SHTC3_SLEEP_ERROR);
}

void wake_failure_still_attempts_to_restore_sleep_state()
{
    FakeShtc3Device device{{}, {}, {}, 0, 0};
    const deck_i2c_device_t adapter = {transmit, receive, nullptr, delay_us, &device};
    deck_shtc3_sample_t sample{};

    assert(deck_shtc3_measure(adapter, 1, &sample) == DECK_SHTC3_IO_ERROR);
    const std::vector<std::array<uint8_t, 2>> expected_commands = {
        {0x35, 0x17},
        {0xb0, 0x98},
    };
    assert(device.commands == expected_commands);
}

void conversion_uses_the_datasheet_power_of_two_denominator()
{
    FakeShtc3Device device{{{0x09, 0x4a, 0xa4, 0x0c, 0xac, 0xb4}}, {}, {}, 0};
    const deck_i2c_device_t adapter = {transmit, receive, nullptr, delay_us, &device};
    deck_shtc3_sample_t sample{};

    assert(deck_shtc3_measure(adapter, 1, &sample) == DECK_SHTC3_OK);
    assert(sample.raw_temperature_tenths_c == -387);
    assert(sample.humidity_tenths_percent == 49);
}

}  // namespace

int main()
{
    datasheet_sample_is_converted_and_sensor_returns_to_sleep();
    crc_failure_sleeps_before_retrying_the_full_measurement();
    measurement_failure_still_attempts_sleep();
    sleep_failure_is_reported_after_a_valid_sample();
    wake_failure_still_attempts_to_restore_sleep_state();
    conversion_uses_the_datasheet_power_of_two_denominator();
    return 0;
}
