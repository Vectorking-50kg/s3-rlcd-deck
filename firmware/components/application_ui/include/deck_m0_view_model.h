#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "deck_ai_page_view_model.h"

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
    DECK_WIFI_CONFIG_VIEW_NO_ACTIVE = 0,
    DECK_WIFI_CONFIG_VIEW_ACTIVE,
    DECK_WIFI_CONFIG_VIEW_VALIDATING,
    DECK_WIFI_CONFIG_VIEW_AUTH_FAILED,
    DECK_WIFI_CONFIG_VIEW_TIMED_OUT,
    DECK_WIFI_CONFIG_VIEW_CONNECTION_FAILED,
    DECK_WIFI_CONFIG_VIEW_STORAGE_ERROR,
} deck_wifi_config_view_state_t;

typedef enum {
    DECK_WIFI_RECORD_VIEW_EMPTY = 0,
    DECK_WIFI_RECORD_VIEW_VALID,
    DECK_WIFI_RECORD_VIEW_RECOVERED_PREVIOUS,
    DECK_WIFI_RECORD_VIEW_CORRUPT,
    DECK_WIFI_RECORD_VIEW_UNSUPPORTED_SCHEMA,
    DECK_WIFI_RECORD_VIEW_MIGRATION_FAILED,
    DECK_WIFI_RECORD_VIEW_IO_ERROR,
} deck_wifi_record_view_status_t;

typedef enum {
    DECK_SETUP_UNAVAILABLE = 0,
    DECK_SETUP_IDLE,
    DECK_SETUP_ACTIVE,
} deck_setup_state_t;

typedef enum {
    DECK_SERIAL_VIEW_UNAVAILABLE = 0,
    DECK_SERIAL_VIEW_DISARMED,
    DECK_SERIAL_VIEW_USB_TX,
    DECK_SERIAL_VIEW_WEB_TX,
} deck_serial_view_state_t;

typedef struct {
    deck_serial_view_state_t state;
    uint64_t session_id;
    uint64_t owner_generation;
    uint64_t usb_tx_rejected;
    uint32_t uart_install_failures;
    bool uart_install_failed;
    bool uart_installed;
} deck_serial_view_model_t;

#define DECK_M0_SETUP_SSID_CAPACITY 13
#define DECK_M0_SETUP_PASSWORD_CAPACITY 15
#define DECK_M0_SETUP_ADDRESS_CAPACITY 16

typedef enum {
    DECK_DATA_UNAVAILABLE = 0,
    DECK_DATA_SIMULATED,
    DECK_DATA_VERIFIED,
} deck_data_source_t;

typedef struct {
    const char *firmware_version;
    uint8_t data_source;
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
    deck_wifi_config_view_state_t wifi_config_state;
    deck_wifi_record_view_status_t wifi_record_status;
    deck_wifi_record_view_status_t wifi_candidate_record_status;
    uint32_t wifi_config_generation;
    deck_setup_state_t setup_state;
    char setup_ssid[DECK_M0_SETUP_SSID_CAPACITY];
    char setup_password[DECK_M0_SETUP_PASSWORD_CAPACITY];
    char setup_address[DECK_M0_SETUP_ADDRESS_CAPACITY];
    uint32_t refresh_count;
    uint64_t uptime_seconds;
    uint32_t minimum_free_heap_bytes;
    deck_ai_page_view_model_t ai_page;
    deck_serial_view_model_t serial;
} deck_m0_view_model_t;

bool deck_m0_view_model_equal(const deck_m0_view_model_t *left, const deck_m0_view_model_t *right);
bool deck_m0_view_model_format(const deck_m0_view_model_t *model, char *buffer, size_t buffer_size);
bool deck_m0_view_model_format_active_page(
    const deck_m0_view_model_t *model,
    char *buffer,
    size_t buffer_size,
    bool *ai_page_visible
);
const char *deck_m0_required_glyphs(void);

#ifdef __cplusplus
}
#endif
