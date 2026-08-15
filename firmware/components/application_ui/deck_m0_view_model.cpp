#include "deck_m0_view_model.h"
#include "deck_m0_glyphs.h"

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
    switch (state) {
        case DECK_SETUP_ACTIVE:
            return "ACTIVE";
        case DECK_SETUP_IDLE:
            return "IDLE";
        case DECK_SETUP_UNAVAILABLE:
        default:
            return "UNAVAILABLE";
    }
}

const char *wifi_config_state_name(deck_wifi_config_view_state_t state)
{
    switch (state) {
        case DECK_WIFI_CONFIG_VIEW_ACTIVE:
            return "ACTIVE";
        case DECK_WIFI_CONFIG_VIEW_VALIDATING:
            return "VALIDATING";
        case DECK_WIFI_CONFIG_VIEW_AUTH_FAILED:
            return "AUTH_FAILED";
        case DECK_WIFI_CONFIG_VIEW_TIMED_OUT:
            return "TIMED_OUT";
        case DECK_WIFI_CONFIG_VIEW_CONNECTION_FAILED:
            return "CONNECTION_FAILED";
        case DECK_WIFI_CONFIG_VIEW_STORAGE_ERROR:
            return "STORAGE_ERROR";
        case DECK_WIFI_CONFIG_VIEW_NO_ACTIVE:
        default:
            return "NO_ACTIVE";
    }
}

const char *wifi_record_status_name(deck_wifi_record_view_status_t status)
{
    switch (status) {
        case DECK_WIFI_RECORD_VIEW_VALID:
            return "VALID";
        case DECK_WIFI_RECORD_VIEW_RECOVERED_PREVIOUS:
            return "RECOVERED";
        case DECK_WIFI_RECORD_VIEW_CORRUPT:
            return "CORRUPT";
        case DECK_WIFI_RECORD_VIEW_UNSUPPORTED_SCHEMA:
            return "UNSUPPORTED";
        case DECK_WIFI_RECORD_VIEW_MIGRATION_FAILED:
            return "MIGRATION_FAILED";
        case DECK_WIFI_RECORD_VIEW_IO_ERROR:
            return "IO_ERROR";
        case DECK_WIFI_RECORD_VIEW_EMPTY:
        default:
            return "EMPTY";
    }
}

bool same_text(const char *left, const char *right)
{
    if (left == nullptr || right == nullptr) {
        return left == right;
    }
    return strcmp(left, right) == 0;
}

bool data_is_available(uint8_t source)
{
    return source == DECK_DATA_SIMULATED || source == DECK_DATA_VERIFIED;
}

bool serial_page_visible(const deck_serial_view_model_t &serial)
{
    return serial.state == DECK_SERIAL_VIEW_USB_TX ||
           serial.state == DECK_SERIAL_VIEW_WEB_TX;
}

const char *serial_owner_name(deck_serial_view_state_t state)
{
    return state == DECK_SERIAL_VIEW_WEB_TX ? "WEB TX" : "USB TX";
}

bool serial_equal(
    const deck_serial_view_model_t &left,
    const deck_serial_view_model_t &right
)
{
    return left.state == right.state && left.session_id == right.session_id &&
           left.owner_generation == right.owner_generation &&
           left.usb_tx_rejected == right.usb_tx_rejected &&
           left.uart_install_failures == right.uart_install_failures &&
           left.uart_fifo_overflows == right.uart_fifo_overflows &&
           left.uart_driver_buffer_full == right.uart_driver_buffer_full &&
           left.uart_install_failed == right.uart_install_failed &&
           left.uart_installed == right.uart_installed;
}

void format_uart_error_count(uint64_t count, char output[8])
{
    if (count > 999'999U) {
        memcpy(output, "999999+", 8);
        return;
    }
    (void)snprintf(output, 8, "%" PRIu64, count);
}

const char *serial_footer(const deck_serial_view_model_t &serial)
{
    if (serial.state == DECK_SERIAL_VIEW_UNAVAILABLE) {
        return "TX UNAVAILABLE";
    }
    if (serial.uart_install_failed) {
        return "TX UART ERROR";
    }
    return "TX DISARMED";
}

bool overlay_serial_footer(
    const deck_serial_view_model_t &serial,
    char *buffer,
    size_t buffer_size
)
{
    static constexpr char placeholder[] = "TX DISARMED";
    char *position = strstr(buffer, placeholder);
    if (position == nullptr) {
        return true;
    }
    const char *replacement = serial_footer(serial);
    const size_t old_size = sizeof(placeholder) - 1U;
    const size_t new_size = strlen(replacement);
    const size_t prefix_size = static_cast<size_t>(position - buffer);
    const size_t tail_size = strlen(position + old_size) + 1U;
    if (prefix_size + new_size + tail_size > buffer_size) {
        return false;
    }
    memmove(position + new_size, position + old_size, tail_size);
    memcpy(position, replacement, new_size);
    return true;
}

