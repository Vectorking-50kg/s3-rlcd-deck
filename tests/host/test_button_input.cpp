#include "deck_button_input.h"

#include <cassert>
#include <cstdint>

namespace {

void key_short_press_is_emitted_after_debounced_release()
{
    deck_button_input_t *key = deck_button_input_create(20, 1'500);
    assert(key != nullptr);

    assert(deck_button_input_sample(key, true, 0) == DECK_BUTTON_INPUT_NONE);
    assert(deck_button_input_sample(key, false, 100) == DECK_BUTTON_INPUT_NONE);
    assert(deck_button_input_sample(key, false, 120) == DECK_BUTTON_INPUT_NONE);
    assert(deck_button_input_sample(key, true, 500) == DECK_BUTTON_INPUT_NONE);
    assert(deck_button_input_sample(key, true, 519) == DECK_BUTTON_INPUT_NONE);
    assert(deck_button_input_sample(key, true, 520) == DECK_BUTTON_INPUT_SHORT_PRESS);

    deck_button_input_destroy(key);
}

void key_long_press_is_emitted_once_at_1500_ms()
{
    deck_button_input_t *key = deck_button_input_create(20, 1'500);
    assert(key != nullptr);

    assert(deck_button_input_sample(key, false, 100) == DECK_BUTTON_INPUT_NONE);
    assert(deck_button_input_sample(key, false, 120) == DECK_BUTTON_INPUT_NONE);
    assert(deck_button_input_sample(key, false, 1'599) == DECK_BUTTON_INPUT_NONE);
    assert(deck_button_input_sample(key, false, 1'600) == DECK_BUTTON_INPUT_LONG_PRESS);
    assert(deck_button_input_sample(key, false, 1'700) == DECK_BUTTON_INPUT_NONE);
    assert(deck_button_input_sample(key, true, 1'800) == DECK_BUTTON_INPUT_NONE);
    assert(deck_button_input_sample(key, true, 1'820) == DECK_BUTTON_INPUT_NONE);

    deck_button_input_destroy(key);
}

deck_button_input_event_t release_after(
    uint32_t long_press_ms,
    uint64_t release_ms
)
{
    deck_button_input_t *button = deck_button_input_create(20, long_press_ms);
    assert(button != nullptr);
    assert(deck_button_input_sample(button, false, 0) == DECK_BUTTON_INPUT_NONE);
    assert(deck_button_input_sample(button, false, 20) == DECK_BUTTON_INPUT_NONE);
    assert(deck_button_input_sample(button, true, release_ms) == DECK_BUTTON_INPUT_NONE);
    const deck_button_input_event_t event =
        deck_button_input_sample(button, true, release_ms + 20);
    deck_button_input_destroy(button);
    return event;
}

void release_boundary_and_boot_threshold_are_exact()
{
    assert(release_after(1'500, 1'499) == DECK_BUTTON_INPUT_SHORT_PRESS);
    assert(release_after(1'500, 1'500) == DECK_BUTTON_INPUT_LONG_PRESS);
    assert(release_after(3'000, 2'999) == DECK_BUTTON_INPUT_SHORT_PRESS);
    assert(release_after(3'000, 3'000) == DECK_BUTTON_INPUT_LONG_PRESS);
}

}  // namespace

int main()
{
    key_short_press_is_emitted_after_debounced_release();
    key_long_press_is_emitted_once_at_1500_ms();
    release_boundary_and_boot_threshold_are_exact();
    return 0;
}
