#pragma once

#include "deck_setup_mode.h"
#include "deck_wifi_config.h"

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define DECK_SETUP_SCAN_SSID_CAPACITY 33

typedef enum {
    DECK_SETUP_HTTP_NOT_FOUND = 0,
    DECK_SETUP_HTTP_METHOD_NOT_ALLOWED,
    DECK_SETUP_HTTP_PAGE,
    DECK_SETUP_HTTP_STATUS,
    DECK_SETUP_HTTP_SCAN,
    DECK_SETUP_HTTP_WIFI,
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

const deck_setup_http_route_spec_t *deck_setup_http_routes(size_t *route_count);
deck_setup_http_route_t deck_setup_http_route(const char *method, const char *path);
bool deck_setup_http_convert_scan_results(
    const deck_setup_scan_observation_t *observations,
    size_t observation_count,
    deck_setup_scan_result_t *results,
    size_t result_capacity,
    size_t *result_count
);
bool deck_setup_http_render_page(char *buffer, size_t buffer_size);
deck_setup_wifi_request_result_t deck_setup_http_parse_wifi_request(
    const char *body,
    size_t body_size,
    deck_wifi_credentials_t *credentials
);
bool deck_setup_http_render_status(
    const deck_setup_snapshot_t *snapshot,
    const deck_wifi_config_snapshot_t *wifi,
    const deck_setup_scan_result_t *networks,
    size_t network_count,
    char *buffer,
    size_t buffer_size
);

#ifdef __cplusplus
}
#endif
