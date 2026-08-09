#pragma once

#include <stdbool.h>

#include "deck_display.h"
#include "deck_m0_view_model.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef enum {
    DECK_APPLICATION_UI_READY = 0,
    DECK_APPLICATION_UI_FRAME_COMPLETED,
    DECK_APPLICATION_UI_FAILED,
} deck_application_ui_state_t;

typedef struct {
    deck_application_ui_state_t state;
    deck_display_metrics_t display;
} deck_application_ui_event_t;

typedef void (*deck_application_ui_event_fn)(void *context, const deck_application_ui_event_t *event);

/* Starts the sole LVGL owner task. The display and callback context must outlive it. */
bool deck_application_ui_start(
    deck_display_service_t *display,
    const deck_m0_view_model_t *initial_model,
    deck_application_ui_event_fn event_callback,
    void *event_context
);

/* Coalesces a cross-task model update for the sole LVGL owner. */
bool deck_application_ui_update(const deck_m0_view_model_t *model);

#ifdef __cplusplus
}
#endif
