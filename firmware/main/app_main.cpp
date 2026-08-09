#include "sdkconfig.h"

#include "deck_application_ui.h"
#include "deck_peripherals.h"
#include "deck_boot_diagnostics.h"
#include "deck_display.h"
#include "deck_m0_view_model.h"
#include "deck_rlcd_panel.h"

#include "esp_app_desc.h"
#include "esp_system.h"
#include "esp_timer.h"

namespace {

deck_rlcd_panel_t *application_panel = nullptr;
deck_display_service_t *application_display = nullptr;
deck_peripherals_t *application_peripherals = nullptr;
deck_m0_view_model_t application_model{};

}  // namespace

#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE

#include <fcntl.h>
#include <stdio.h>
#include <unistd.h>

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

namespace {

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
        DECK_SETUP_IDLE,
        0,
        uptime_seconds,
        minimum_free_heap_bytes,
    };
    return model;
}

void peripheral_snapshot(void *, const deck_peripheral_snapshot_t *snapshot)
{
    if (snapshot == nullptr) {
        return;
    }
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
    const ButtonEventMapping key_event = map_button_event(snapshot->key_event);
    const ButtonEventMapping boot_event = map_button_event(snapshot->boot_event);
    application_model.key_event = key_event.view;
    application_model.key_event_count = snapshot->key_event_count;
    application_model.boot_event = boot_event.view;
    application_model.boot_event_count = snapshot->boot_event_count;
    (void)deck_application_ui_update(&application_model);
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
    };
    const deck_diagnostic_sink_t sink = {write_stdout, nullptr};
    (void)deck_peripheral_diagnostics_emit(&info, sink);
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
    wait_for_diagnostic_host_ready();
    const deck_boot_info_t info = {
        app->version,
        reset_reason_name(esp_reset_reason()),
        static_cast<uint64_t>(esp_timer_get_time() / 1000),
        esp_get_minimum_free_heap_size(),
    };
    const deck_diagnostic_sink_t sink = {write_stdout, nullptr};
    deck_boot_diagnostics_emit(&info, sink);
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
        return;
    }
    application_peripherals = deck_peripherals_start(peripheral_snapshot, nullptr);
#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
    if (application_peripherals == nullptr) {
        static constexpr char error[] = "{\"type\":\"peripheral_error\",\"stage\":\"start\"}\n";
        write_stdout(nullptr, error, sizeof(error) - 1);
    }
#endif
}
