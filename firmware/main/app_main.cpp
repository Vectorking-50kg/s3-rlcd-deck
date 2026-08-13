#include "sdkconfig.h"

#include <cstring>

#include "deck_application_ui.h"
#include "deck_peripherals.h"
#include "deck_boot_diagnostics.h"
#include "deck_companion_link.h"
#include "deck_device_settings.h"
#include "deck_display.h"
#include "deck_m0_view_model.h"
#include "deck_rlcd_panel.h"
#include "deck_setup_service.h"

#include "esp_app_desc.h"
#include "esp_system.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"

namespace {

static_assert(DECK_M0_SETUP_SSID_CAPACITY == DECK_SETUP_SSID_CAPACITY);
static_assert(DECK_M0_SETUP_PASSWORD_CAPACITY == DECK_SETUP_PASSWORD_CAPACITY);
static_assert(DECK_M0_SETUP_ADDRESS_CAPACITY == DECK_SETUP_ADDRESS_CAPACITY);

deck_rlcd_panel_t *application_panel = nullptr;
deck_display_service_t *application_display = nullptr;
deck_peripherals_t *application_peripherals = nullptr;
deck_setup_service_t *application_setup = nullptr;
deck_companion_link_t *application_companion_link = nullptr;
deck_m0_view_model_t application_model{};
SemaphoreHandle_t application_model_mutex = nullptr;
uint32_t handled_boot_long_press_count = 0;
int16_t application_temperature_offset_tenths_c =
    DECK_DEVICE_SETTINGS_DEFAULT_TEMPERATURE_OFFSET_TENTHS_C;

}  // namespace

#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE

#include <cerrno>
#include <fcntl.h>
#include <stdio.h>
#include <unistd.h>

#include "driver/usb_serial_jtag.h"
#include "driver/usb_serial_jtag_vfs.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

namespace {

bool initialize_diagnostic_console_driver()
{
    usb_serial_jtag_driver_config_t configuration =
        USB_SERIAL_JTAG_DRIVER_CONFIG_DEFAULT();
    if (usb_serial_jtag_driver_install(&configuration) != ESP_OK) {
        return false;
    }
    usb_serial_jtag_vfs_use_driver();
    return true;
}

const char *reset_reason_name(esp_reset_reason_t reason)
{
    switch (reason) {
        case ESP_RST_POWERON:
            return "power_on";
        case ESP_RST_EXT:
            return "external";
        case ESP_RST_SW:
            return "software";
        case ESP_RST_PANIC:
            return "panic";
        case ESP_RST_INT_WDT:
            return "interrupt_watchdog";
        case ESP_RST_TASK_WDT:
            return "task_watchdog";
        case ESP_RST_WDT:
            return "watchdog";
        case ESP_RST_DEEPSLEEP:
            return "deep_sleep";
        case ESP_RST_BROWNOUT:
            return "brownout";
        case ESP_RST_SDIO:
            return "sdio";
        case ESP_RST_USB:
            return "usb";
        case ESP_RST_JTAG:
            return "jtag";
        case ESP_RST_EFUSE:
            return "efuse";
        case ESP_RST_PWR_GLITCH:
            return "power_glitch";
        case ESP_RST_CPU_LOCKUP:
            return "cpu_lockup";
        case ESP_RST_UNKNOWN:
        default:
            return "unknown";
    }
}

void write_stdout(void *, const char *data, size_t size)
{
    fwrite(data, 1, size, stdout);
    fflush(stdout);
}

bool diagnostic_host_ready()
{
    constexpr char ready_message[] = "DECK_HIL_READY\n";
    static size_t matched = 0;
    char input[16];
    const ssize_t size = read(STDIN_FILENO, input, sizeof(input));
    for (ssize_t index = 0; index < size; ++index) {
        if (input[index] == ready_message[matched]) {
            ++matched;
            if (matched == sizeof(ready_message) - 1) {
                return true;
            }
        } else {
            matched = input[index] == ready_message[0] ? 1 : 0;
        }
    }
    return false;
}

void wait_for_diagnostic_host_ready()
{
    constexpr int64_t timeout_us = 10'000'000;
    const int current_flags = fcntl(STDIN_FILENO, F_GETFL);
    if (current_flags >= 0) {
        fcntl(STDIN_FILENO, F_SETFL, current_flags | O_NONBLOCK);
    }

    const int64_t deadline = esp_timer_get_time() + timeout_us;
    while (!diagnostic_host_ready() && esp_timer_get_time() < deadline) {
        vTaskDelay(pdMS_TO_TICKS(10));
    }
}

void display_start_error(const char *stage)
{
    char line[128];
    const int size = snprintf(line, sizeof(line), "{\"type\":\"display_error\",\"stage\":\"%s\"}\n", stage);
    if (size > 0 && static_cast<size_t>(size) < sizeof(line)) {
        write_stdout(nullptr, line, static_cast<size_t>(size));
    }
}

}  // namespace

