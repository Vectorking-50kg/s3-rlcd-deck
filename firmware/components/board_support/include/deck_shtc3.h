#pragma once

#include <stdint.h>

#include "deck_i2c_device.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef enum {
    DECK_SHTC3_OK = 0,
    DECK_SHTC3_CRC_ERROR,
    DECK_SHTC3_IO_ERROR,
    DECK_SHTC3_SLEEP_ERROR,
    DECK_SHTC3_INVALID_ARGUMENT,
} deck_shtc3_result_t;

typedef struct {
    int16_t raw_temperature_tenths_c;
    uint16_t humidity_tenths_percent;
} deck_shtc3_sample_t;

deck_shtc3_result_t deck_shtc3_measure(
    deck_i2c_device_t device,
    uint8_t maximum_attempts,
    deck_shtc3_sample_t *sample
);

#ifdef __cplusplus
}
#endif
