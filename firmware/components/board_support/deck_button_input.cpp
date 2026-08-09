#include "deck_button_input.h"

#include <new>

struct deck_button_input {
    uint32_t debounce_ms;
    uint32_t long_press_ms;
    uint64_t raw_changed_ms;
    uint64_t press_started_ms;
    bool raw_level_high;
    bool stable_level_high;
    bool press_active;
    bool long_press_emitted;
};

deck_button_input_t *deck_button_input_create(uint32_t debounce_ms, uint32_t long_press_ms)
{
    if (debounce_ms == 0 || long_press_ms == 0) {
        return nullptr;
    }
    return new (std::nothrow) deck_button_input_t{
        debounce_ms,
        long_press_ms,
        0,
        0,
        true,
        true,
        false,
        false,
    };
}

void deck_button_input_destroy(deck_button_input_t *button)
{
    delete button;
}

deck_button_input_event_t deck_button_input_sample(
    deck_button_input_t *button,
    bool level_high,
    uint64_t now_ms
)
{
    if (button == nullptr) {
        return DECK_BUTTON_INPUT_NONE;
    }
    if (level_high != button->raw_level_high) {
        button->raw_level_high = level_high;
        button->raw_changed_ms = now_ms;
    }
    if (button->press_active && !button->long_press_emitted &&
        !button->raw_level_high &&
        now_ms - button->press_started_ms >= button->long_press_ms) {
        button->long_press_emitted = true;
        return DECK_BUTTON_INPUT_LONG_PRESS;
    }
    if (button->raw_level_high == button->stable_level_high ||
        now_ms - button->raw_changed_ms < button->debounce_ms) {
        return DECK_BUTTON_INPUT_NONE;
    }

    button->stable_level_high = button->raw_level_high;
    if (!button->stable_level_high) {
        button->press_active = true;
        button->long_press_emitted = false;
        button->press_started_ms = button->raw_changed_ms;
        return DECK_BUTTON_INPUT_NONE;
    }

    if (!button->press_active || button->long_press_emitted) {
        return DECK_BUTTON_INPUT_NONE;
    }
    button->press_active = false;
    const uint64_t press_duration_ms = button->raw_changed_ms - button->press_started_ms;
    return press_duration_ms >= button->long_press_ms ? DECK_BUTTON_INPUT_LONG_PRESS
                                                      : DECK_BUTTON_INPUT_SHORT_PRESS;
}
