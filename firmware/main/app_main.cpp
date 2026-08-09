#include "sdkconfig.h"

#include "deck_application_ui.h"
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
    deck_display_diagnostics_emit(&info, sink);
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

    const deck_m0_view_model_t initial_model = {
        app->version,
        DECK_DATA_SIMULATED,
        true,
        12,
        34,
        234,
        194,
        456,
        0,
        DECK_BUTTON_SHORT_PRESS,
        3,
        DECK_BUTTON_LONG_PRESS,
        1,
        DECK_WIFI_UNAVAILABLE,
        DECK_SETUP_IDLE,
        0,
        static_cast<uint64_t>(esp_timer_get_time() / 1'000'000),
        esp_get_minimum_free_heap_size(),
    };
    if (!deck_application_ui_start(
            application_display,
            &initial_model,
            ui_event,
            nullptr
        )) {
#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
        display_start_error("ui_start");
#endif
        (void)release_display_resources();
    }
}