bool format_serial_page(
    const deck_serial_view_model_t &serial,
    char *buffer,
    size_t buffer_size
)
{
    if (!serial_page_visible(serial) || buffer == nullptr || buffer_size == 0) {
        return false;
    }
    char fifo_overflows[8]{};
    char driver_buffer_full[8]{};
    format_uart_error_count(serial.uart_fifo_overflows, fifo_overflows);
    format_uart_error_count(serial.uart_driver_buffer_full, driver_buffer_full);
    const bool uart_data_lost = serial.uart_fifo_overflows != 0 ||
                                serial.uart_driver_buffer_full != 0;
    const int size = snprintf(
        buffer,
        buffer_size,
        "SERIAL          %s\n"
        "%s F%s B%s\n"
        "--------------------------------\n"
        "115200  8N1  UART %s\n"
        "SESSION #%" PRIu64 "\n"
        "OWNER GEN %" PRIu64 "\n"
        "USB REJECTED %" PRIu64 " B\n"
        "UART INSTALL ERR %" PRIu32 "\n"
        "ROUTER ACTIVE / STATS LATEST\n"
        "--------------------------------\n"
        "KEY: Stats    BOOT: Exit",
        serial_owner_name(serial.state),
        uart_data_lost ? "!! UART RX LOSS" : "UART RX OK",
        fifo_overflows,
        driver_buffer_full,
        serial.uart_installed ? "OK" : "FAULT",
        serial.session_id,
        serial.owner_generation,
        serial.usb_tx_rejected,
        serial.uart_install_failures
    );
    return size >= 0 && static_cast<size_t>(size) < buffer_size;
}

}  // namespace

