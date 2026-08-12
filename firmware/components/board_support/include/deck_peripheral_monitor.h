#pragma once

#include <stdbool.h>
#include <stdint.h>

#include "deck_button_input.h"
#include "deck_i2c_device.h"
#include "deck_pcf85063.h"
#include "deck_shtc3.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef struct deck_peripheral_monitor deck_peripheral_monitor_t;

typedef struct {
    deck_i2c_device_t rtc;
    deck_i2c_device_t shtc3;
    int16_t temperature_offset_tenths_c;
    uint32_t peripheral_poll_interval_ms;
    bool buttons_available;
} deck_peripheral_monitor_config_t;

typedef struct {
    bool rtc_available;
    uint8_t rtc_hour;
    uint8_t rtc_minute;
    uint8_t rtc_second;
    uint32_t rtc_error_count;
    bool sensor_available;
    int16_t raw_temperature_tenths_c;
    int16_t calibrated_temperature_tenths_c;
    uint16_t humidity_tenths_percent;
    uint32_t sensor_error_count;
    deck_button_input_event_t key_event;
    uint32_t key_event_count;
    deck_button_input_event_t boot_event;
    uint32_t boot_event_count;
    bool buttons_available;
} deck_peripheral_snapshot_t;

typedef struct {
    deck_rtc_result_t rtc_result;
    deck_rtc_time_t rtc;
    deck_shtc3_result_t sensor_result;
    deck_shtc3_sample_t sensor;
} deck_peripheral_measurement_t;

deck_peripheral_monitor_t *deck_peripheral_monitor_create(const deck_peripheral_monitor_config_t *config);
void deck_peripheral_monitor_destroy(deck_peripheral_monitor_t *monitor);

/* Fast path: samples only active-low KEY/BOOT levels and never performs I2C. */
bool deck_peripheral_monitor_sample_inputs(
    deck_peripheral_monitor_t *monitor,
    bool key_level_high,
    bool boot_level_high,
    uint64_t now_ms
);

/* Split I2C path: reserve a due poll, measure without state mutation, then apply briefly. */
bool deck_peripheral_monitor_poll_due(deck_peripheral_monitor_t *monitor, uint64_t now_ms);
bool deck_peripheral_monitor_measure(
    const deck_peripheral_monitor_t *monitor,
    deck_peripheral_measurement_t *measurement
);
bool deck_peripheral_monitor_apply(
    deck_peripheral_monitor_t *monitor,
    const deck_peripheral_measurement_t *measurement
);

/* Sequential convenience API used by single-threaded clients and Host tests. */
bool deck_peripheral_monitor_sample(
    deck_peripheral_monitor_t *monitor,
    bool key_level_high,
    bool boot_level_high,
    uint64_t now_ms
);
bool deck_peripheral_monitor_snapshot(
    const deck_peripheral_monitor_t *monitor,
    deck_peripheral_snapshot_t *snapshot
);

#ifdef __cplusplus
}
#endif
