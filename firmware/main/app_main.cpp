#include "sdkconfig.h"

#include <atomic>
#include <cstring>
#include <inttypes.h>
#include <new>

#include "deck_application_ui.h"
#include "deck_peripherals.h"
#include "deck_boot_diagnostics.h"
#include "deck_diagnostic_ring.h"
#include "deck_companion_link.h"
#include "deck_device_settings.h"
#include "deck_display.h"
#include "deck_lan_pairing.h"
#include "deck_m0_view_model.h"
#include "deck_ota_service.h"
#include "deck_rlcd_panel.h"
#include "deck_setup_service.h"
#include "deck_serial_service.h"

#include "esp_app_desc.h"
#include "esp_heap_caps.h"
#include "esp_system.h"
#include "esp_timer.h"
#include "esp_wifi.h"
#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"
#include "freertos/queue.h"
#include "freertos/semphr.h"
#include "freertos/task.h"

namespace {

static_assert(DECK_M0_SETUP_SSID_CAPACITY == DECK_SETUP_SSID_CAPACITY);
static_assert(DECK_M0_SETUP_PASSWORD_CAPACITY == DECK_SETUP_PASSWORD_CAPACITY);
static_assert(DECK_M0_SETUP_ADDRESS_CAPACITY == DECK_SETUP_ADDRESS_CAPACITY);

deck_rlcd_panel_t *application_panel = nullptr;
deck_display_service_t *application_display = nullptr;
deck_peripherals_t *application_peripherals = nullptr;
deck_setup_service_t *application_setup = nullptr;
deck_lan_pairing_t *application_lan_pairing = nullptr;
deck_serial_service_t *application_serial = nullptr;
deck_companion_link_t *application_companion_link = nullptr;
struct AiPageTaskContext {
    deck_companion_link_t *link;
    char *document;
    deck_ai_snapshot_codex_projection_t *codex_projection;
    deck_ai_snapshot_pages_projection_t *pages_projection;
    EventGroupHandle_t lifecycle;
    TaskHandle_t task;
    std::atomic<bool> stop_requested;
};
struct SerialViewTaskContext {
    QueueHandle_t events;
    EventGroupHandle_t lifecycle;
    TaskHandle_t task;
    std::atomic<bool> stop_requested;
};
AiPageTaskContext *application_ai_page_task = nullptr;
SerialViewTaskContext *application_serial_view_task = nullptr;
deck_m0_view_model_t application_model{};
SemaphoreHandle_t application_model_mutex = nullptr;
EventGroupHandle_t application_ui_lifecycle = nullptr;
uint32_t handled_boot_long_press_count = 0;
uint32_t handled_boot_short_press_count = 0;
uint32_t handled_key_short_press_count = 0;
uint32_t handled_key_long_press_count = 0;
std::atomic<bool> application_serial_requested{false};
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
#include "esp_private/periph_ctrl.h"
#include "esp_rom_sys.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "hal/usb_serial_jtag_ll.h"

namespace {

bool initialize_diagnostic_console_driver()
{
    // OpenOCD's flash stub and the application share the USB Serial/JTAG
    // peripheral. An ESP32-S3 software reset can leave the host holding a
    // stale CDC endpoint because D+ never indicated a disconnect. Perform a
    // USB-spec detach before resetting the controller so the host enumerates
    // a clean diagnostic endpoint without requiring a power cycle.
    const usb_serial_jtag_pull_override_vals_t detached{};
    usb_serial_jtag_ll_phy_enable_pull_override(&detached);
    esp_rom_delay_us(50'000);
    PERIPH_RCC_ATOMIC() {
        usb_serial_jtag_ll_reset_register();
    }
    usb_serial_jtag_ll_phy_disable_pull_override();

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

constexpr EventBits_t kAiPageTaskStoppedBit = BIT0;
constexpr EventBits_t kSerialViewTaskReadyBit = BIT0;
constexpr EventBits_t kSerialViewTaskStoppedBit = BIT1;
constexpr EventBits_t kApplicationUiReadyBit = BIT0;
constexpr EventBits_t kApplicationUiFailedBit = BIT1;
constexpr TickType_t kSerialViewPollTicks = pdMS_TO_TICKS(100);
constexpr TickType_t kSerialViewLifecycleTicks = pdMS_TO_TICKS(2'000);

bool stop_ai_page_task();
bool stop_serial_view_task();

bool release_companion_resources()
{
    if (application_lan_pairing != nullptr) {
        if (!deck_lan_pairing_stop(application_lan_pairing)) {
            return false;
        }
        application_lan_pairing = nullptr;
    }
    if (!stop_ai_page_task()) {
        return false;
    }
    if (application_companion_link != nullptr) {
        if (!deck_companion_link_stop(application_companion_link)) {
            return false;
        }
        application_companion_link = nullptr;
    }
    return true;
}

bool release_serial_resources()
{
    if (application_serial != nullptr) {
        if (application_companion_link != nullptr &&
            !deck_companion_link_detach_serial(
                application_companion_link,
                application_serial
            )) {
            return false;
        }
        if (!deck_serial_service_stop(application_serial)) {
            return false;
        }
        application_serial = nullptr;
    }
    return stop_serial_view_task();
}

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

const char *companion_link_error_name(deck_companion_link_error_t error)
{
    switch (error) {
        case DECK_COMPANION_LINK_ERROR_TRANSPORT:
            return "transport";
        case DECK_COMPANION_LINK_ERROR_TLS_PIN_MISMATCH:
            return "tls_pin_mismatch";
        case DECK_COMPANION_LINK_ERROR_AUTH_REJECTED:
            return "auth_rejected";
        case DECK_COMPANION_LINK_ERROR_PROTOCOL_MAJOR_REJECTED:
            return "protocol_major_rejected";
        case DECK_COMPANION_LINK_ERROR_PROTOCOL_INVALID:
            return "protocol_invalid";
        case DECK_COMPANION_LINK_ERROR_HEARTBEAT_TIMEOUT:
            return "heartbeat_timeout";
        case DECK_COMPANION_LINK_ERROR_INTERNAL:
            return "internal";
        case DECK_COMPANION_LINK_ERROR_NONE:
        default:
            return "none";
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
        companion_link_error_name(snapshot.last_error),
        snapshot.error_generation,
        snapshot.last_heartbeat_monotonic_ms,
    };
    const deck_diagnostic_sink_t sink = {write_stdout, nullptr};
    (void)deck_companion_link_diagnostics_emit(&info, sink);
}
#endif

bool release_display_resources()
{
    if (!release_serial_resources()) {
        return false;
    }
    if (!release_companion_resources()) {
        return false;
    }
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

void initialize_application_model(
    const char *firmware_version,
    uint64_t uptime_seconds,
    uint32_t minimum_free_heap_bytes
)
{
    std::memset(&application_model, 0, sizeof(application_model));
    application_model.firmware_version = firmware_version;
    application_model.data_source = DECK_DATA_UNAVAILABLE;
    application_model.wifi_state = DECK_WIFI_UNAVAILABLE;
    application_model.wifi_config_state = DECK_WIFI_CONFIG_VIEW_NO_ACTIVE;
    application_model.wifi_record_status = DECK_WIFI_RECORD_VIEW_EMPTY;
    application_model.wifi_candidate_record_status = DECK_WIFI_RECORD_VIEW_EMPTY;
    application_model.setup_state = DECK_SETUP_UNAVAILABLE;
    application_model.uptime_seconds = uptime_seconds;
    application_model.minimum_free_heap_bytes = minimum_free_heap_bytes;
}

bool publish_application_model()
{
    if (application_model_mutex == nullptr ||
        xSemaphoreTake(application_model_mutex, portMAX_DELAY) != pdTRUE) {
        return false;
    }
    const bool published = deck_application_ui_update(&application_model);
    xSemaphoreGive(application_model_mutex);
    return published;
}

deck_serial_view_state_t serial_view_state(deck_serial_state_t state)
{
    switch (state) {
        case DECK_SERIAL_USB_TX:
            return DECK_SERIAL_VIEW_USB_TX;
        case DECK_SERIAL_WEB_TX:
            return DECK_SERIAL_VIEW_WEB_TX;
        case DECK_SERIAL_DISARMED:
        default:
            return DECK_SERIAL_VIEW_DISARMED;
    }
}

void apply_serial_event(const deck_serial_service_event_t &event)
{
    application_serial_requested.store(
        event.snapshot.state != DECK_SERIAL_DISARMED,
        std::memory_order_release
    );
    if (application_model_mutex != nullptr &&
        xSemaphoreTake(application_model_mutex, portMAX_DELAY) == pdTRUE) {
        application_model.serial = {
            serial_view_state(event.snapshot.state),
            event.snapshot.session_id,
            event.snapshot.owner_generation,
            event.snapshot.usb_tx_rejected,
            event.snapshot.uart_install_failures,
            event.has_router_stats ? event.router_stats.uart_fifo_overflows : 0,
            event.has_router_stats ? event.router_stats.uart_driver_buffer_full : 0,
            event.snapshot.uart_install_failed,
            event.snapshot.uart_installed,
        };
        (void)deck_application_ui_update(&application_model);
        xSemaphoreGive(application_model_mutex);
    }
}

void serial_view_task(void *context)
{
    auto *task = static_cast<SerialViewTaskContext *>(context);
    xEventGroupSetBits(task->lifecycle, kSerialViewTaskReadyBit);
    while (!task->stop_requested.load(std::memory_order_acquire)) {
        deck_serial_service_event_t event{};
        if (xQueueReceive(task->events, &event, kSerialViewPollTicks) == pdTRUE) {
            deck_serial_service_event_t newer{};
            while (xQueueReceive(task->events, &newer, 0) == pdTRUE) {
                event = newer;
            }
            apply_serial_event(event);
        }
    }
    xEventGroupSetBits(task->lifecycle, kSerialViewTaskStoppedBit);
    vTaskSuspend(nullptr);
}

SerialViewTaskContext *start_serial_view_task()
{
    auto *context = new (std::nothrow) SerialViewTaskContext{};
    if (context == nullptr) {
        return nullptr;
    }
    context->events = xQueueCreate(1, sizeof(deck_serial_service_event_t));
    context->lifecycle = xEventGroupCreate();
    context->stop_requested.store(false, std::memory_order_release);
    if (context->events == nullptr || context->lifecycle == nullptr ||
        xTaskCreatePinnedToCore(
            serial_view_task,
            "serial_view",
            4'096,
            context,
            2,
            &context->task,
            0
        ) != pdPASS) {
        if (context->events != nullptr) {
            vQueueDelete(context->events);
        }
        if (context->lifecycle != nullptr) {
            vEventGroupDelete(context->lifecycle);
        }
        delete context;
        return nullptr;
    }
    const EventBits_t ready = xEventGroupWaitBits(
        context->lifecycle,
        kSerialViewTaskReadyBit,
        pdFALSE,
        pdTRUE,
        kSerialViewLifecycleTicks
    );
    if ((ready & kSerialViewTaskReadyBit) == 0) {
        vTaskDelete(context->task);
        vQueueDelete(context->events);
        vEventGroupDelete(context->lifecycle);
        delete context;
        return nullptr;
    }
    return context;
}

bool stop_serial_view_task()
{
    if (application_serial_view_task == nullptr) {
        return true;
    }
    SerialViewTaskContext *context = application_serial_view_task;
    context->stop_requested.store(true, std::memory_order_release);
    const EventBits_t stopped = xEventGroupWaitBits(
        context->lifecycle,
        kSerialViewTaskStoppedBit,
        pdFALSE,
        pdTRUE,
        kSerialViewLifecycleTicks
    );
    if ((stopped & kSerialViewTaskStoppedBit) == 0) {
        return false;
    }
    vTaskDelete(context->task);
    vQueueDelete(context->events);
    vEventGroupDelete(context->lifecycle);
    delete context;
    application_serial_view_task = nullptr;
    return true;
}

void serial_event(void *context, const deck_serial_service_event_t *event)
{
    auto *view_task = static_cast<SerialViewTaskContext *>(context);
    if (view_task != nullptr && event != nullptr) {
        (void)xQueueOverwrite(view_task->events, event);
    }
}

void peripheral_snapshot(void *, const deck_peripheral_snapshot_t *snapshot)
{
    if (snapshot == nullptr) {
        return;
    }
    const ButtonEventMapping key_event = map_button_event(snapshot->key_event);
    const ButtonEventMapping boot_event = map_button_event(snapshot->boot_event);
    deck_serial_session_snapshot_t serial_snapshot{};
    const bool serial_active =
        application_serial_requested.load(std::memory_order_acquire) ||
        (application_serial != nullptr &&
         deck_serial_service_snapshot(application_serial, &serial_snapshot) &&
         serial_snapshot.state != DECK_SERIAL_DISARMED);
    bool enter_setup = false;
    bool enter_serial = false;
    bool exit_serial = false;
    bool open_pairing = false;
    bool cancel_pairing = false;
    uint32_t pending_boot_long_press_count = 0;
    uint32_t pending_boot_short_press_count = 0;
    uint32_t pending_key_long_press_count = 0;
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
        application_model.ai_page.rtc_available = snapshot->rtc_available;
        application_model.ai_page.rtc_hour = snapshot->rtc_hour;
        application_model.ai_page.rtc_minute = snapshot->rtc_minute;
        application_model.ai_page.temperature_available = snapshot->sensor_available;
        application_model.ai_page.calibrated_temperature_tenths_c =
            snapshot->calibrated_temperature_tenths_c;
        application_model.buttons_available = snapshot->buttons_available;
        application_model.key_event = key_event.view;
        application_model.key_event_count = snapshot->key_event_count;
        application_model.boot_event = boot_event.view;
        application_model.boot_event_count = snapshot->boot_event_count;
        if (snapshot->key_event == DECK_BUTTON_INPUT_SHORT_PRESS &&
            snapshot->key_event_count > handled_key_short_press_count) {
            handled_key_short_press_count = snapshot->key_event_count;
            if (!serial_active && application_model.ai_page.active &&
                application_model.setup_state != DECK_SETUP_ACTIVE) {
                deck_ai_page_view_model_next(&application_model.ai_page);
            }
        }
        if (snapshot->key_event == DECK_BUTTON_INPUT_LONG_PRESS &&
            snapshot->key_event_count > handled_key_long_press_count) {
            pending_key_long_press_count = snapshot->key_event_count;
            if (application_serial != nullptr && !serial_active &&
                application_model.setup_state != DECK_SETUP_ACTIVE) {
                enter_serial = true;
            } else {
                handled_key_long_press_count = pending_key_long_press_count;
            }
        }
        if (snapshot->boot_event == DECK_BUTTON_INPUT_SHORT_PRESS &&
            snapshot->boot_event_count > handled_boot_short_press_count) {
            pending_boot_short_press_count = snapshot->boot_event_count;
            if (application_serial != nullptr && serial_active) {
                exit_serial = true;
            } else if (application_lan_pairing != nullptr &&
                       application_model.wifi_state == DECK_WIFI_CONNECTED &&
                       application_model.setup_state != DECK_SETUP_ACTIVE) {
                const deck_pairing_v2_state_t pairing_state =
                    application_model.pairing_v2.state;
                cancel_pairing = pairing_state == DECK_PAIRING_V2_ACTIVE ||
                                 pairing_state == DECK_PAIRING_V2_AUTHENTICATING ||
                                 pairing_state == DECK_PAIRING_V2_PROOF_VERIFIED;
                open_pairing = !cancel_pairing;
            } else {
                handled_boot_short_press_count = pending_boot_short_press_count;
            }
        }
        if (snapshot->boot_event == DECK_BUTTON_INPUT_LONG_PRESS &&
            snapshot->boot_event_count > handled_boot_long_press_count) {
            pending_boot_long_press_count = snapshot->boot_event_count;
            if (!serial_active) {
                enter_setup = true;
            } else {
                handled_boot_long_press_count = pending_boot_long_press_count;
            }
        }
        xSemaphoreGive(application_model_mutex);
    }
    (void)publish_application_model();
    if (enter_serial && application_serial != nullptr &&
        deck_serial_service_enter(application_serial)) {
        application_serial_requested.store(true, std::memory_order_release);
        handled_key_long_press_count = pending_key_long_press_count;
    }
    if (exit_serial && application_serial != nullptr &&
        deck_serial_service_exit(application_serial)) {
        application_serial_requested.store(false, std::memory_order_release);
        handled_boot_short_press_count = pending_boot_short_press_count;
    }
    if (open_pairing && application_lan_pairing != nullptr &&
        deck_lan_pairing_open(application_lan_pairing)) {
        handled_boot_short_press_count = pending_boot_short_press_count;
    }
    if (cancel_pairing && application_lan_pairing != nullptr &&
        deck_lan_pairing_cancel(application_lan_pairing)) {
        handled_boot_short_press_count = pending_boot_short_press_count;
    }
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

deck_ai_page_companion_state_t ai_page_companion_state(deck_companion_link_state_t state)
{
    switch (state) {
        case DECK_COMPANION_LINK_ONLINE:
            return DECK_AI_PAGE_COMPANION_ONLINE;
        case DECK_COMPANION_LINK_CONNECTING:
            return DECK_AI_PAGE_COMPANION_CONNECTING;
        case DECK_COMPANION_LINK_OFFLINE:
            return DECK_AI_PAGE_COMPANION_OFFLINE;
        case DECK_COMPANION_LINK_UNPAIRED:
        default:
            return DECK_AI_PAGE_COMPANION_UNPAIRED;
    }
}

deck_ai_page_snapshot_state_t ai_page_snapshot_state(deck_ai_snapshot_store_state_t state)
{
    switch (state) {
        case DECK_AI_SNAPSHOT_STORE_FRESH:
            return DECK_AI_PAGE_SNAPSHOT_FRESH;
        case DECK_AI_SNAPSHOT_STORE_STALE:
            return DECK_AI_PAGE_SNAPSHOT_STALE;
        case DECK_AI_SNAPSHOT_STORE_UNAVAILABLE:
            return DECK_AI_PAGE_SNAPSHOT_UNAVAILABLE;
        case DECK_AI_SNAPSHOT_STORE_EMPTY:
        default:
            return DECK_AI_PAGE_SNAPSHOT_EMPTY;
    }
}

void ai_page_task(void *task_context)
{
    auto *context = static_cast<AiPageTaskContext *>(task_context);
    if (context == nullptr || context->link == nullptr || context->document == nullptr ||
        context->lifecycle == nullptr) {
        vTaskDelete(nullptr);
        return;
    }

    uint64_t projected_generated_at = 0;
    size_t projected_size = 0;
    while (!context->stop_requested.load(std::memory_order_acquire)) {
        deck_companion_link_snapshot_t link_snapshot{};
        if (!deck_companion_link_snapshot(context->link, &link_snapshot)) {
            (void)ulTaskNotifyTake(pdTRUE, pdMS_TO_TICKS(1'000));
            continue;
        }

        deck_ai_snapshot_store_snapshot_t stored{};
        size_t document_size = 0;
        bool copied = false;
        if (link_snapshot.has_trusted_utc) {
            copied = deck_companion_link_copy_ai_snapshot(
                context->link,
                link_snapshot.trusted_utc_ms,
                context->document,
                DECK_AI_SNAPSHOT_MAX_BYTES,
                &document_size,
                &stored
            );
        }

        bool projection_changed = false;
        bool projection_valid = true;
        wifi_ap_record_t access_point{};
        const bool wifi_signal_available = esp_wifi_sta_get_ap_info(&access_point) == ESP_OK;
        if (copied && stored.document_visible && document_size != 0U &&
            (stored.metadata.generated_at_unix_ms != projected_generated_at ||
             document_size != projected_size)) {
            std::memset(
                context->codex_projection,
                0,
                sizeof(*context->codex_projection)
            );
            std::memset(
                context->pages_projection,
                0,
                sizeof(*context->pages_projection)
            );
            projection_valid = deck_ai_snapshot_project_codex(
                context->document,
                document_size,
                context->codex_projection
            ) && deck_ai_snapshot_project_pages(
                context->document,
                document_size,
                context->pages_projection
            );
            projection_changed = projection_valid;
        }

        if (application_model_mutex != nullptr &&
            xSemaphoreTake(application_model_mutex, portMAX_DELAY) == pdTRUE) {
            application_model.ai_page.companion_state =
                ai_page_companion_state(link_snapshot.state);
            application_model.ai_page.wifi_signal_bars =
                wifi_signal_available ? deck_ai_page_wifi_signal_bars(access_point.rssi) : 0;
            if (link_snapshot.has_trusted_utc) {
                application_model.ai_page.trusted_utc_ms = link_snapshot.trusted_utc_ms;
            } else {
                application_model.ai_page.trusted_utc_ms = 0;
                if (application_model.ai_page.active) {
                    application_model.ai_page.snapshot_state =
                        DECK_AI_PAGE_SNAPSHOT_UNAVAILABLE;
                }
            }
            if (copied) {
                if (stored.has_snapshot) {
                    application_model.ai_page.active = true;
                }
                application_model.ai_page.snapshot_state =
                    projection_valid ? ai_page_snapshot_state(stored.state)
                                     : DECK_AI_PAGE_SNAPSHOT_UNAVAILABLE;
                if (projection_changed) {
                    application_model.ai_page.codex = *context->codex_projection;
                    projection_valid = deck_ai_page_view_model_apply_pages(
                        &application_model.ai_page,
                        context->pages_projection
                    );
                    projected_generated_at = stored.metadata.generated_at_unix_ms;
                    projected_size = document_size;
                }
            }
            xSemaphoreGive(application_model_mutex);
        }
        if (document_size != 0U) {
            std::memset(context->document, 0, document_size);
        }
        (void)publish_application_model();
        (void)ulTaskNotifyTake(pdTRUE, pdMS_TO_TICKS(1'000));
    }
    xEventGroupSetBits(context->lifecycle, kAiPageTaskStoppedBit);
    // The owner deletes this suspended task after observing the stopped bit.
    // Keeping the handle alive makes a timed-out stop safely retryable.
    vTaskSuspend(nullptr);
}

AiPageTaskContext *start_ai_page_task(deck_companion_link_t *link)
{
    if (link == nullptr) {
        return nullptr;
    }
    auto *context = new (std::nothrow) AiPageTaskContext{};
    if (context == nullptr) {
        return nullptr;
    }
    context->link = link;
    context->document = static_cast<char *>(heap_caps_malloc(
        DECK_AI_SNAPSHOT_MAX_BYTES,
        MALLOC_CAP_SPIRAM | MALLOC_CAP_8BIT
    ));
    context->codex_projection = static_cast<deck_ai_snapshot_codex_projection_t *>(
        heap_caps_calloc(
            1,
            sizeof(*context->codex_projection),
            MALLOC_CAP_SPIRAM | MALLOC_CAP_8BIT
        )
    );
    context->pages_projection = static_cast<deck_ai_snapshot_pages_projection_t *>(
        heap_caps_calloc(
            1,
            sizeof(*context->pages_projection),
            MALLOC_CAP_SPIRAM | MALLOC_CAP_8BIT
        )
    );
    context->lifecycle = xEventGroupCreate();
    context->stop_requested.store(false, std::memory_order_release);
    if (context->document == nullptr || context->codex_projection == nullptr ||
        context->pages_projection == nullptr || context->lifecycle == nullptr ||
        xTaskCreatePinnedToCore(
            ai_page_task,
            "ai_page_model",
            4'096,
            context,
            2,
            &context->task,
            0
        ) != pdPASS) {
        if (context->lifecycle != nullptr) {
            vEventGroupDelete(context->lifecycle);
        }
        if (context->document != nullptr) {
            std::memset(context->document, 0, DECK_AI_SNAPSHOT_MAX_BYTES);
            heap_caps_free(context->document);
        }
        if (context->codex_projection != nullptr) {
            std::memset(
                context->codex_projection,
                0,
                sizeof(*context->codex_projection)
            );
            heap_caps_free(context->codex_projection);
        }
        if (context->pages_projection != nullptr) {
            std::memset(
                context->pages_projection,
                0,
                sizeof(*context->pages_projection)
            );
            heap_caps_free(context->pages_projection);
        }
        delete context;
        return nullptr;
    }
    return context;
}

bool stop_ai_page_task()
{
    if (application_ai_page_task == nullptr) {
        return true;
    }
    AiPageTaskContext *context = application_ai_page_task;
    const bool first_stop =
        !context->stop_requested.exchange(true, std::memory_order_acq_rel);
    if (first_stop) {
        xTaskNotifyGive(context->task);
    }
    const EventBits_t stopped = xEventGroupWaitBits(
        context->lifecycle,
        kAiPageTaskStoppedBit,
        pdFALSE,
        pdTRUE,
        pdMS_TO_TICKS(2'000)
    );
    if ((stopped & kAiPageTaskStoppedBit) == 0) {
        return false;
    }
    vTaskDelete(context->task);
    std::memset(context->document, 0, DECK_AI_SNAPSHOT_MAX_BYTES);
    heap_caps_free(context->document);
    std::memset(
        context->codex_projection,
        0,
        sizeof(*context->codex_projection)
    );
    heap_caps_free(context->codex_projection);
    std::memset(
        context->pages_projection,
        0,
        sizeof(*context->pages_projection)
    );
    heap_caps_free(context->pages_projection);
    vEventGroupDelete(context->lifecycle);
    delete context;
    application_ai_page_task = nullptr;
    return true;
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
    if (std::strcmp(line, "DECK_BUILD_IDENTITY") == 0) {
        char identity[128];
        const int size = snprintf(
            identity,
            sizeof(identity),
            "{\"type\":\"deck_build_identity\",\"firmware_commit\":\"%s\"}\n",
            DECK_FIRMWARE_COMMIT
        );
        if (size > 0 && static_cast<size_t>(size) < sizeof(identity)) {
            write_stdout(nullptr, identity, static_cast<size_t>(size));
        }
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
    if (std::strcmp(line, "DECK_PAIRING_OPEN") == 0) {
        bool allowed = false;
        if (application_model_mutex != nullptr &&
            xSemaphoreTake(application_model_mutex, portMAX_DELAY) == pdTRUE) {
            allowed = application_model.wifi_state == DECK_WIFI_CONNECTED &&
                      application_model.setup_state != DECK_SETUP_ACTIVE;
            xSemaphoreGive(application_model_mutex);
        }
        if (allowed && application_lan_pairing != nullptr) {
            (void)deck_lan_pairing_open(application_lan_pairing);
        }
        return;
    }
    if (std::strcmp(line, "DECK_PAIRING_CANCEL") == 0) {
        if (application_lan_pairing != nullptr) {
            (void)deck_lan_pairing_cancel(application_lan_pairing);
        }
        return;
    }
    if (std::strcmp(line, "DECK_RESTART") == 0) {
        static constexpr char acknowledgement[] =
            "{\"type\":\"restart_ack\"}\n";
        write_stdout(nullptr, acknowledgement, sizeof(acknowledgement) - 1);
        vTaskDelay(pdMS_TO_TICKS(100));
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
        application_model.ai_page.wifi_state =
            application_model.wifi_state == DECK_WIFI_CONNECTED
                ? DECK_AI_PAGE_WIFI_CONNECTED
                : application_model.wifi_state == DECK_WIFI_DISCONNECTED
                      ? DECK_AI_PAGE_WIFI_DISCONNECTED
                      : DECK_AI_PAGE_WIFI_UNAVAILABLE;
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
    if ((event->setup.active || event->wifi.state != DECK_WIFI_CONFIG_ACTIVE) &&
        application_lan_pairing != nullptr) {
        (void)deck_lan_pairing_cancel(application_lan_pairing);
    } else if (!event->setup.active &&
               event->wifi.state == DECK_WIFI_CONFIG_ACTIVE &&
               application_lan_pairing != nullptr) {
        (void)deck_lan_pairing_open_if_unpaired(application_lan_pairing);
    }
    (void)publish_application_model();
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

#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
const char *pairing_v2_state_name(deck_lan_pairing_state_t state)
{
    switch (state) {
        case DECK_LAN_PAIRING_ACTIVE:
            return "active";
        case DECK_LAN_PAIRING_AUTHENTICATING:
            return "authenticating";
        case DECK_LAN_PAIRING_PROOF_VERIFIED:
            return "proof_verified";
        case DECK_LAN_PAIRING_PAIRED:
            return "paired";
        case DECK_LAN_PAIRING_EXPIRED:
            return "expired";
        case DECK_LAN_PAIRING_ERROR:
            return "error";
        case DECK_LAN_PAIRING_IDLE:
        default:
            return "idle";
    }
}
#endif

void pairing_v2_event(void *, const deck_lan_pairing_event_t *event)
{
    if (event == nullptr) {
        return;
    }
    if (application_model_mutex != nullptr &&
        xSemaphoreTake(application_model_mutex, portMAX_DELAY) == pdTRUE) {
        application_model.pairing_v2.state =
            static_cast<deck_pairing_v2_state_t>(event->state);
        std::memset(
            application_model.pairing_v2.code,
            0,
            sizeof(application_model.pairing_v2.code)
        );
        if (event->state == DECK_LAN_PAIRING_ACTIVE ||
            event->state == DECK_LAN_PAIRING_AUTHENTICATING ||
            event->state == DECK_LAN_PAIRING_PROOF_VERIFIED) {
            std::memcpy(
                application_model.pairing_v2.code,
                event->code,
                sizeof(application_model.pairing_v2.code)
            );
        }
        application_model.pairing_v2.remaining_seconds = event->remaining_seconds;
        application_model.pairing_v2.proof_count = event->proof_count;
        xSemaphoreGive(application_model_mutex);
    }
    (void)publish_application_model();
#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
    char diagnostic[192];
    const int size = snprintf(
        diagnostic,
        sizeof(diagnostic),
        "{\"type\":\"pairing_v2\",\"state\":\"%s\","
        "\"remaining_seconds\":%" PRIu32 ",\"proof_count\":%" PRIu32 ","
        "\"error_stage\":\"%s\"}\n",
        pairing_v2_state_name(event->state),
        event->remaining_seconds,
        event->proof_count,
        event->error_stage == nullptr ? "" : event->error_stage
    );
    if (size > 0 && static_cast<size_t>(size) < sizeof(diagnostic)) {
        write_stdout(nullptr, diagnostic, static_cast<size_t>(size));
    }
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
        deck_companion_profiles_t *profiles =
            deck_setup_service_wait_companion_profiles(application_setup, 10'000);
        if (profiles != nullptr) {
            application_lan_pairing = deck_lan_pairing_start(
                profiles,
                app->version,
                pairing_v2_event,
                nullptr
            );
            bool pairing_network_ready = false;
            if (application_model_mutex != nullptr &&
                xSemaphoreTake(application_model_mutex, portMAX_DELAY) == pdTRUE) {
                pairing_network_ready =
                    application_model.wifi_state == DECK_WIFI_CONNECTED &&
                    application_model.setup_state != DECK_SETUP_ACTIVE;
                xSemaphoreGive(application_model_mutex);
            }
            if (pairing_network_ready && application_lan_pairing != nullptr) {
                (void)deck_lan_pairing_open_if_unpaired(application_lan_pairing);
            }
            application_companion_link = deck_companion_link_start(
                profiles,
                app->version
            );
            if (application_companion_link != nullptr) {
                application_ai_page_task = start_ai_page_task(application_companion_link);
            }
        }
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
        } else if (application_ai_page_task == nullptr) {
            static constexpr char ai_page_error[] =
                "{\"type\":\"diagnostic_error\",\"stage\":\"ai_page\"}\n";
            write_stdout(nullptr, ai_page_error, sizeof(ai_page_error) - 1);
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

void confirm_ota_boot_when_healthy(deck_ota_boot_guard_t *guard)
{
    if (guard == nullptr) {
        return;
    }
    constexpr uint64_t kHealthWaitMs = 50'000;
    const uint64_t started_ms = static_cast<uint64_t>(esp_timer_get_time() / 1'000);
    while (static_cast<uint64_t>(esp_timer_get_time() / 1'000) - started_ms <
           kHealthWaitMs) {
        deck_companion_link_snapshot_t link{};
        const bool companion_online =
            application_companion_link != nullptr &&
            deck_companion_link_snapshot(application_companion_link, &link) &&
            link.state == DECK_COMPANION_LINK_ONLINE;
        if (application_peripherals != nullptr && application_setup != nullptr &&
            companion_online) {
            (void)deck_ota_boot_guard_confirm(
                guard,
                true,
                true,
                true,
                true
            );
            return;
        }
        vTaskDelay(pdMS_TO_TICKS(250));
    }
    // The guard owns rollback at its 60 second deadline. Leaving it active is
    // deliberate: a boot that cannot re-establish Wi-Fi + Device Link is not
    // marked valid merely because its local tasks were created.
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
        if (application_ui_lifecycle != nullptr) {
            xEventGroupSetBits(application_ui_lifecycle, kApplicationUiFailedBit);
        }
        return;
    }
    if (event->state == DECK_APPLICATION_UI_READY) {
        if (application_ui_lifecycle != nullptr) {
            xEventGroupSetBits(application_ui_lifecycle, kApplicationUiReadyBit);
        }
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
    deck_diagnostic_ring_reset();
    (void)deck_diagnostic_ring_record(
        static_cast<uint64_t>(esp_timer_get_time() / 1'000),
        DECK_DIAGNOSTIC_LEVEL_INFO,
        DECK_DIAGNOSTIC_COMPONENT_SYSTEM,
        DECK_DIAGNOSTIC_CODE_BOOT,
        static_cast<uint32_t>(esp_reset_reason())
    );
    deck_ota_boot_guard_t *ota_boot_guard = deck_ota_boot_guard_start(60'000);
    if (!deck_serial_service_prepare_disarmed()) {
        (void)deck_diagnostic_ring_record(
            static_cast<uint64_t>(esp_timer_get_time() / 1'000),
            DECK_DIAGNOSTIC_LEVEL_ERROR,
            DECK_DIAGNOSTIC_COMPONENT_SERIAL,
            DECK_DIAGNOSTIC_CODE_UNAVAILABLE,
            1
        );
        return;
    }
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
        (void)deck_diagnostic_ring_record(
            static_cast<uint64_t>(esp_timer_get_time() / 1'000),
            DECK_DIAGNOSTIC_LEVEL_ERROR,
            DECK_DIAGNOSTIC_COMPONENT_DISPLAY,
            DECK_DIAGNOSTIC_CODE_UNAVAILABLE,
            1
        );
#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
        display_start_error("panel_create");
#endif
        (void)release_display_resources();
        return;
    }
    if (!deck_rlcd_panel_initialize(application_panel)) {
        (void)deck_diagnostic_ring_record(
            static_cast<uint64_t>(esp_timer_get_time() / 1'000),
            DECK_DIAGNOSTIC_LEVEL_ERROR,
            DECK_DIAGNOSTIC_COMPONENT_DISPLAY,
            DECK_DIAGNOSTIC_CODE_UNAVAILABLE,
            2
        );
#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
        display_start_error("panel_initialize");
#endif
        (void)release_display_resources();
        return;
    }

    application_display =
        deck_display_service_create(deck_rlcd_panel_adapter(application_panel), 100);
    if (application_display == nullptr) {
        (void)deck_diagnostic_ring_record(
            static_cast<uint64_t>(esp_timer_get_time() / 1'000),
            DECK_DIAGNOSTIC_LEVEL_ERROR,
            DECK_DIAGNOSTIC_COMPONENT_DISPLAY,
            DECK_DIAGNOSTIC_CODE_UNAVAILABLE,
            3
        );
#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
        display_start_error("service_create");
#endif
        (void)release_display_resources();
        return;
    }

    initialize_application_model(
        app->version,
        static_cast<uint64_t>(esp_timer_get_time() / 1'000'000),
        esp_get_minimum_free_heap_size()
    );
    application_model_mutex = xSemaphoreCreateMutex();
    application_ui_lifecycle = xEventGroupCreate();
    if (application_model_mutex == nullptr || application_ui_lifecycle == nullptr) {
        (void)release_display_resources();
        if (application_model_mutex != nullptr) {
            vSemaphoreDelete(application_model_mutex);
            application_model_mutex = nullptr;
        }
        if (application_ui_lifecycle != nullptr) {
            vEventGroupDelete(application_ui_lifecycle);
            application_ui_lifecycle = nullptr;
        }
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
        vEventGroupDelete(application_ui_lifecycle);
        application_ui_lifecycle = nullptr;
        return;
    }
    const EventBits_t ui_state = xEventGroupWaitBits(
        application_ui_lifecycle,
        kApplicationUiReadyBit | kApplicationUiFailedBit,
        pdFALSE,
        pdFALSE,
        portMAX_DELAY
    );
    if ((ui_state & kApplicationUiReadyBit) == 0) {
        vSemaphoreDelete(application_model_mutex);
        application_model_mutex = nullptr;
        return;
    }
    (void)deck_diagnostic_ring_record(
        static_cast<uint64_t>(esp_timer_get_time() / 1'000),
        DECK_DIAGNOSTIC_LEVEL_INFO,
        DECK_DIAGNOSTIC_COMPONENT_DISPLAY,
        DECK_DIAGNOSTIC_CODE_READY,
        0
    );
    start_setup_after_ui_ready();
    application_serial_view_task = start_serial_view_task();
    if (application_serial_view_task != nullptr) {
        application_serial = deck_serial_service_start(
            serial_event,
            application_serial_view_task
        );
    }
    if (application_serial != nullptr && application_companion_link != nullptr &&
        !deck_companion_link_attach_serial(
            application_companion_link,
            application_serial
        )) {
        if (deck_serial_service_stop(application_serial)) {
            application_serial = nullptr;
        }
    }
    if (application_serial == nullptr) {
        (void)stop_serial_view_task();
    }
#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
    if (application_serial == nullptr) {
        static constexpr char error[] =
            "{\"type\":\"diagnostic_error\",\"stage\":\"serial_service\"}\n";
        write_stdout(nullptr, error, sizeof(error) - 1);
    }
#endif
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
    confirm_ota_boot_when_healthy(ota_boot_guard);
    (void)deck_diagnostic_ring_record(
        static_cast<uint64_t>(esp_timer_get_time() / 1'000),
        DECK_DIAGNOSTIC_LEVEL_INFO,
        DECK_DIAGNOSTIC_COMPONENT_SYSTEM,
        DECK_DIAGNOSTIC_CODE_READY,
        0
    );
}