bool deck_m0_view_model_equal(const deck_m0_view_model_t *left, const deck_m0_view_model_t *right)
{
    if (left == nullptr || right == nullptr) {
        return left == right;
    }
    const bool left_serial_visible = serial_page_visible(left->serial);
    const bool right_serial_visible = serial_page_visible(right->serial);
    if (left_serial_visible || right_serial_visible) {
        return left_serial_visible == right_serial_visible &&
               serial_equal(left->serial, right->serial);
    }
    const bool left_ai_visible = left->ai_page.active && left->setup_state != DECK_SETUP_ACTIVE;
    const bool right_ai_visible = right->ai_page.active && right->setup_state != DECK_SETUP_ACTIVE;
    if (left_ai_visible || right_ai_visible) {
        return left_ai_visible == right_ai_visible &&
               deck_ai_page_view_model_equal(&left->ai_page, &right->ai_page) &&
               strcmp(serial_footer(left->serial), serial_footer(right->serial)) == 0;
    }
    return same_text(left->firmware_version, right->firmware_version) &&
           left->data_source == right->data_source && left->rtc_available == right->rtc_available &&
           left->rtc_hour == right->rtc_hour &&
           left->rtc_minute == right->rtc_minute &&
           left->rtc_error_count == right->rtc_error_count &&
           left->sensor_available == right->sensor_available &&
           left->raw_temperature_tenths_c == right->raw_temperature_tenths_c &&
           left->calibrated_temperature_tenths_c == right->calibrated_temperature_tenths_c &&
           left->humidity_tenths_percent == right->humidity_tenths_percent &&
           left->sensor_error_count == right->sensor_error_count &&
           left->buttons_available == right->buttons_available &&
           left->key_event == right->key_event &&
           left->key_event_count == right->key_event_count && left->boot_event == right->boot_event &&
           left->boot_event_count == right->boot_event_count && left->wifi_state == right->wifi_state &&
           left->wifi_config_state == right->wifi_config_state &&
           left->wifi_record_status == right->wifi_record_status &&
           left->wifi_candidate_record_status == right->wifi_candidate_record_status &&
           left->wifi_config_generation == right->wifi_config_generation &&
           left->setup_state == right->setup_state &&
           memcmp(left->setup_ssid, right->setup_ssid, sizeof(left->setup_ssid)) == 0 &&
           memcmp(left->setup_password, right->setup_password, sizeof(left->setup_password)) == 0 &&
           memcmp(left->setup_address, right->setup_address, sizeof(left->setup_address)) == 0 &&
           left->refresh_count == right->refresh_count &&
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
    const bool available = data_is_available(model->data_source);
    const char *source_suffix = " [UNAVAILABLE]";
    const char *rtc_state = "UNAVAILABLE";
    if (model->data_source == DECK_DATA_SIMULATED) {
        source_suffix = " [SIM]";
        rtc_state = "SIMULATED";
    } else if (model->data_source == DECK_DATA_VERIFIED) {
        source_suffix = "";
        rtc_state = "OK";
    }

    char rtc[64];
    const int rtc_size = available && model->rtc_available
                             ? snprintf(
                                   rtc,
                                   sizeof(rtc),
                                   "%02u:%02u / 状态 %s / RTC ERR %" PRIu32,
                                   model->rtc_hour,
                                   model->rtc_minute,
                                   rtc_state,
                                   model->rtc_error_count
                               )
                             : snprintf(
                                   rtc,
                                   sizeof(rtc),
                                   "--:-- / 状态 UNAVAILABLE / RTC ERR %" PRIu32,
                                   model->rtc_error_count
                               );
    if (rtc_size < 0 || static_cast<size_t>(rtc_size) >= sizeof(rtc)) {
        return false;
    }

    char sensor[160];
    int sensor_size = snprintf(
        sensor,
        sizeof(sensor),
        "温度 RAW --.-C / CAL --.-C\n湿度 --.-%% / SENSOR ERR %" PRIu32,
        model->sensor_error_count
    );
    if (available && model->sensor_available) {
        const int32_t raw_abs = model->raw_temperature_tenths_c < 0
                                    ? -static_cast<int32_t>(model->raw_temperature_tenths_c)
                                    : model->raw_temperature_tenths_c;
        const int32_t calibrated_abs = model->calibrated_temperature_tenths_c < 0
                                           ? -static_cast<int32_t>(model->calibrated_temperature_tenths_c)
                                           : model->calibrated_temperature_tenths_c;
        const char raw_sign = model->raw_temperature_tenths_c < 0 ? '-' : '+';
        const char calibrated_sign = model->calibrated_temperature_tenths_c < 0 ? '-' : '+';
        sensor_size = snprintf(
            sensor,
            sizeof(sensor),
            "温度 RAW %c%d.%dC / CAL %c%d.%dC\n湿度 %u.%u%% / SENSOR ERR %" PRIu32,
            raw_sign,
            static_cast<int>(raw_abs / 10),
            static_cast<int>(raw_abs % 10),
            calibrated_sign,
            static_cast<int>(calibrated_abs / 10),
            static_cast<int>(calibrated_abs % 10),
            static_cast<unsigned>(model->humidity_tenths_percent / 10U),
            static_cast<unsigned>(model->humidity_tenths_percent % 10U),
            model->sensor_error_count
        );
    }
    if (sensor_size < 0 || static_cast<size_t>(sensor_size) >= sizeof(sensor)) {
        return false;
    }

    char setup[256];
    const int setup_size = model->setup_state == DECK_SETUP_ACTIVE
                               ? snprintf(
                                     setup,
                                     sizeof(setup),
                                     "Wi-Fi %s / CFG %s #%" PRIu32
                                     " / REC %s/%s / Setup ACTIVE\n"
                                     "AP %s\nPASS %s\nHTTP http://%s",
                                     wifi_state_name(model->wifi_state),
                                     wifi_config_state_name(model->wifi_config_state),
                                     model->wifi_config_generation,
                                     wifi_record_status_name(model->wifi_record_status),
                                     wifi_record_status_name(model->wifi_candidate_record_status),
                                     model->setup_ssid,
                                     model->setup_password,
                                     model->setup_address
                                 )
                               : snprintf(
                                     setup,
                                     sizeof(setup),
                                     "Wi-Fi %s / CFG %s #%" PRIu32
                                     " / REC %s/%s / Setup %s",
                                     wifi_state_name(model->wifi_state),
                                     wifi_config_state_name(model->wifi_config_state),
                                     model->wifi_config_generation,
                                     wifi_record_status_name(model->wifi_record_status),
                                     wifi_record_status_name(model->wifi_candidate_record_status),
                                     setup_state_name(model->setup_state)
                                 );
    if (setup_size < 0 || static_cast<size_t>(setup_size) >= sizeof(setup)) {
        return false;
    }

    const int size = snprintf(
        buffer,
        buffer_size,
        "S3 RLCD Deck / M0 诊断%s\n"
        "FW %s / UP %02" PRIu64 ":%02" PRIu64 ":%02" PRIu64 "\n"
        "RTC %s\n"
        "%s\n"
        "KEY %s #%" PRIu32 " / BOOT %s #%" PRIu32 "\n"
        "%s\n"
        "刷新 %" PRIu32 " / 最低堆 %" PRIu32 " KiB\n"
        "Companion 配对 M1",
        source_suffix,
        model->firmware_version,
        hours,
        minutes,
        seconds,
        rtc,
        sensor,
        available && model->buttons_available ? button_event_name(model->key_event) : "UNAVAILABLE",
        model->key_event_count,
        available && model->buttons_available ? button_event_name(model->boot_event) : "UNAVAILABLE",
        model->boot_event_count,
        setup,
        model->refresh_count,
        model->minimum_free_heap_bytes / 1024U
    );
    return size >= 0 && static_cast<size_t>(size) < buffer_size;
}

bool deck_m0_view_model_format_active_page(
    const deck_m0_view_model_t *model,
    char *buffer,
    size_t buffer_size,
    bool *ai_page_visible
)
{
    if (model == nullptr || ai_page_visible == nullptr) {
        return false;
    }
    if (serial_page_visible(model->serial)) {
        *ai_page_visible = false;
        return format_serial_page(model->serial, buffer, buffer_size);
    }
    *ai_page_visible = model->ai_page.active && model->setup_state != DECK_SETUP_ACTIVE;
    if (!*ai_page_visible) {
        return deck_m0_view_model_format(model, buffer, buffer_size);
    }
    return deck_ai_page_view_model_format(&model->ai_page, buffer, buffer_size) &&
           overlay_serial_footer(model->serial, buffer, buffer_size);
}

const char *deck_m0_required_glyphs(void)
{
    return DECK_M0_REQUIRED_GLYPHS;
}
