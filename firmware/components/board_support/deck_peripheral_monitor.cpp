#include "deck_peripheral_monitor.h"

#include <limits>
#include <new>

#include "deck_pcf85063.h"
#include "deck_shtc3.h"

namespace {

constexpr uint32_t kButtonDebounceMs = 20;
constexpr uint32_t kKeyLongPressMs = 1'500;
constexpr uint32_t kBootLongPressMs = 3'000;
constexpr uint8_t kSensorMaximumAttempts = 2;

int16_t calibrated_temperature(int16_t raw, int16_t offset)
{
    const int32_t calibrated = static_cast<int32_t>(raw) + static_cast<int32_t>(offset);
    if (calibrated > std::numeric_limits<int16_t>::max()) {
        return std::numeric_limits<int16_t>::max();
    }
    if (calibrated < std::numeric_limits<int16_t>::min()) {
        return std::numeric_limits<int16_t>::min();
    }
    return static_cast<int16_t>(calibrated);
}

}  // namespace

struct deck_peripheral_monitor {
    deck_peripheral_monitor_config_t config;
    deck_button_input_t *key;
    deck_button_input_t *boot;
    deck_peripheral_snapshot_t snapshot;
    uint64_t next_peripheral_poll_ms;
    bool peripherals_polled;
};

deck_peripheral_monitor_t *deck_peripheral_monitor_create(const deck_peripheral_monitor_config_t *config)
{
    if (config == nullptr || config->peripheral_poll_interval_ms == 0) {
        return nullptr;
    }
    auto *monitor = new (std::nothrow) deck_peripheral_monitor_t{};
    if (monitor == nullptr) {
        return nullptr;
    }
    monitor->config = *config;
    monitor->key = deck_button_input_create(kButtonDebounceMs, kKeyLongPressMs);
    monitor->boot = deck_button_input_create(kButtonDebounceMs, kBootLongPressMs);
    if (monitor->key == nullptr || monitor->boot == nullptr) {
        deck_button_input_destroy(monitor->key);
        deck_button_input_destroy(monitor->boot);
        delete monitor;
        return nullptr;
    }
    monitor->snapshot.buttons_available = config->buttons_available;
    return monitor;
}

void deck_peripheral_monitor_destroy(deck_peripheral_monitor_t *monitor)
{
    if (monitor == nullptr) {
        return;
    }
    deck_button_input_destroy(monitor->key);
    deck_button_input_destroy(monitor->boot);
    delete monitor;
}

bool deck_peripheral_monitor_sample_inputs(
    deck_peripheral_monitor_t *monitor,
    bool key_level_high,
    bool boot_level_high,
    uint64_t now_ms
)
{
    if (monitor == nullptr) {
        return false;
    }
    bool changed = false;
    const deck_button_input_event_t key_event =
        deck_button_input_sample(monitor->key, key_level_high, now_ms);
    if (key_event != DECK_BUTTON_INPUT_NONE) {
        monitor->snapshot.key_event = key_event;
        ++monitor->snapshot.key_event_count;
        changed = true;
    }
    const deck_button_input_event_t boot_event =
        deck_button_input_sample(monitor->boot, boot_level_high, now_ms);
    if (boot_event != DECK_BUTTON_INPUT_NONE) {
        monitor->snapshot.boot_event = boot_event;
        ++monitor->snapshot.boot_event_count;
        changed = true;
    }

    return changed;
}

bool deck_peripheral_monitor_poll_due(deck_peripheral_monitor_t *monitor, uint64_t now_ms)
{
    if (monitor == nullptr ||
        (monitor->peripherals_polled && now_ms < monitor->next_peripheral_poll_ms)) {
        return false;
    }
    monitor->peripherals_polled = true;
    monitor->next_peripheral_poll_ms =
        now_ms + monitor->config.peripheral_poll_interval_ms;
    return true;
}

bool deck_peripheral_monitor_measure(
    const deck_peripheral_monitor_t *monitor,
    deck_peripheral_measurement_t *measurement
)
{
    if (monitor == nullptr || measurement == nullptr) {
        return false;
    }
    *measurement = {};
    measurement->rtc_result =
        deck_pcf85063_read(monitor->config.rtc, &measurement->rtc);
    measurement->sensor_result = deck_shtc3_measure(
        monitor->config.shtc3,
        kSensorMaximumAttempts,
        &measurement->sensor
    );
    return true;
}

bool deck_peripheral_monitor_apply(
    deck_peripheral_monitor_t *monitor,
    const deck_peripheral_measurement_t *measurement
)
{
    if (monitor == nullptr || measurement == nullptr) {
        return false;
    }

    monitor->snapshot.rtc_available = measurement->rtc_result == DECK_RTC_OK;
    if (measurement->rtc_result == DECK_RTC_OK) {
        monitor->snapshot.rtc_hour = measurement->rtc.hour;
        monitor->snapshot.rtc_minute = measurement->rtc.minute;
        monitor->snapshot.rtc_second = measurement->rtc.second;
    } else if (measurement->rtc_result == DECK_RTC_IO_ERROR ||
               measurement->rtc_result == DECK_RTC_DATA_INVALID) {
        ++monitor->snapshot.rtc_error_count;
    }

    monitor->snapshot.sensor_available = measurement->sensor_result == DECK_SHTC3_OK;
    if (measurement->sensor_result == DECK_SHTC3_OK) {
        monitor->snapshot.raw_temperature_tenths_c =
            measurement->sensor.raw_temperature_tenths_c;
        monitor->snapshot.calibrated_temperature_tenths_c = calibrated_temperature(
            measurement->sensor.raw_temperature_tenths_c,
            monitor->config.temperature_offset_tenths_c
        );
        monitor->snapshot.humidity_tenths_percent =
            measurement->sensor.humidity_tenths_percent;
    } else {
        ++monitor->snapshot.sensor_error_count;
    }
    return true;
}

bool deck_peripheral_monitor_set_temperature_offset(
    deck_peripheral_monitor_t *monitor,
    int16_t temperature_offset_tenths_c
)
{
    if (monitor == nullptr) {
        return false;
    }
    monitor->config.temperature_offset_tenths_c = temperature_offset_tenths_c;
    if (monitor->snapshot.sensor_available) {
        monitor->snapshot.calibrated_temperature_tenths_c = calibrated_temperature(
            monitor->snapshot.raw_temperature_tenths_c,
            temperature_offset_tenths_c
        );
    }
    return true;
}

bool deck_peripheral_monitor_sample(
    deck_peripheral_monitor_t *monitor,
    bool key_level_high,
    bool boot_level_high,
    uint64_t now_ms
)
{
    const bool inputs_changed = deck_peripheral_monitor_sample_inputs(
        monitor,
        key_level_high,
        boot_level_high,
        now_ms
    );
    if (!deck_peripheral_monitor_poll_due(monitor, now_ms)) {
        return inputs_changed;
    }
    deck_peripheral_measurement_t measurement{};
    return deck_peripheral_monitor_measure(monitor, &measurement) &&
           deck_peripheral_monitor_apply(monitor, &measurement);
}

bool deck_peripheral_monitor_snapshot(
    const deck_peripheral_monitor_t *monitor,
    deck_peripheral_snapshot_t *snapshot
)
{
    if (monitor == nullptr || snapshot == nullptr) {
        return false;
    }
    *snapshot = monitor->snapshot;
    return true;
}
