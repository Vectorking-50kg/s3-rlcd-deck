#pragma once

#include "deck_setup_mode.h"

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
} deck_setup_http_route_t;

typedef struct {
    char ssid[DECK_SETUP_SCAN_SSID_CAPACITY];
    int8_t rssi;
    bool secure;
} deck_setup_scan_result_t;

deck_setup_http_route_t deck_setup_http_route(const char *method, const char *path);
bool deck_setup_http_render_page(char *buffer, size_t buffer_size);
bool deck_setup_http_render_status(
    const deck_setup_snapshot_t *snapshot,
    const deck_setup_scan_result_t *networks,
    size_t network_count,
    char *buffer,
    size_t buffer_size
);

#ifdef __cplusplus
}
#endif
