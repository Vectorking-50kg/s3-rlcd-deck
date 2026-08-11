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

namespace {

const char *button_event_name(deck_diagnostic_button_event_t event)
{
    switch (event) {
        case DECK_DIAGNOSTIC_BUTTON_SHORT_PRESS:
            return "short_press";
        case DECK_DIAGNOSTIC_BUTTON_LONG_PRESS:
            return "long_press";
        case DECK_DIAGNOSTIC_BUTTON_NONE:
        default:
            return "none";
    }
}

bool emit_display_diagnostics(
    const char *event_type,
    const deck_display_ready_info_t *info,
    deck_diagnostic_sink_t sink
)
{
    if (event_type == nullptr || info == nullptr || sink.write == nullptr) {
        return false;
    }

    char line[320];
    const int size = snprintf(
        line,
        sizeof(line),
        "{\"type\":\"%s\",\"width\":%" PRIu16 ",\"height\":%" PRIu16 ","
        "\"frame_bytes\":%" PRIu32 ",\"submitted_frames\":%" PRIu32 ","
        "\"completed_frames\":%" PRIu32 ",\"transfer_timeouts\":%" PRIu32 ","
        "\"start_failures\":%" PRIu32 ",\"rejected_updates\":%" PRIu32 "}\n",
        event_type,
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

}  // namespace

bool deck_display_diagnostics_emit(const deck_display_ready_info_t *info, deck_diagnostic_sink_t sink)
{
    return emit_display_diagnostics("display_ready", info, sink);
}

bool deck_display_progress_diagnostics_emit(
    const deck_display_ready_info_t *info,
    deck_diagnostic_sink_t sink
)
{
    return emit_display_diagnostics("display_progress", info, sink);
}

bool deck_peripheral_diagnostics_emit(
    const deck_peripheral_diagnostic_info_t *info,
    deck_diagnostic_sink_t sink
)
{
    if (info == nullptr || sink.write == nullptr) {
        return false;
    }

    char line[512];
    const int size = snprintf(
        line,
        sizeof(line),
        "{\"type\":\"peripheral_state\",\"rtc_available\":%s,"
        "\"rtc_hour\":%" PRIu8 ",\"rtc_minute\":%" PRIu8 ","
        "\"sensor_available\":%s,\"raw_temperature_tenths_c\":%" PRIi16 ","
        "\"calibrated_temperature_tenths_c\":%" PRIi16 ","
        "\"humidity_tenths_percent\":%" PRIu16 ",\"buttons_available\":%s,"
        "\"key_event\":\"%s\","
        "\"key_event_count\":%" PRIu32 ",\"boot_event\":\"%s\","
        "\"boot_event_count\":%" PRIu32 ",\"rtc_errors\":%" PRIu32 ","
        "\"sensor_errors\":%" PRIu32 "}\n",
        info->rtc_available ? "true" : "false",
        info->rtc_hour,
        info->rtc_minute,
        info->sensor_available ? "true" : "false",
        info->raw_temperature_tenths_c,
        info->calibrated_temperature_tenths_c,
        info->humidity_tenths_percent,
        info->buttons_available ? "true" : "false",
        button_event_name(info->key_event),
        info->key_event_count,
        button_event_name(info->boot_event),
        info->boot_event_count,
        info->rtc_error_count,
        info->sensor_error_count
    );
    if (size < 0 || static_cast<size_t>(size) >= sizeof(line)) {
        return false;
    }
    sink.write(sink.context, line, static_cast<size_t>(size));
    return true;
}

bool deck_setup_diagnostics_emit(
    const deck_setup_diagnostic_info_t *info,
    deck_diagnostic_sink_t sink
)
{
    if (info == nullptr || info->reason == nullptr || info->ssid == nullptr ||
        info->address == nullptr || info->wifi_config_state == nullptr ||
        info->wifi_record_status == nullptr ||
        info->wifi_candidate_record_status == nullptr ||
        info->device_settings_state == nullptr ||
        info->device_settings_record_status == nullptr ||
        info->device_settings_candidate_record_status == nullptr ||
        sink.write == nullptr) {
        return false;
    }
    const char *error_stage = info->error_stage == nullptr ? "" : info->error_stage;
    char line[768];
    const int size = snprintf(
        line,
        sizeof(line),
        "{\"type\":\"setup_state\",\"active\":%s,\"reason\":\"%s\","
        "\"session_id\":%" PRIu32 ",\"ssid\":\"%s\",\"address\":\"%s\","
        "\"error_stage\":\"%s\",\"wifi_config_state\":\"%s\","
        "\"wifi_record_status\":\"%s\","
        "\"wifi_candidate_record_status\":\"%s\","
        "\"wifi_has_active\":%s,\"wifi_has_candidate\":%s,"
        "\"wifi_generation\":%" PRIu32 ","
        "\"device_settings_state\":\"%s\","
        "\"device_settings_record_status\":\"%s\","
        "\"device_settings_candidate_record_status\":\"%s\","
        "\"device_settings_has_active\":%s,\"device_settings_has_candidate\":%s,"
        "\"device_settings_generation\":%" PRIu32 ","
        "\"temperature_offset_tenths_c\":%" PRIi16 "}\n",
        info->active ? "true" : "false",
        info->reason,
        info->session_id,
        info->ssid,
        info->address,
        error_stage,
        info->wifi_config_state,
        info->wifi_record_status,
        info->wifi_candidate_record_status,
        info->wifi_has_active ? "true" : "false",
        info->wifi_has_candidate ? "true" : "false",
        info->wifi_generation,
        info->device_settings_state,
        info->device_settings_record_status,
        info->device_settings_candidate_record_status,
        info->device_settings_has_active ? "true" : "false",
        info->device_settings_has_candidate ? "true" : "false",
        info->device_settings_generation,
        info->temperature_offset_tenths_c
    );
    if (size < 0 || static_cast<size_t>(size) >= sizeof(line)) {
        return false;
    }
    sink.write(sink.context, line, static_cast<size_t>(size));
    return true;
}
