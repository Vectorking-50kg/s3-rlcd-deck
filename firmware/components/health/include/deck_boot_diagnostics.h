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

typedef void (*deck_diagnostic_write_fn)(void *context, const char *data, size_t size);

typedef struct {
    deck_diagnostic_write_fn write;
    void *context;
} deck_diagnostic_sink_t;

bool deck_boot_diagnostics_emit(const deck_boot_info_t *info, deck_diagnostic_sink_t sink);
bool deck_display_diagnostics_emit(const deck_display_ready_info_t *info, deck_diagnostic_sink_t sink);

#ifdef __cplusplus
}
#endif
