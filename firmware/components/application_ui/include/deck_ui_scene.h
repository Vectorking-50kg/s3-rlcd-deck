#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "deck_m0_view_model.h"

#ifdef __cplusplus
extern "C" {
#endif

#define DECK_UI_SCENE_MAX_METRICS 4U
#define DECK_UI_SCENE_TEXT_CAPACITY 96U
#define DECK_UI_SCENE_SHORT_TEXT_CAPACITY 48U

typedef enum {
    DECK_UI_SCENE_BOARD = 0,
    DECK_UI_SCENE_SETUP,
    DECK_UI_SCENE_PAIRING,
    DECK_UI_SCENE_AI,
    DECK_UI_SCENE_PROVIDER,
    DECK_UI_SCENE_CONFIGURATION_HINT,
    DECK_UI_SCENE_SERIAL,
} deck_ui_scene_kind_t;

typedef enum {
    DECK_UI_BADGE_OUTLINE = 0,
    DECK_UI_BADGE_SOLID,
    DECK_UI_BADGE_ALERT,
} deck_ui_badge_style_t;

typedef struct {
    char label[DECK_UI_SCENE_SHORT_TEXT_CAPACITY];
    char value[DECK_UI_SCENE_SHORT_TEXT_CAPACITY];
    char detail[DECK_UI_SCENE_SHORT_TEXT_CAPACITY];
    uint16_t basis_points;
    bool has_progress;
} deck_ui_scene_metric_t;

typedef struct {
    deck_ui_scene_kind_t kind;
    char status_time[16];
    char status_temperature[24];
    char status_wifi[24];
    char status_companion[32];
    char title[DECK_UI_SCENE_TEXT_CAPACITY];
    char badge[DECK_UI_SCENE_SHORT_TEXT_CAPACITY];
    deck_ui_badge_style_t badge_style;
    char hero[DECK_UI_SCENE_TEXT_CAPACITY];
    char message[DECK_UI_SCENE_TEXT_CAPACITY];
    char detail[DECK_UI_SCENE_TEXT_CAPACITY];
    deck_ui_scene_metric_t metrics[DECK_UI_SCENE_MAX_METRICS];
    uint8_t metric_count;
    char summary_title[DECK_UI_SCENE_TEXT_CAPACITY];
    char summary_value[DECK_UI_SCENE_TEXT_CAPACITY];
    char summary_detail[DECK_UI_SCENE_TEXT_CAPACITY];
    char footer_left[DECK_UI_SCENE_SHORT_TEXT_CAPACITY];
    char footer_center[DECK_UI_SCENE_SHORT_TEXT_CAPACITY];
    char footer_right[DECK_UI_SCENE_SHORT_TEXT_CAPACITY];
    bool centered;
    bool hero_is_code;
} deck_ui_scene_t;

/* Projects domain facts into one bounded, Chinese-first visual scene. */
bool deck_ui_scene_project(const deck_m0_view_model_t *model, deck_ui_scene_t *scene);

/* Compares only semantic fields; struct padding never controls repaint coalescing. */
bool deck_ui_scene_equal(const deck_ui_scene_t *left, const deck_ui_scene_t *right);

#ifdef __cplusplus
}
#endif
