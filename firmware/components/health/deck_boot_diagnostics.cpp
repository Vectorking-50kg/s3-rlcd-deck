#include "deck_boot_diagnostics.h"

#include <inttypes.h>
#include <stdio.h>

bool deck_boot_diagnostics_emit(const deck_boot_info_t *info, deck_diagnostic_sink_t sink)
{
    if (info == nullptr || info->firmware_version == nullptr || info->reset_reason == nullptr ||
        sink.write == nullptr) {
        return false;
    }

    char line[256];
    const int size = snprintf(
        line,
        sizeof(line),
        "{\"type\":\"boot_ok\",\"firmware_version\":\"%s\","
        "\"reset_reason\":\"%s\",\"uptime_ms\":%" PRIu64 ","
        "\"minimum_free_heap_bytes\":%" PRIu32 "}\n",
        info->firmware_version,
        info->reset_reason,
        info->uptime_ms,
        info->minimum_free_heap_bytes
    );
    if (size < 0 || static_cast<size_t>(size) >= sizeof(line)) {
        return false;
    }

    sink.write(sink.context, line, static_cast<size_t>(size));
    return true;
}

bool deck_display_diagnostics_emit(const deck_display_ready_info_t *info, deck_diagnostic_sink_t sink)
{
    if (info == nullptr || sink.write == nullptr) {
        return false;
    }

    char line[320];
    const int size = snprintf(
        line,
        sizeof(line),
        "{\"type\":\"display_ready\",\"width\":%" PRIu16 ",\"height\":%" PRIu16 ","
        "\"frame_bytes\":%" PRIu32 ",\"submitted_frames\":%" PRIu32 ","
        "\"completed_frames\":%" PRIu32 ",\"transfer_timeouts\":%" PRIu32 ","
        "\"start_failures\":%" PRIu32 ",\"rejected_updates\":%" PRIu32 "}\n",
        info->width,
        info->height,
        info->frame_bytes,
        info->submitted_frames,
        info->completed_frames,
        info->transfer_timeouts,
        info->start_failures,
        info->rejected_updates
    );
    if (size < 0 || static_cast<size_t>(size) >= sizeof(line)) {
        return false;
    }
    sink.write(sink.context, line, static_cast<size_t>(size));
    return true;
}
