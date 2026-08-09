#include "sdkconfig.h"

#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE

#include "deck_boot_diagnostics.h"

#include <stdio.h>

#include "driver/usb_serial_jtag.h"
#include "esp_app_desc.h"
#include "esp_system.h"
#include "esp_timer.h"
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

void wait_for_diagnostic_host()
{
    constexpr int64_t timeout_us = 10'000'000;
    const int64_t deadline = esp_timer_get_time() + timeout_us;
    while (!usb_serial_jtag_is_connected() && esp_timer_get_time() < deadline) {
        vTaskDelay(pdMS_TO_TICKS(10));
    }
}

}  // namespace

#endif

extern "C" void app_main(void)
{
#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
    wait_for_diagnostic_host();
    const esp_app_desc_t *app = esp_app_get_description();
    const deck_boot_info_t info = {
        app->version,
        reset_reason_name(esp_reset_reason()),
        static_cast<uint64_t>(esp_timer_get_time() / 1000),
        esp_get_minimum_free_heap_size(),
    };
    const deck_diagnostic_sink_t sink = {write_stdout, nullptr};
    deck_boot_diagnostics_emit(&info, sink);
#endif
}
