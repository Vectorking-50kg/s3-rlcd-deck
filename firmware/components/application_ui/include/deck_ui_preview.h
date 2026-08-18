#pragma once

#include <stdbool.h>

#include "deck_ui_scene.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef enum {
    DECK_UI_PREVIEW_BOARD = 0,
    DECK_UI_PREVIEW_PAIRING,
    DECK_UI_PREVIEW_PAIRING_AUTHENTICATING,
    DECK_UI_PREVIEW_PAIRING_VERIFIED,
    DECK_UI_PREVIEW_PAIRING_SUCCESS,
    DECK_UI_PREVIEW_PAIRING_EXPIRED,
    DECK_UI_PREVIEW_PAIRING_ERROR,
    DECK_UI_PREVIEW_SETUP,
    DECK_UI_PREVIEW_SETUP_VALIDATING,
    DECK_UI_PREVIEW_SETUP_ERROR,
    DECK_UI_PREVIEW_AI,
    DECK_UI_PREVIEW_AI_STALE,
    DECK_UI_PREVIEW_PROVIDER,
    DECK_UI_PREVIEW_CONFIGURATION,
    DECK_UI_PREVIEW_SERIAL,
    DECK_UI_PREVIEW_OFFLINE,
    DECK_UI_PREVIEW_ERROR,
} deck_ui_preview_page_t;

/* Parses the stable ASCII page names accepted by the dev-only visual harness. */
bool deck_ui_preview_page_parse(const char *name, deck_ui_preview_page_t *page);

/* Produces deterministic, credential-free scenes for real-panel visual review. */
bool deck_ui_preview_scene(deck_ui_preview_page_t page, deck_ui_scene_t *scene);

#ifdef __cplusplus
}
#endif
