#pragma once

#include <stdbool.h>
#include <stdint.h>

#include "deck_ui_scene.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef struct {
    int16_t x;
    int16_t y;
    int16_t width;
    int16_t height;
} deck_ui_rect_t;

typedef struct {
    deck_ui_rect_t hero;
    deck_ui_rect_t message;
    deck_ui_rect_t detail;
    deck_ui_rect_t metric_rows[DECK_UI_SCENE_MAX_METRICS];
    deck_ui_rect_t summary;
    bool metric_visible[DECK_UI_SCENE_MAX_METRICS];
    bool summary_visible;
    bool summary_detail_visible;
} deck_ui_layout_t;

/* Computes every state-dependent rectangle in the fixed 400x300 content grid. */
bool deck_ui_layout_plan(const deck_ui_scene_t *scene, deck_ui_layout_t *layout);

/* Used by Host contracts to prove that a visible object stays on the physical panel. */
bool deck_ui_rect_within_display(deck_ui_rect_t rectangle);

#ifdef __cplusplus
}
#endif
