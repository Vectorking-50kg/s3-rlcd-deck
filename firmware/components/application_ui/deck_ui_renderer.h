#pragma once

#include "deck_ui_scene.h"

#pragma GCC diagnostic push
#pragma GCC diagnostic ignored "-Wsign-conversion"
#include "lvgl.h"
#pragma GCC diagnostic pop

struct deck_ui_renderer;
typedef struct deck_ui_renderer deck_ui_renderer_t;

deck_ui_renderer_t *deck_ui_renderer_create(lv_obj_t *screen);
void deck_ui_renderer_destroy(deck_ui_renderer_t *renderer);
bool deck_ui_renderer_present(deck_ui_renderer_t *renderer, const deck_ui_scene_t *scene);
