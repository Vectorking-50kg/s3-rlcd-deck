#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef enum {
    DECK_BUTTON_NONE = 0,
    DECK_BUTTON_SHORT_PRESS,
    DECK_BUTTON_LONG_PRESS,
} deck_button_event_t;

typedef enum {
    DECK_WIFI_UNAVAILABLE = 0,
    DECK_WIFI_DISCONNECTED,
    DECK_WIFI_CONNECTED,
} deck_wifi_state_t;

typedef enum {
    DECK_SETUP_IDLE = 0,
    DECK_SETUP_ACTIVE,
} deck_setup_state_t;

typedef enum {
    DECK_DATA_UNAVAILABLE = 0,
    DECK_DATA_SIMULATED,
    DECK_DATA_VERIFIED,
} deck_data_source_t;

typedef struct {
    const char *firmware_version;
    deck_data_source_t data_source;
    bool rtc_available;
    uint8_t rtc_hour;
    uint8_t rtc_minute;
    uint32_t rtc_error_count;
    bool sensor_available;
    int16_t raw_temperature_tenths_c;
    int16_t calibrated_temperature_tenths_c;
    uint16_t humidity_tenths_percent;
    uint32_t sensor_error_count;
    bool buttons_available;
    deck_button_event_t key_event;
    uint32_t key_event_count;
    deck_button_event_t boot_event;
    uint32_t boot_event_count;
    deck_wifi_state_t wifi_state;
    deck_setup_state_t setup_state;
    uint32_t refresh_count;
    uint64_t uptime_seconds;
    uint32_t minimum_free_heap_bytes;
} deck_m0_view_model_t;

bool deck_m0_view_model_equal(const deck_m0_view_model_t *left, const deck_m0_view_model_t *right);
bool deck_m0_view_model_format(const deck_m0_view_model_t *model, char *buffer, size_t buffer_size);
const char *deck_m0_required_glyphs(void);

#ifdef __cplusplus
}
#endif
