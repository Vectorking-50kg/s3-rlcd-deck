#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct {
    const char *firmware_version;
    const char *reset_reason;
    uint64_t uptime_ms;
    uint32_t minimum_free_heap_bytes;
} deck_boot_info_t;

typedef struct {
    uint16_t width;
    uint16_t height;
    uint32_t frame_bytes;
    uint32_t submitted_frames;
    uint32_t completed_frames;
    uint32_t transfer_timeouts;
    uint32_t start_failures;
    uint32_t rejected_updates;
} deck_display_ready_info_t;

typedef enum {
    DECK_DIAGNOSTIC_BUTTON_NONE = 0,
    DECK_DIAGNOSTIC_BUTTON_SHORT_PRESS,
    DECK_DIAGNOSTIC_BUTTON_LONG_PRESS,
} deck_diagnostic_button_event_t;

typedef struct {
    bool rtc_available;
    uint8_t rtc_hour;
    uint8_t rtc_minute;
    bool sensor_available;
    int16_t raw_temperature_tenths_c;
    int16_t calibrated_temperature_tenths_c;
    uint16_t humidity_tenths_percent;
    bool buttons_available;
    deck_diagnostic_button_event_t key_event;
    uint32_t key_event_count;
    deck_diagnostic_button_event_t boot_event;
    uint32_t boot_event_count;
    uint32_t rtc_error_count;
    uint32_t sensor_error_count;
} deck_peripheral_diagnostic_info_t;

typedef struct {
    bool active;
    const char *reason;
    uint32_t session_id;
    const char *ssid;
    const char *address;
    const char *error_stage;
    const char *wifi_config_state;
    const char *wifi_record_status;
    const char *wifi_candidate_record_status;
    bool wifi_has_active;
    bool wifi_has_candidate;
    uint32_t wifi_generation;
} deck_setup_diagnostic_info_t;

typedef void (*deck_diagnostic_write_fn)(void *context, const char *data, size_t size);

typedef struct {
    deck_diagnostic_write_fn write;
    void *context;
} deck_diagnostic_sink_t;

bool deck_boot_diagnostics_emit(const deck_boot_info_t *info, deck_diagnostic_sink_t sink);
bool deck_display_diagnostics_emit(const deck_display_ready_info_t *info, deck_diagnostic_sink_t sink);
bool deck_display_progress_diagnostics_emit(
    const deck_display_ready_info_t *info,
    deck_diagnostic_sink_t sink
);
bool deck_peripheral_diagnostics_emit(
    const deck_peripheral_diagnostic_info_t *info,
    deck_diagnostic_sink_t sink
);
bool deck_setup_diagnostics_emit(
    const deck_setup_diagnostic_info_t *info,
    deck_diagnostic_sink_t sink
);

#ifdef __cplusplus
}
#endif
