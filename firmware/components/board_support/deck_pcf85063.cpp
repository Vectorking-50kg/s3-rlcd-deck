#include "deck_pcf85063.h"

#include <stddef.h>
#include <stdint.h>

namespace {

constexpr uint8_t kControl1Register = 0x00;
constexpr size_t kRegistersThroughHours = 7;
constexpr size_t kSecondsIndex = 4;
constexpr size_t kMinutesIndex = 5;
constexpr size_t kHoursIndex = 6;
constexpr uint8_t kOscillatorStopMask = 0x80;
constexpr uint8_t k12HourModeMask = 0x02;

bool decode_bcd(uint8_t value, uint8_t mask, uint8_t maximum, uint8_t *decoded)
{
    const uint8_t masked = value & mask;
    const uint8_t units = masked & 0x0f;
    const uint8_t tens = (masked >> 4) & 0x0f;
    const uint8_t result = static_cast<uint8_t>(tens * 10U + units);
    if (units > 9 || result > maximum) {
        return false;
    }
    *decoded = result;
    return true;
}

}  // namespace

deck_rtc_result_t deck_pcf85063_read(deck_i2c_device_t device, deck_rtc_time_t *time)
{
    if (time == nullptr || device.transmit_receive == nullptr) {
        return DECK_RTC_INVALID_ARGUMENT;
    }
    *time = {};
    uint8_t registers[kRegistersThroughHours] = {};
    if (!device.transmit_receive(
            device.context,
            &kControl1Register,
            1,
            registers,
            sizeof(registers)
        )) {
        return DECK_RTC_IO_ERROR;
    }
    if ((registers[kSecondsIndex] & kOscillatorStopMask) != 0) {
        return DECK_RTC_CLOCK_INVALID;
    }
    time->clock_integrity_guaranteed = true;
    if (!decode_bcd(registers[kSecondsIndex], 0x7f, 59, &time->second) ||
        !decode_bcd(registers[kMinutesIndex], 0x7f, 59, &time->minute)) {
        return DECK_RTC_DATA_INVALID;
    }
    if ((registers[0] & k12HourModeMask) != 0) {
        uint8_t hour12 = 0;
        if (!decode_bcd(registers[kHoursIndex], 0x1f, 12, &hour12) || hour12 == 0) {
            return DECK_RTC_DATA_INVALID;
        }
        time->hour = static_cast<uint8_t>(hour12 % 12U);
        if ((registers[kHoursIndex] & 0x20U) != 0) {
            time->hour = static_cast<uint8_t>(time->hour + 12U);
        }
    } else if (!decode_bcd(registers[kHoursIndex], 0x3f, 23, &time->hour)) {
        return DECK_RTC_DATA_INVALID;
    }
    return DECK_RTC_OK;
}