#endif

namespace {

struct ButtonEventMapping {
    deck_button_event_t view;
    deck_diagnostic_button_event_t diagnostic;
};

ButtonEventMapping map_button_event(deck_button_input_event_t event)
{
    switch (event) {
        case DECK_BUTTON_INPUT_SHORT_PRESS:
            return {DECK_BUTTON_SHORT_PRESS, DECK_DIAGNOSTIC_BUTTON_SHORT_PRESS};
        case DECK_BUTTON_INPUT_LONG_PRESS:
            return {DECK_BUTTON_LONG_PRESS, DECK_DIAGNOSTIC_BUTTON_LONG_PRESS};
        case DECK_BUTTON_INPUT_NONE:
        default:
            return {DECK_BUTTON_NONE, DECK_DIAGNOSTIC_BUTTON_NONE};
    }
}

#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
const char *companion_link_state_name(deck_companion_link_state_t state)
{
    switch (state) {
        case DECK_COMPANION_LINK_OFFLINE:
            return "offline";
        case DECK_COMPANION_LINK_CONNECTING:
            return "connecting";
        case DECK_COMPANION_LINK_ONLINE:
            return "online";
        case DECK_COMPANION_LINK_UNPAIRED:
        default:
            return "unpaired";
    }
}

void emit_companion_link_diagnostics()
{
    deck_companion_link_snapshot_t snapshot{};
    if (application_companion_link == nullptr ||
        !deck_companion_link_snapshot(application_companion_link, &snapshot)) {
        return;
    }
    const deck_companion_link_diagnostic_info_t info = {
        companion_link_state_name(snapshot.state),
        snapshot.has_active_profile,
        snapshot.profile_generation,
        snapshot.reconnect_attempts,
        snapshot.error_count,
        snapshot.last_heartbeat_monotonic_ms,
    };
    const deck_diagnostic_sink_t sink = {write_stdout, nullptr};
    (void)deck_companion_link_diagnostics_emit(&info, sink);
}
#endif

bool release_display_resources()
{
    if (application_display != nullptr) {
        if (!deck_display_service_destroy(application_display)) {
            return false;
        }
        application_display = nullptr;
    }
    if (application_panel != nullptr) {
        deck_rlcd_panel_destroy(application_panel);
        application_panel = nullptr;
    }
    return true;
}

deck_m0_view_model_t make_initial_model(
    const char *firmware_version,
    uint64_t uptime_seconds,
    uint32_t minimum_free_heap_bytes
)
{
    deck_m0_view_model_t model = {
        firmware_version,
        DECK_DATA_UNAVAILABLE,
        false,
        0,
        0,
        0,
        false,
        0,
        0,
        0,
        0,
        false,
        DECK_BUTTON_NONE,
        0,
        DECK_BUTTON_NONE,
        0,
        DECK_WIFI_UNAVAILABLE,
        DECK_WIFI_CONFIG_VIEW_NO_ACTIVE,
        DECK_WIFI_RECORD_VIEW_EMPTY,
        DECK_WIFI_RECORD_VIEW_EMPTY,
        0,
        DECK_SETUP_UNAVAILABLE,
        {},
        {},
        {},
        0,
        uptime_seconds,
        minimum_free_heap_bytes,
    };
    return model;
}

bool publish_application_model(deck_m0_view_model_t *published)
{
    if (published == nullptr || application_model_mutex == nullptr ||
        xSemaphoreTake(application_model_mutex, portMAX_DELAY) != pdTRUE) {
        return false;
    }
    *published = application_model;
    xSemaphoreGive(application_model_mutex);
    return deck_application_ui_update(published);
}

void peripheral_snapshot(void *, const deck_peripheral_snapshot_t *snapshot)
{
    if (snapshot == nullptr) {
        return;
    }
    const ButtonEventMapping key_event = map_button_event(snapshot->key_event);
    const ButtonEventMapping boot_event = map_button_event(snapshot->boot_event);
    bool enter_setup = false;
    uint32_t pending_boot_long_press_count = 0;
    if (application_model_mutex != nullptr &&
        xSemaphoreTake(application_model_mutex, portMAX_DELAY) == pdTRUE) {
        application_model.data_source = DECK_DATA_VERIFIED;
        application_model.rtc_available = snapshot->rtc_available;
        application_model.rtc_hour = snapshot->rtc_hour;
        application_model.rtc_minute = snapshot->rtc_minute;
        application_model.rtc_error_count = snapshot->rtc_error_count;
        application_model.sensor_available = snapshot->sensor_available;
        application_model.raw_temperature_tenths_c = snapshot->raw_temperature_tenths_c;
        application_model.calibrated_temperature_tenths_c =
            snapshot->calibrated_temperature_tenths_c;
        application_model.humidity_tenths_percent = snapshot->humidity_tenths_percent;
        application_model.sensor_error_count = snapshot->sensor_error_count;
        application_model.buttons_available = snapshot->buttons_available;
        application_model.key_event = key_event.view;
        application_model.key_event_count = snapshot->key_event_count;
        application_model.boot_event = boot_event.view;
        application_model.boot_event_count = snapshot->boot_event_count;
        if (snapshot->boot_event == DECK_BUTTON_INPUT_LONG_PRESS &&
            snapshot->boot_event_count > handled_boot_long_press_count) {
            pending_boot_long_press_count = snapshot->boot_event_count;
            enter_setup = true;
        }
        xSemaphoreGive(application_model_mutex);
    }
    deck_m0_view_model_t published{};
    (void)publish_application_model(&published);
    if (enter_setup && application_setup != nullptr &&
        deck_setup_service_enter_from_boot(application_setup)) {
        handled_boot_long_press_count = pending_boot_long_press_count;
    }
#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
    const deck_peripheral_diagnostic_info_t info = {
        snapshot->rtc_available,
        snapshot->rtc_hour,
        snapshot->rtc_minute,
        snapshot->sensor_available,
        snapshot->raw_temperature_tenths_c,
        snapshot->calibrated_temperature_tenths_c,
        snapshot->humidity_tenths_percent,
        snapshot->buttons_available,
        key_event.diagnostic,
        snapshot->key_event_count,
        boot_event.diagnostic,
        snapshot->boot_event_count,
        snapshot->rtc_error_count,
        snapshot->sensor_error_count,
        esp_get_free_heap_size(),
        esp_get_minimum_free_heap_size(),
    };
    const deck_diagnostic_sink_t sink = {write_stdout, nullptr};
    (void)deck_peripheral_diagnostics_emit(&info, sink);
    emit_companion_link_diagnostics();
#endif
}

#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
const char *setup_reason_name(deck_setup_reason_t reason)
{
    switch (reason) {
        case DECK_SETUP_REASON_NO_WIFI:
            return "no_wifi_config";
        case DECK_SETUP_REASON_BOOT_LONG_PRESS:
            return "boot_long_press";
        case DECK_SETUP_REASON_NONE:
        default:
            return "none";
    }
}
#endif

deck_wifi_config_view_state_t wifi_config_view_state(deck_wifi_config_state_t state)
{
    switch (state) {
        case DECK_WIFI_CONFIG_ACTIVE:
            return DECK_WIFI_CONFIG_VIEW_ACTIVE;
        case DECK_WIFI_CONFIG_VALIDATING:
            return DECK_WIFI_CONFIG_VIEW_VALIDATING;
        case DECK_WIFI_CONFIG_AUTH_FAILED:
            return DECK_WIFI_CONFIG_VIEW_AUTH_FAILED;
        case DECK_WIFI_CONFIG_TIMED_OUT:
            return DECK_WIFI_CONFIG_VIEW_TIMED_OUT;
        case DECK_WIFI_CONFIG_CONNECTION_FAILED:
            return DECK_WIFI_CONFIG_VIEW_CONNECTION_FAILED;
        case DECK_WIFI_CONFIG_STORAGE_ERROR:
            return DECK_WIFI_CONFIG_VIEW_STORAGE_ERROR;
        case DECK_WIFI_CONFIG_NO_ACTIVE:
        default:
            return DECK_WIFI_CONFIG_VIEW_NO_ACTIVE;
    }
}

deck_wifi_record_view_status_t wifi_record_view_status(deck_wifi_record_status_t status)
{
    switch (status) {
        case DECK_WIFI_RECORD_VALID:
            return DECK_WIFI_RECORD_VIEW_VALID;
        case DECK_WIFI_RECORD_RECOVERED_PREVIOUS:
            return DECK_WIFI_RECORD_VIEW_RECOVERED_PREVIOUS;
        case DECK_WIFI_RECORD_CORRUPT:
            return DECK_WIFI_RECORD_VIEW_CORRUPT;
        case DECK_WIFI_RECORD_UNSUPPORTED_SCHEMA:
            return DECK_WIFI_RECORD_VIEW_UNSUPPORTED_SCHEMA;
        case DECK_WIFI_RECORD_MIGRATION_FAILED:
            return DECK_WIFI_RECORD_VIEW_MIGRATION_FAILED;
        case DECK_WIFI_RECORD_IO_ERROR:
            return DECK_WIFI_RECORD_VIEW_IO_ERROR;
        case DECK_WIFI_RECORD_EMPTY:
        default:
            return DECK_WIFI_RECORD_VIEW_EMPTY;
    }
}

#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
int diagnostic_hex_value(char value)
{
    if (value >= '0' && value <= '9') {
        return value - '0';
    }
    if (value >= 'a' && value <= 'f') {
        return value - 'a' + 10;
    }
    if (value >= 'A' && value <= 'F') {
        return value - 'A' + 10;
    }
    return -1;
}

bool decode_diagnostic_hex(
    const char *encoded,
    size_t encoded_size,
    char *output,
    size_t output_capacity
)
{
    if (encoded == nullptr || output == nullptr || encoded_size % 2 != 0 ||
        encoded_size / 2 >= output_capacity) {
        return false;
    }
    for (size_t index = 0; index < encoded_size; index += 2) {
        const int high = diagnostic_hex_value(encoded[index]);
        const int low = diagnostic_hex_value(encoded[index + 1]);
        if (high < 0 || low < 0) {
            return false;
        }
        const unsigned char byte = static_cast<unsigned char>((high << 4) | low);
        if (byte < 0x20U || byte == 0x7fU) {
            return false;
        }
        output[index / 2] = static_cast<char>(byte);
    }
    output[encoded_size / 2] = '\0';
    return true;
}

void handle_diagnostic_control_line(char *line)
{
    if (line == nullptr) {
        return;
    }
    if (std::strcmp(line, "DECK_IDENTIFY") == 0) {
        static constexpr char identity[] =
            "{\"type\":\"deck_identity\",\"model\":\"s3-rlcd-deck\",\"protocol\":1}\n";
        write_stdout(nullptr, identity, sizeof(identity) - 1);
        return;
    }
    if (std::strcmp(line, "DECK_HIL_SETUP_ACCESS") == 0) {
        char ssid[DECK_M0_SETUP_SSID_CAPACITY]{};
        char password[DECK_M0_SETUP_PASSWORD_CAPACITY]{};
        char address[DECK_M0_SETUP_ADDRESS_CAPACITY]{};
        bool active = false;
        if (application_model_mutex != nullptr &&
            xSemaphoreTake(application_model_mutex, portMAX_DELAY) == pdTRUE) {
            active = application_model.setup_state == DECK_SETUP_ACTIVE;
            if (active) {
                std::memcpy(ssid, application_model.setup_ssid, sizeof(ssid));
                std::memcpy(password, application_model.setup_password, sizeof(password));
                std::memcpy(address, application_model.setup_address, sizeof(address));
            }
            xSemaphoreGive(application_model_mutex);
        }
        if (active) {
            char response[192];
            const int size = snprintf(
                response,
                sizeof(response),
                "{\"type\":\"hil_setup_access\",\"ssid\":\"%s\","
                "\"password\":\"%s\",\"address\":\"%s\"}\n",
                ssid,
                password,
                address
            );
            if (size > 0 && static_cast<size_t>(size) < sizeof(response)) {
                write_stdout(nullptr, response, static_cast<size_t>(size));
            }
        }
        std::memset(ssid, 0, sizeof(ssid));
        std::memset(password, 0, sizeof(password));
        std::memset(address, 0, sizeof(address));
        return;
    }
    if (application_setup == nullptr) {
        return;
    }
    if (std::strcmp(line, "DECK_SETUP") == 0) {
        (void)deck_setup_service_enter_from_boot(application_setup);
        return;
    }
    if (std::strcmp(line, "DECK_RESTART") == 0) {
        esp_restart();
        return;
    }
    constexpr char kWifiPrefix[] = "DECK_WIFI ";
    if (std::strncmp(line, kWifiPrefix, sizeof(kWifiPrefix) - 1) != 0) {
        return;
    }
    char *ssid = line + sizeof(kWifiPrefix) - 1;
    char *separator = std::strchr(ssid, ' ');
    if (separator == nullptr) {
        return;
    }
    *separator = '\0';
    const char *password = separator + 1;
    deck_wifi_credentials_t credentials{};
    const bool ssid_ok = decode_diagnostic_hex(
        ssid,
        std::strlen(ssid),
        credentials.ssid,
        sizeof(credentials.ssid)
    );
    const bool password_ok = std::strcmp(password, "-") == 0 ||
                             decode_diagnostic_hex(
                                 password,
                                 std::strlen(password),
                                 credentials.password,
                                 sizeof(credentials.password)
                             );
    if (ssid_ok && password_ok) {
        (void)deck_setup_service_submit_wifi(application_setup, &credentials);
    }
    deck_wifi_credentials_clear(&credentials);
}

void diagnostic_control_task(void *)
{
    char line[256];
    size_t line_size = 0;
    while (true) {
        char input[32];
        const ssize_t size = read(STDIN_FILENO, input, sizeof(input));
        if (size < 0 && errno != EAGAIN && errno != EWOULDBLOCK) {
            vTaskDelay(pdMS_TO_TICKS(20));
            continue;
        }
        for (ssize_t index = 0; index < size; ++index) {
            if (input[index] == '\n') {
                line[line_size] = '\0';
                handle_diagnostic_control_line(line);
                line_size = 0;
            } else if (input[index] != '\r' && line_size + 1 < sizeof(line)) {
                line[line_size++] = input[index];
            } else if (line_size + 1 >= sizeof(line)) {
                line_size = 0;
            }
        }
        vTaskDelay(pdMS_TO_TICKS(20));
    }
}
#endif

void setup_event(void *, const deck_setup_service_event_t *event)
{
    if (event == nullptr) {
        return;
    }
    deck_peripherals_t *peripherals_to_update = nullptr;
    if (application_model_mutex != nullptr &&
        xSemaphoreTake(application_model_mutex, portMAX_DELAY) == pdTRUE) {
        application_temperature_offset_tenths_c =
            event->settings.temperature_offset_tenths_c;
        peripherals_to_update = application_peripherals;
        application_model.setup_state = event->setup.active
                                            ? DECK_SETUP_ACTIVE
                                            : event->state == DECK_SETUP_SERVICE_ERROR
                                                  ? DECK_SETUP_UNAVAILABLE
                                                  : DECK_SETUP_IDLE;
        application_model.wifi_state = event->state == DECK_SETUP_SERVICE_ERROR ||
                                               event->wifi.state == DECK_WIFI_CONFIG_STORAGE_ERROR
                                           ? DECK_WIFI_UNAVAILABLE
                                           : event->wifi.state == DECK_WIFI_CONFIG_ACTIVE
                                                 ? DECK_WIFI_CONNECTED
                                                 : DECK_WIFI_DISCONNECTED;
        application_model.wifi_config_state = wifi_config_view_state(event->wifi.state);
        application_model.wifi_record_status = wifi_record_view_status(
            event->wifi.record_status
        );
        application_model.wifi_candidate_record_status = wifi_record_view_status(
            event->wifi.candidate_record_status
        );
        application_model.wifi_config_generation = event->wifi.generation;
        std::memcpy(
            application_model.setup_ssid,
            event->setup.ssid,
            sizeof(application_model.setup_ssid)
        );
        std::memcpy(
            application_model.setup_password,
            event->setup.password,
            sizeof(application_model.setup_password)
        );
        std::memcpy(
            application_model.setup_address,
            event->setup.address,
            sizeof(application_model.setup_address)
        );
        xSemaphoreGive(application_model_mutex);
    }
    if (peripherals_to_update != nullptr) {
        (void)deck_peripherals_set_temperature_offset(
            peripherals_to_update,
            event->settings.temperature_offset_tenths_c
        );
    }
    deck_m0_view_model_t published{};
    (void)publish_application_model(&published);
#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
    const deck_setup_diagnostic_info_t info = {
        event->setup.active,
        setup_reason_name(event->setup.reason),
        event->setup.session_id,
        event->setup.ssid,
        event->setup.address,
        event->error_stage,
        deck_wifi_config_state_name(event->wifi.state),
        deck_wifi_record_status_name(event->wifi.record_status),
        deck_wifi_record_status_name(event->wifi.candidate_record_status),
        event->wifi.has_active,
        event->wifi.has_candidate,
        event->wifi.generation,
        deck_device_settings_state_name(event->settings.state),
        deck_device_settings_record_status_name(event->settings.record_status),
        deck_device_settings_record_status_name(
            event->settings.candidate_record_status
        ),
        event->settings.has_active,
        event->settings.has_candidate,
        event->settings.generation,
        event->settings.temperature_offset_tenths_c,
    };
    const deck_diagnostic_sink_t sink = {write_stdout, nullptr};
    (void)deck_setup_diagnostics_emit(&info, sink);
#endif
}

void start_setup_after_ui_ready()
{
    if (application_setup != nullptr) {
        return;
    }
    application_setup = deck_setup_service_start(setup_event, nullptr);
    if (application_setup != nullptr) {
        const esp_app_desc_t *app = esp_app_get_description();
        application_companion_link = deck_companion_link_start(
            deck_setup_service_wait_companion_profiles(application_setup, 10'000),
            app->version
        );
    }
#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
    if (application_setup == nullptr) {
        static constexpr char error[] =
            "{\"type\":\"setup_state\",\"active\":false,\"reason\":\"none\","
            "\"session_id\":0,\"ssid\":\"\",\"address\":\"192.168.4.1\","
            "\"error_stage\":\"start\",\"wifi_config_state\":\"no_active\","
            "\"wifi_record_status\":\"empty\","
            "\"wifi_candidate_record_status\":\"empty\","
            "\"wifi_has_active\":false,\"wifi_has_candidate\":false,"
            "\"wifi_generation\":0,\"device_settings_state\":\"default\","
            "\"device_settings_record_status\":\"empty\","
            "\"device_settings_candidate_record_status\":\"empty\","
            "\"device_settings_has_active\":false,"
            "\"device_settings_has_candidate\":false,"
            "\"device_settings_generation\":0,"
            "\"temperature_offset_tenths_c\":-40}\n";
        write_stdout(nullptr, error, sizeof(error) - 1);
    } else {
        if (application_companion_link == nullptr) {
            static constexpr char link_error[] =
                "{\"type\":\"diagnostic_error\",\"stage\":\"companion_link\"}\n";
            write_stdout(nullptr, link_error, sizeof(link_error) - 1);
        }
        if (xTaskCreatePinnedToCore(
                diagnostic_control_task,
                "diagnostic_control",
                4'096,
                nullptr,
                1,
                nullptr,
                0
            ) != pdPASS) {
            static constexpr char control_error[] =
                "{\"type\":\"diagnostic_error\",\"stage\":\"control\"}\n";
            write_stdout(nullptr, control_error, sizeof(control_error) - 1);
        }
    }
#endif
}

void ui_event(void *, const deck_application_ui_event_t *event)
{
    if (event == nullptr) {
        return;
    }
    if (event->state == DECK_APPLICATION_UI_FAILED) {
#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
        static constexpr char error[] = "{\"type\":\"display_error\",\"stage\":\"ui\"}\n";
        write_stdout(nullptr, error, sizeof(error) - 1);
#endif
        (void)release_display_resources();
        return;
    }
    if (event->state == DECK_APPLICATION_UI_READY) {
        start_setup_after_ui_ready();
    }

#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
    const deck_display_ready_info_t info = {
        DECK_DISPLAY_WIDTH,
        DECK_DISPLAY_HEIGHT,
        DECK_DISPLAY_FRAME_BYTES,
        event->display.submitted_frames,
        event->display.completed_frames,
        event->display.transfer_timeouts,
        event->display.start_failures,
        event->display.rejected_updates,
    };
    const deck_diagnostic_sink_t sink = {write_stdout, nullptr};
    if (event->state == DECK_APPLICATION_UI_READY) {
        deck_display_diagnostics_emit(&info, sink);
    } else {
        deck_display_progress_diagnostics_emit(&info, sink);
    }
#endif
}

}  // namespace

extern "C" void app_main(void)
{
    const esp_app_desc_t *app = esp_app_get_description();
#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
    const bool diagnostic_console_ready = initialize_diagnostic_console_driver();
    wait_for_diagnostic_host_ready();
    const deck_boot_info_t info = {
        app->version,
        reset_reason_name(esp_reset_reason()),
        static_cast<uint64_t>(esp_timer_get_time() / 1000),
        esp_get_minimum_free_heap_size(),
    };
    const deck_diagnostic_sink_t sink = {write_stdout, nullptr};
    deck_boot_diagnostics_emit(&info, sink);
    if (!diagnostic_console_ready) {
        static constexpr char error[] =
            "{\"type\":\"diagnostic_error\",\"stage\":\"console_driver\"}\n";
        write_stdout(nullptr, error, sizeof(error) - 1);
    }
#endif

    application_panel = deck_rlcd_panel_create();
    if (application_panel == nullptr) {
#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
        display_start_error("panel_create");
#endif
        return;
    }
    if (!deck_rlcd_panel_initialize(application_panel)) {
#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
        display_start_error("panel_initialize");
#endif
        (void)release_display_resources();
        return;
    }

    application_display =
        deck_display_service_create(deck_rlcd_panel_adapter(application_panel), 100);
    if (application_display == nullptr) {
#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
        display_start_error("service_create");
#endif
        (void)release_display_resources();
        return;
    }

    application_model = make_initial_model(
        app->version,
        static_cast<uint64_t>(esp_timer_get_time() / 1'000'000),
        esp_get_minimum_free_heap_size()
    );
    application_model_mutex = xSemaphoreCreateMutex();
    if (application_model_mutex == nullptr) {
        (void)release_display_resources();
        return;
    }
    if (!deck_application_ui_start(
            application_display,
            &application_model,
            ui_event,
            nullptr
        )) {
#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
        display_start_error("ui_start");
#endif
        (void)release_display_resources();
        vSemaphoreDelete(application_model_mutex);
        application_model_mutex = nullptr;
        return;
    }
    if (xSemaphoreTake(application_model_mutex, portMAX_DELAY) == pdTRUE) {
        application_peripherals = deck_peripherals_start(
            application_temperature_offset_tenths_c,
            peripheral_snapshot,
            nullptr
        );
        xSemaphoreGive(application_model_mutex);
    }
#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
    if (application_peripherals == nullptr) {
        static constexpr char error[] = "{\"type\":\"peripheral_error\",\"stage\":\"start\"}\n";
        write_stdout(nullptr, error, sizeof(error) - 1);
    }
#endif
}
