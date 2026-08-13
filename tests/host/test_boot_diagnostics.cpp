#include "deck_boot_diagnostics.h"

#include <cstdlib>
#include <iostream>
#include <string>

namespace {

void append_to_string(void *context, const char *data, size_t size)
{
    static_cast<std::string *>(context)->append(data, size);
}

void require(bool condition, const char *message)
{
    if (!condition) {
        std::cerr << message << '\n';
        std::exit(EXIT_FAILURE);
    }
}

}  // namespace

int main()
{
    const deck_boot_info_t info = {
        "0.1.0-dev",
        "power_on",
        42,
        131072,
    };
    std::string output;
    const deck_diagnostic_sink_t sink = {
        append_to_string,
        &output,
    };

    const bool emitted = deck_boot_diagnostics_emit(&info, sink);

    require(emitted, "expected the boot event to be emitted");
    require(
        output ==
            "{\"type\":\"boot_ok\",\"firmware_version\":\"0.1.0-dev\","
            "\"reset_reason\":\"power_on\",\"uptime_ms\":42,"
            "\"minimum_free_heap_bytes\":131072}\n",
        "expected one complete boot_ok JSON line"
    );

    output.clear();
    const deck_display_ready_info_t display_info = {
        400,
        300,
        15000,
        1,
        1,
        0,
        0,
        0,
    };
    require(
        deck_display_diagnostics_emit(&display_info, sink),
        "expected the display event to be emitted"
    );
    require(
        output ==
            "{\"type\":\"display_ready\",\"width\":400,\"height\":300,"
            "\"frame_bytes\":15000,\"submitted_frames\":1,\"completed_frames\":1,"
            "\"transfer_timeouts\":0,\"start_failures\":0,\"rejected_updates\":0}\n",
        "expected one complete display_ready JSON line"
    );

    output.clear();
    const deck_display_ready_info_t progress_info = {
        400,
        300,
        15000,
        3,
        3,
        0,
        0,
        0,
    };
    require(
        deck_display_progress_diagnostics_emit(&progress_info, sink),
        "expected the display progress event to be emitted"
    );
    require(
        output ==
            "{\"type\":\"display_progress\",\"width\":400,\"height\":300,"
            "\"frame_bytes\":15000,\"submitted_frames\":3,\"completed_frames\":3,"
            "\"transfer_timeouts\":0,\"start_failures\":0,\"rejected_updates\":0}\n",
        "expected one complete display_progress JSON line"
    );

    output.clear();
    const deck_peripheral_diagnostic_info_t peripheral_info = {
        false,
        0,
        0,
        true,
        237,
        197,
        630,
        true,
        DECK_DIAGNOSTIC_BUTTON_LONG_PRESS,
        2,
        DECK_DIAGNOSTIC_BUTTON_SHORT_PRESS,
        1,
        0,
        3,
        100000,
        90000,
    };
    require(
        deck_peripheral_diagnostics_emit(&peripheral_info, sink),
        "expected the peripheral state event to be emitted"
    );
    require(
        output ==
            "{\"type\":\"peripheral_state\",\"rtc_available\":false,\"rtc_hour\":0,"
            "\"rtc_minute\":0,\"sensor_available\":true,"
            "\"raw_temperature_tenths_c\":237,"
            "\"calibrated_temperature_tenths_c\":197,"
            "\"humidity_tenths_percent\":630,\"buttons_available\":true,"
            "\"key_event\":\"long_press\","
            "\"key_event_count\":2,\"boot_event\":\"short_press\","
            "\"boot_event_count\":1,\"rtc_errors\":0,\"sensor_errors\":3,"
            "\"free_heap_bytes\":100000,"
            "\"minimum_free_heap_bytes\":90000}\n",
        "expected one complete peripheral_state JSON line"
    );

    output.clear();
    const deck_setup_diagnostic_info_t setup_info = {
        true,
        "no_wifi_config",
        7,
        "S3-RLCD-A1B2",
        "192.168.4.1",
        nullptr,
        "validating",
        "valid",
        "corrupt",
        true,
        true,
        7,
        "active",
        "valid",
        "empty",
        true,
        false,
        3,
        -35,
    };
    require(
        deck_setup_diagnostics_emit(&setup_info, sink),
        "expected the setup state event to be emitted"
    );
    require(
        output ==
            "{\"type\":\"setup_state\",\"active\":true,"
            "\"reason\":\"no_wifi_config\",\"session_id\":7,"
            "\"ssid\":\"S3-RLCD-A1B2\",\"address\":\"192.168.4.1\","
            "\"error_stage\":\"\",\"wifi_config_state\":\"validating\","
            "\"wifi_record_status\":\"valid\","
            "\"wifi_candidate_record_status\":\"corrupt\","
            "\"wifi_has_active\":true,\"wifi_has_candidate\":true,"
            "\"wifi_generation\":7,\"device_settings_state\":\"active\","
            "\"device_settings_record_status\":\"valid\","
            "\"device_settings_candidate_record_status\":\"empty\","
            "\"device_settings_has_active\":true,"
            "\"device_settings_has_candidate\":false,"
            "\"device_settings_generation\":3,"
            "\"temperature_offset_tenths_c\":-35}\n",
        "expected setup diagnostics to omit the ephemeral password"
    );

    output.clear();
    const deck_companion_link_diagnostic_info_t companion_info = {
        "online",
        true,
        4,
        2,
        1,
        123456,
    };
    require(
        deck_companion_link_diagnostics_emit(&companion_info, sink),
        "expected the Companion Link state event to be emitted"
    );
    require(
        output ==
            "{\"type\":\"companion_link_state\",\"state\":\"online\","
            "\"has_active_profile\":true,\"profile_generation\":4,"
            "\"reconnect_attempts\":2,\"error_count\":1,"
            "\"last_heartbeat_monotonic_ms\":123456}\n",
        "expected Companion Link diagnostics to contain state and counters only"
    );
}
