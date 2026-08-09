#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

enum {
    DECK_DISPLAY_WIDTH = 400,
    DECK_DISPLAY_HEIGHT = 300,
    DECK_DISPLAY_FRAME_BYTES = DECK_DISPLAY_WIDTH * DECK_DISPLAY_HEIGHT / 8,
};

typedef struct {
    int16_t x1;
    int16_t y1;
    int16_t x2;
    int16_t y2;
} deck_display_area_t;

typedef enum {
    DECK_DISPLAY_PACK_CHANGED = 0,
    DECK_DISPLAY_PACK_UNCHANGED,
    DECK_DISPLAY_PACK_INVALID_ARGUMENT,
} deck_display_pack_result_t;

deck_display_pack_result_t deck_display_pack_rgb565(
    uint8_t *frame,
    size_t frame_size,
    deck_display_area_t area,
    const uint16_t *pixels,
    size_t pixel_count
);

typedef struct deck_display_service deck_display_service_t;

typedef void (*deck_display_transfer_done_fn)(void *context);
typedef bool (*deck_display_start_transfer_fn)(
    void *context,
    const uint8_t *frame,
    size_t frame_size,
    deck_display_transfer_done_fn done,
    void *done_context
);

typedef struct {
    deck_display_start_transfer_fn start_transfer;
    void *context;
} deck_display_panel_adapter_t;

typedef enum {
    DECK_DISPLAY_UPDATED = 0,
    DECK_DISPLAY_UNCHANGED,
    DECK_DISPLAY_SUBMITTED,
    DECK_DISPLAY_IN_FLIGHT,
    DECK_DISPLAY_COMPLETED,
    DECK_DISPLAY_TIMED_OUT,
    DECK_DISPLAY_START_FAILED,
    DECK_DISPLAY_INVALID_ARGUMENT,
} deck_display_result_t;

typedef struct {
    uint32_t submitted_frames;
    uint32_t completed_frames;
    uint32_t coalesced_submissions;
    uint32_t transfer_timeouts;
    uint32_t start_failures;
    uint32_t rejected_updates;
} deck_display_metrics_t;

/*
 * The service owns one logical 1bpp working framebuffer plus bounded immutable
 * transfer and last-successful snapshots. Updates are merged into the working frame
 * while a transfer is in flight. The panel adapter must invoke done exactly once for
 * every accepted transfer, even if completion arrives after the configured timeout.
 * Destroy refuses to release the service while that callback is still outstanding.
 */
deck_display_service_t *deck_display_service_create(
    deck_display_panel_adapter_t adapter,
    uint32_t transfer_timeout_ms
);
bool deck_display_service_destroy(deck_display_service_t *display);
deck_display_result_t deck_display_service_update(
    deck_display_service_t *display,
    deck_display_area_t area,
    const uint16_t *pixels,
    size_t pixel_count
);
deck_display_result_t deck_display_service_submit(deck_display_service_t *display, uint64_t now_ms);
deck_display_result_t deck_display_service_poll(deck_display_service_t *display, uint64_t now_ms);
deck_display_metrics_t deck_display_service_metrics(const deck_display_service_t *display);

#ifdef __cplusplus
}
#endif
