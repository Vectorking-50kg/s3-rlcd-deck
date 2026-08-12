#pragma once

#include <stdbool.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct deck_button_input deck_button_input_t;

typedef enum {
    DECK_BUTTON_INPUT_NONE = 0,
    DECK_BUTTON_INPUT_SHORT_PRESS,
    DECK_BUTTON_INPUT_LONG_PRESS,
} deck_button_input_event_t;

deck_button_input_t *deck_button_input_create(uint32_t debounce_ms, uint32_t long_press_ms);
void deck_button_input_destroy(deck_button_input_t *button);

/* Samples an electrical GPIO level. The Deck buttons are active-low. */
deck_button_input_event_t deck_button_input_sample(
    deck_button_input_t *button,
    bool level_high,
    uint64_t now_ms
);

#ifdef __cplusplus
}
#endif
