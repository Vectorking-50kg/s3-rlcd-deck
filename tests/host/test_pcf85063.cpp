#include "deck_pcf85063.h"

#include <array>
#include <cassert>
#include <cstddef>
#include <cstdint>
#include <cstring>

namespace {

struct FakeRtcDevice {
    std::array<uint8_t, 7> registers;
    bool io_ok = true;
};

bool transmit_receive(
    void *context,
    const uint8_t *write_data,
    size_t write_size,
    uint8_t *read_data,
    size_t read_size
)
{
    auto *device = static_cast<FakeRtcDevice *>(context);
    assert(write_size == 1);
    assert(write_data[0] == 0x00);
    assert(read_size == device->registers.size());
    if (device->io_ok) {
        std::memcpy(read_data, device->registers.data(), read_size);
    }
    return device->io_ok;
}

void oscillator_stop_flag_makes_time_unavailable()
{
    FakeRtcDevice device{{0x00, 0x00, 0x00, 0x00, 0xc2, 0x17, 0x09}};
    const deck_i2c_device_t adapter = {nullptr, nullptr, transmit_receive, nullptr, &device};
    deck_rtc_time_t time{};

    assert(deck_pcf85063_read(adapter, &time) == DECK_RTC_CLOCK_INVALID);
    assert(!time.clock_integrity_guaranteed);
}

void valid_24_hour_bcd_time_is_decoded()
{
    FakeRtcDevice device{{0x00, 0x00, 0x00, 0x00, 0x58, 0x59, 0x23}};
    const deck_i2c_device_t adapter = {nullptr, nullptr, transmit_receive, nullptr, &device};
    deck_rtc_time_t time{};

    assert(deck_pcf85063_read(adapter, &time) == DECK_RTC_OK);
    assert(time.clock_integrity_guaranteed);
    assert(time.hour == 23);
    assert(time.minute == 59);
    assert(time.second == 58);
}

void valid_12_hour_pm_time_is_converted_to_24_hour_time()
{
    FakeRtcDevice device{{0x02, 0x00, 0x00, 0x00, 0x42, 0x05, 0x31}};
    const deck_i2c_device_t adapter = {nullptr, nullptr, transmit_receive, nullptr, &device};
    deck_rtc_time_t time{};

    assert(deck_pcf85063_read(adapter, &time) == DECK_RTC_OK);
    assert(time.hour == 23);
    assert(time.minute == 5);
    assert(time.second == 42);
}

void malformed_bcd_and_i2c_failure_are_distinct()
{
    FakeRtcDevice malformed{{0x00, 0x00, 0x00, 0x00, 0x58, 0x7a, 0x23}};
    const deck_i2c_device_t malformed_adapter = {
        nullptr,
        nullptr,
        transmit_receive,
        nullptr,
        &malformed,
    };
    deck_rtc_time_t time{};
    assert(deck_pcf85063_read(malformed_adapter, &time) == DECK_RTC_DATA_INVALID);

    FakeRtcDevice disconnected{{}, false};
    const deck_i2c_device_t disconnected_adapter = {
        nullptr,
        nullptr,
        transmit_receive,
        nullptr,
        &disconnected,
    };
    assert(deck_pcf85063_read(disconnected_adapter, &time) == DECK_RTC_IO_ERROR);
}

}  // namespace

int main()
{
    oscillator_stop_flag_makes_time_unavailable();
    valid_24_hour_bcd_time_is_decoded();
    valid_12_hour_pm_time_is_converted_to_24_hour_time();
    malformed_bcd_and_i2c_failure_are_distinct();
    return 0;
}
