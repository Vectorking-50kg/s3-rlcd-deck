#include "deck_m0_view_model.h"

#include <inttypes.h>
#include <stdio.h>
#include <string.h>

namespace {

const char *button_event_name(deck_button_event_t event)
{
    switch (event) {
        case DECK_BUTTON_SHORT_PRESS:
            return "短按";
        case DECK_BUTTON_LONG_PRESS:
            return "长按";
        case DECK_BUTTON_NONE:
        default:
            return "NONE";
    }
}

const char *wifi_state_name(deck_wifi_state_t state)
{
    switch (state) {
        case DECK_WIFI_DISCONNECTED:
            return "DISCONNECTED";
        case DECK_WIFI_CONNECTED:
            return "CONNECTED";
        case DECK_WIFI_UNAVAILABLE:
        default:
            return "UNAVAILABLE";
    }
}

const char *setup_state_name(deck_setup_state_t state)
{
    return state == DECK_SETUP_ACTIVE ? "ACTIVE" : "IDLE";
}

bool same_text(const char *left, const char *right)
{
    if (left == nullptr || right == nullptr) {
        return left == right;
    }
    return strcmp(left, right) == 0;
}

}  // namespace

bool deck_m0_view_model_equal(const deck_m0_view_model_t *left, const deck_m0_view_model_t *right)
{
    if (left == nullptr || right == nullptr) {
        return left == right;
    }
    return same_text(left->firmware_version, right->firmware_version) &&
           left->rtc_available == right->rtc_available && left->rtc_hour == right->rtc_hour &&
           left->rtc_minute == right->rtc_minute &&
           left->raw_temperature_tenths_c == right->raw_temperature_tenths_c &&
           left->calibrated_temperature_tenths_c == right->calibrated_temperature_tenths_c &&
           left->humidity_tenths_percent == right->humidity_tenths_percent &&
           left->sensor_error_count == right->sensor_error_count && left->key_event == right->key_event &&
           left->key_event_count == right->key_event_count && left->boot_event == right->boot_event &&
           left->boot_event_count == right->boot_event_count && left->wifi_state == right->wifi_state &&
           left->setup_state == right->setup_state && left->refresh_count == right->refresh_count &&
           left->uptime_seconds == right->uptime_seconds &&
           left->minimum_free_heap_bytes == right->minimum_free_heap_bytes;
}

bool deck_m0_view_model_format(const deck_m0_view_model_t *model, char *buffer, size_t buffer_size)
{
    if (model == nullptr || model->firmware_version == nullptr || buffer == nullptr || buffer_size == 0) {
        return false;
    }

    const uint64_t hours = model->uptime_seconds / 3600U;
    const uint64_t minutes = model->uptime_seconds / 60U % 60U;
    const uint64_t seconds = model->uptime_seconds % 60U;
    const int32_t raw_abs = model->raw_temperature_tenths_c < 0
                                ? -static_cast<int32_t>(model->raw_temperature_tenths_c)
                                : model->raw_temperature_tenths_c;
    const int32_t calibrated_abs = model->calibrated_temperature_tenths_c < 0
                                       ? -static_cast<int32_t>(model->calibrated_temperature_tenths_c)
                                       : model->calibrated_temperature_tenths_c;
    const char raw_sign = model->raw_temperature_tenths_c < 0 ? '-' : '+';
    const char calibrated_sign = model->calibrated_temperature_tenths_c < 0 ? '-' : '+';
    char rtc[32];
    const int rtc_size = model->rtc_available
                             ? snprintf(rtc, sizeof(rtc), "%02u:%02u / 状态 OK", model->rtc_hour, model->rtc_minute)
                             : snprintf(rtc, sizeof(rtc), "--:-- / 状态 UNAVAILABLE");
    if (rtc_size < 0 || static_cast<size_t>(rtc_size) >= sizeof(rtc)) {
        return false;
    }

    const int size = snprintf(
        buffer,
        buffer_size,
        "S3 RLCD Deck / M0 诊断\n"
        "FW %s / UP %02" PRIu64 ":%02" PRIu64 ":%02" PRIu64 "\n"
        "RTC %s\n"
        "温度 RAW %c%d.%dC / CAL %c%d.%dC\n"
        "湿度 %u.%u%% / SENSOR ERR %" PRIu32 "\n"
        "KEY %s #%" PRIu32 " / BOOT %s #%" PRIu32 "\n"
        "Wi-Fi %s / Setup %s\n"
        "刷新 %" PRIu32 " / 最低堆 %" PRIu32 " KiB\n"
        "Companion 配对 M1",
        model->firmware_version,
        hours,
        minutes,
        seconds,
        rtc,
        raw_sign,
        static_cast<int>(raw_abs / 10),
        static_cast<int>(raw_abs % 10),
        calibrated_sign,
        static_cast<int>(calibrated_abs / 10),
        static_cast<int>(calibrated_abs % 10),
        static_cast<unsigned>(model->humidity_tenths_percent / 10U),
        static_cast<unsigned>(model->humidity_tenths_percent % 10U),
        model->sensor_error_count,
        button_event_name(model->key_event),
        model->key_event_count,
        button_event_name(model->boot_event),
        model->boot_event_count,
        wifi_state_name(model->wifi_state),
        setup_state_name(model->setup_state),
        model->refresh_count,
        model->minimum_free_heap_bytes / 1024U
    );
    return size >= 0 && static_cast<size_t>(size) < buffer_size;
}

const char *deck_m0_required_glyphs(void)
{
    return "诊断状态温度湿度短按长刷新最低堆配对";
}
