#pragma once

#include "deck_setup_mode.h"
#include "deck_setup_confirmation.h"
#include "deck_device_settings.h"
#include "deck_companion_profiles.h"
#include "deck_wifi_config.h"

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define DECK_SETUP_SCAN_SSID_CAPACITY 33
#define DECK_SETUP_PAIR_ACK_SIZE 16

typedef enum {
    DECK_SETUP_HTTP_NOT_FOUND = 0,
    DECK_SETUP_HTTP_METHOD_NOT_ALLOWED,
    DECK_SETUP_HTTP_PAGE,
    DECK_SETUP_HTTP_LEGACY_PAIRING_PAGE,
    DECK_SETUP_HTTP_STATUS,
    DECK_SETUP_HTTP_SCAN,
    DECK_SETUP_HTTP_WIFI,
    DECK_SETUP_HTTP_TEMPERATURE,
    DECK_SETUP_HTTP_WIFI_CLEAR_REQUEST,
    DECK_SETUP_HTTP_WIFI_CLEAR_CONFIRM,
    DECK_SETUP_HTTP_COMPANION_PAIR,
    DECK_SETUP_HTTP_COMPANION_PAIR_ACK,
    DECK_SETUP_HTTP_COMPANION_SELECT,
    DECK_SETUP_HTTP_COMPANION_PRIORITY,
    DECK_SETUP_HTTP_COMPANION_REVOKE,
} deck_setup_http_route_t;

typedef enum {
    DECK_SETUP_HTTP_GET = 0,
    DECK_SETUP_HTTP_POST,
} deck_setup_http_method_t;

typedef struct {
    deck_setup_http_route_t route;
    deck_setup_http_method_t method;
    const char *path;
} deck_setup_http_route_spec_t;

typedef struct {
    char ssid[DECK_SETUP_SCAN_SSID_CAPACITY];
    int8_t rssi;
    bool secure;
} deck_setup_scan_result_t;

typedef struct {
    const uint8_t *ssid;
    size_t ssid_size;
    int8_t rssi;
    bool secure;
} deck_setup_scan_observation_t;

typedef enum {
    DECK_SETUP_WIFI_REQUEST_OK = 0,
    DECK_SETUP_WIFI_REQUEST_MALFORMED,
    DECK_SETUP_WIFI_REQUEST_INVALID_SSID,
    DECK_SETUP_WIFI_REQUEST_INVALID_PASSWORD,
} deck_setup_wifi_request_result_t;

typedef enum {
    DECK_SETUP_TEMPERATURE_REQUEST_OK = 0,
    DECK_SETUP_TEMPERATURE_REQUEST_MALFORMED,
    DECK_SETUP_TEMPERATURE_REQUEST_NOT_NUMERIC,
    DECK_SETUP_TEMPERATURE_REQUEST_OUT_OF_RANGE,
    DECK_SETUP_TEMPERATURE_REQUEST_NOT_EXACT_TENTH,
} deck_setup_temperature_request_result_t;

typedef enum {
    DECK_SETUP_COMPANION_REQUEST_OK = 0,
    DECK_SETUP_COMPANION_REQUEST_MALFORMED,
    DECK_SETUP_COMPANION_REQUEST_INVALID_ADDRESS,
    DECK_SETUP_COMPANION_REQUEST_INVALID_CODE,
} deck_setup_companion_request_result_t;

const deck_setup_http_route_spec_t *deck_setup_http_routes(size_t *route_count);
deck_setup_http_route_t deck_setup_http_route(const char *method, const char *path);
bool deck_setup_http_address_is_setup_gateway(
    const uint8_t *local_address,
    size_t local_address_size
);
bool deck_setup_http_extract_ipv4(
    const uint8_t *address,
    size_t address_size,
    uint8_t ipv4[4]
);
bool deck_setup_http_convert_scan_results(
    const deck_setup_scan_observation_t *observations,
    size_t observation_count,
    deck_setup_scan_result_t *results,
    size_t result_capacity,
    size_t *result_count
);
// Returns the immutable, NUL-terminated Setup page owned by this component.
const char *deck_setup_http_page(void);
// Returns the temporary, explicitly labelled Pairing v1 compatibility page.
const char *deck_setup_http_legacy_pairing_page(void);
deck_setup_wifi_request_result_t deck_setup_http_parse_wifi_request(
    const char *body,
    size_t body_size,
    deck_wifi_credentials_t *credentials
);
deck_setup_temperature_request_result_t deck_setup_http_parse_temperature_request(
    const char *body,
    size_t body_size,
    int16_t *temperature_offset_tenths_c
);
bool deck_setup_http_parse_confirmation_request(
    const char *body,
    size_t body_size,
    char *token,
    size_t token_capacity
);
deck_setup_companion_request_result_t deck_setup_http_parse_companion_pair_request(
    const char *body,
    size_t body_size,
    deck_companion_pair_request_t *request
);
bool deck_setup_http_parse_pair_ack_request(
    const char *body,
    size_t body_size,
    uint8_t response_ack[DECK_SETUP_PAIR_ACK_SIZE]
);
bool deck_setup_http_parse_companion_profile_request(
    const char *body,
    size_t body_size,
    char *profile_id,
    size_t profile_id_capacity
);
bool deck_setup_http_parse_companion_priority_request(
    const char *body,
    size_t body_size,
    char *profile_id,
    size_t profile_id_capacity,
    int32_t *priority
);
bool deck_setup_http_render_status(
    const deck_setup_snapshot_t *snapshot,
    const deck_wifi_config_snapshot_t *wifi,
    const deck_device_settings_snapshot_t *settings,
    const deck_companion_profiles_snapshot_t *companions,
    const deck_setup_scan_result_t *networks,
    size_t network_count,
    char *buffer,
    size_t buffer_size
);

#ifdef __cplusplus
}
#endif
