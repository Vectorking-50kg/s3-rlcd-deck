#pragma once

#include "deck_peripheral_monitor.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef struct deck_peripherals deck_peripherals_t;
typedef void (*deck_peripheral_snapshot_fn)(void *context, const deck_peripheral_snapshot_t *snapshot);

/* Starts a lifetime Deck peripheral service. The callback context must remain valid. */
deck_peripherals_t *deck_peripherals_start(
    int16_t temperature_offset_tenths_c,
    deck_peripheral_snapshot_fn callback,
    void *callback_context
);
bool deck_peripherals_set_temperature_offset(
    deck_peripherals_t *peripherals,
    int16_t temperature_offset_tenths_c
);

#ifdef __cplusplus
}
#endif
