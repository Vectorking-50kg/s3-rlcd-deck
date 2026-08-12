#pragma once

#include <stdbool.h>
#include <stdint.h>

#include "deck_i2c_device.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef enum {
    DECK_RTC_OK = 0,
    DECK_RTC_CLOCK_INVALID,
    DECK_RTC_DATA_INVALID,
    DECK_RTC_IO_ERROR,
    DECK_RTC_INVALID_ARGUMENT,
} deck_rtc_result_t;

typedef struct {
    bool clock_integrity_guaranteed;
    uint8_t hour;
    uint8_t minute;
    uint8_t second;
} deck_rtc_time_t;

deck_rtc_result_t deck_pcf85063_read(deck_i2c_device_t device, deck_rtc_time_t *time);

#ifdef __cplusplus
}
#endif
