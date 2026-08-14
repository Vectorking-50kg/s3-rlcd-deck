#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "deck_ai_snapshot.h"

#ifdef __cplusplus
extern "C" {
#endif

#define DECK_AI_PAGE_MAX_LINES 13U
#define DECK_AI_PAGE_FONT_LINE_HEIGHT 19U
#define DECK_AI_PAGE_LINE_SPACING 1U
#define DECK_AI_PAGE_TOP_OFFSET 6U
#define DECK_AI_PAGE_MAX_SESSION_NAME_CELLS 27U

typedef enum {
    DECK_AI_PAGE_WIFI_UNAVAILABLE = 0,
    DECK_AI_PAGE_WIFI_DISCONNECTED,
    DECK_AI_PAGE_WIFI_CONNECTED,
} deck_ai_page_wifi_state_t;

typedef enum {
    DECK_AI_PAGE_COMPANION_UNPAIRED = 0,
    DECK_AI_PAGE_COMPANION_OFFLINE,
    DECK_AI_PAGE_COMPANION_CONNECTING,
    DECK_AI_PAGE_COMPANION_ONLINE,
} deck_ai_page_companion_state_t;

typedef enum {
    DECK_AI_PAGE_SNAPSHOT_EMPTY = 0,
    DECK_AI_PAGE_SNAPSHOT_FRESH,
    DECK_AI_PAGE_SNAPSHOT_STALE,
    DECK_AI_PAGE_SNAPSHOT_UNAVAILABLE,
} deck_ai_page_snapshot_state_t;

typedef struct {
    bool active;
    bool rtc_available;
    uint8_t rtc_hour;
    uint8_t rtc_minute;
    bool temperature_available;
    int16_t calibrated_temperature_tenths_c;
    deck_ai_page_wifi_state_t wifi_state;
    uint8_t wifi_signal_bars;
    deck_ai_page_companion_state_t companion_state;
    deck_ai_page_snapshot_state_t snapshot_state;
    uint64_t trusted_utc_ms;
    deck_ai_snapshot_codex_projection_t codex;
    deck_ai_snapshot_pages_projection_t pages;
    uint8_t selected_provider;
    bool configuration_hint;
} deck_ai_page_view_model_t;

bool deck_ai_page_view_model_equal(
    const deck_ai_page_view_model_t *left,
    const deck_ai_page_view_model_t *right
);

/* Reconciles a dynamic Provider order while preserving the selected ID. */
bool deck_ai_page_view_model_apply_pages(
    deck_ai_page_view_model_t *model,
    const deck_ai_snapshot_pages_projection_t *pages
);

/* Advances Codex -> configured Providers, or Codex <-> configuration hint. */
void deck_ai_page_view_model_next(deck_ai_page_view_model_t *model);

uint8_t deck_ai_page_wifi_signal_bars(int8_t rssi);

/* Converts trusted UTC with the bounded firmware timezone rules. */
bool deck_ai_page_local_time_from_utc(
    uint64_t utc_ms,
    const char *timezone,
    uint8_t *hour,
    uint8_t *minute
);

/* Formats a fixed 400x300 monochrome page with at most DECK_AI_PAGE_MAX_LINES. */
bool deck_ai_page_view_model_format(
    const deck_ai_page_view_model_t *model,
    char *buffer,
    size_t buffer_size
);

#ifdef __cplusplus
}
#endif
