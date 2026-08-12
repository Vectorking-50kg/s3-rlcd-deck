#include "deck_shtc3.h"

#include <stddef.h>
#include <stdint.h>

namespace {

constexpr uint8_t kWakeCommand[] = {0x35, 0x17};
constexpr uint8_t kMeasureTemperatureFirstCommand[] = {0x78, 0x66};
constexpr uint8_t kSleepCommand[] = {0xb0, 0x98};
constexpr uint32_t kWakeDelayUs = 250;
constexpr uint32_t kMeasurementDelayUs = 12'100;

uint8_t crc8(const uint8_t *data, size_t size)
{
    uint8_t crc = 0xff;
    for (size_t index = 0; index < size; ++index) {
        crc ^= data[index];
        for (uint8_t bit = 0; bit < 8; ++bit) {
            crc = (crc & 0x80U) != 0
                      ? static_cast<uint8_t>(static_cast<uint8_t>(crc << 1U) ^ 0x31U)
                      : static_cast<uint8_t>(crc << 1U);
        }
    }
    return crc;
}

int16_t temperature_tenths(uint16_t raw)
{
    const uint32_t scaled = (static_cast<uint32_t>(raw) * 1'750U + 32'768U) / 65'536U;
    return static_cast<int16_t>(static_cast<int32_t>(scaled) - 450);
}

uint16_t humidity_tenths(uint16_t raw)
{
    return static_cast<uint16_t>(
        (static_cast<uint32_t>(raw) * 1'000U + 32'768U) / 65'536U
    );
}

deck_shtc3_result_t measure_once(deck_i2c_device_t device, deck_shtc3_sample_t *sample)
{
    if (!device.transmit(device.context, kWakeCommand, sizeof(kWakeCommand))) {
        (void)device.transmit(device.context, kSleepCommand, sizeof(kSleepCommand));
        return DECK_SHTC3_IO_ERROR;
    }
    device.delay_us(device.context, kWakeDelayUs);

    deck_shtc3_result_t result = DECK_SHTC3_OK;
    uint8_t response[6] = {};
    if (!device.transmit(
            device.context,
            kMeasureTemperatureFirstCommand,
            sizeof(kMeasureTemperatureFirstCommand)
        )) {
        result = DECK_SHTC3_IO_ERROR;
    } else {
        device.delay_us(device.context, kMeasurementDelayUs);
        if (!device.receive(device.context, response, sizeof(response))) {
            result = DECK_SHTC3_IO_ERROR;
        } else if (crc8(response, 2) != response[2] ||
                   crc8(response + 3, 2) != response[5]) {
            result = DECK_SHTC3_CRC_ERROR;
        }
    }

    if (!device.transmit(device.context, kSleepCommand, sizeof(kSleepCommand))) {
        return result == DECK_SHTC3_OK ? DECK_SHTC3_SLEEP_ERROR : result;
    }
    if (result != DECK_SHTC3_OK) {
        return result;
    }

    const uint16_t raw_temperature =
        static_cast<uint16_t>(static_cast<uint16_t>(response[0]) << 8U) | response[1];
    const uint16_t raw_humidity =
        static_cast<uint16_t>(static_cast<uint16_t>(response[3]) << 8U) | response[4];
    sample->raw_temperature_tenths_c = temperature_tenths(raw_temperature);
    sample->humidity_tenths_percent = humidity_tenths(raw_humidity);
    return DECK_SHTC3_OK;
}

}  // namespace

deck_shtc3_result_t deck_shtc3_measure(
    deck_i2c_device_t device,
    uint8_t maximum_attempts,
    deck_shtc3_sample_t *sample
)
{
    if (sample == nullptr || maximum_attempts == 0 || device.transmit == nullptr ||
        device.receive == nullptr || device.delay_us == nullptr) {
        return DECK_SHTC3_INVALID_ARGUMENT;
    }
    *sample = {};
    deck_shtc3_result_t result = DECK_SHTC3_IO_ERROR;
    for (uint8_t attempt = 0; attempt < maximum_attempts; ++attempt) {
        deck_shtc3_sample_t candidate{};
        result = measure_once(device, &candidate);
        if (result == DECK_SHTC3_OK) {
            *sample = candidate;
            return DECK_SHTC3_OK;
        }
    }
    return result;
}
