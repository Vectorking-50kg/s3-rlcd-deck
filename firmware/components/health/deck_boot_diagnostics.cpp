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
