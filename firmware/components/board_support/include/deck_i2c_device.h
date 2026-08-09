#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef bool (*deck_i2c_transmit_fn)(void *context, const uint8_t *data, size_t size);
typedef bool (*deck_i2c_receive_fn)(void *context, uint8_t *data, size_t size);
typedef bool (*deck_i2c_transmit_receive_fn)(
    void *context,
    const uint8_t *write_data,
    size_t write_size,
    uint8_t *read_data,
    size_t read_size
);
typedef void (*deck_delay_us_fn)(void *context, uint32_t delay_us);

typedef struct {
    deck_i2c_transmit_fn transmit;
    deck_i2c_receive_fn receive;
    deck_i2c_transmit_receive_fn transmit_receive;
    deck_delay_us_fn delay_us;
    void *context;
} deck_i2c_device_t;

#ifdef __cplusplus
}
#endif
