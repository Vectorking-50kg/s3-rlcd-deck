#include "deck_setup_service.h"

#include "deck_setup_http.h"
#include "sdkconfig.h"

#include <algorithm>
#include <atomic>
#include <cstring>
#include <new>

#include "esp_event.h"
#include "esp_http_server.h"
#include "esp_netif.h"
#include "esp_random.h"
#include "esp_system.h"
#include "esp_timer.h"
#include "esp_wifi.h"
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/semphr.h"
#include "freertos/task.h"
#include "nvs_flash.h"

namespace {

constexpr uint8_t kSetupChannel = 1;
constexpr uint8_t kMaximumClients = 4;
constexpr size_t kMaximumScanResults = 10;
constexpr uint32_t kServiceTaskStackBytes = 6'144;
constexpr UBaseType_t kServiceTaskPriority = 2;
constexpr TickType_t kServicePollTicks = pdMS_TO_TICKS(100);

enum class Command : uint8_t {
    enter_from_boot,
};

uint64_t monotonic_ms()
{
    return static_cast<uint64_t>(esp_timer_get_time() / 1'000);
}

bool fill_random(void *, uint8_t *output, size_t size)
{
    if (output == nullptr) {
        return false;
    }
    esp_fill_random(output, size);
    return true;
}

}  // namespace

struct deck_setup_service {
    deck_setup_mode_t *mode;
    deck_setup_service_event_fn callback;
    void *callback_context;
    SemaphoreHandle_t state_mutex;
    SemaphoreHandle_t network_mutex;
    QueueHandle_t commands;
    httpd_handle_t http_server;
    deck_setup_scan_result_t scan_results[kMaximumScanResults];
    size_t scan_count;
    bool has_valid_wifi_config;
    bool network_active;
    std::atomic<bool> accepting_commands{false};
};

namespace {

bool snapshot_locked(deck_setup_service_t *service, deck_setup_snapshot_t *snapshot)
{
    return deck_setup_mode_snapshot(service->mode, snapshot);
}

bool copy_state(
    deck_setup_service_t *service,
    deck_setup_snapshot_t *snapshot,
    deck_setup_scan_result_t *networks,
    size_t network_capacity,
    size_t *network_count
)
{
    if (service == nullptr || snapshot == nullptr || network_count == nullptr ||
        xSemaphoreTake(service->state_mutex, portMAX_DELAY) != pdTRUE) {
        return false;
    }
    const bool copied = snapshot_locked(service, snapshot);
    const size_t count = std::min(service->scan_count, network_capacity);
    if (networks != nullptr && count != 0) {
        std::memcpy(networks, service->scan_results, count * sizeof(networks[0]));
    }
    *network_count = count;
    xSemaphoreGive(service->state_mutex);
    return copied;
}

void notify(
    deck_setup_service_t *service,
    deck_setup_service_state_t state,
    const char *error_stage
)
{
    deck_setup_snapshot_t snapshot{};
    size_t unused_count = 0;
    if (!copy_state(service, &snapshot, nullptr, 0, &unused_count)) {
        return;
    }
    const deck_setup_service_event_t event = {state, snapshot, error_stage};
    service->callback(service->callback_context, &event);
}

bool mark_activity(deck_setup_service_t *service)
{
    if (service == nullptr || xSemaphoreTake(service->state_mutex, portMAX_DELAY) != pdTRUE) {
        return false;
    }
    const bool active = deck_setup_mode_activity(service->mode, monotonic_ms());
    xSemaphoreGive(service->state_mutex);
    return active;
}

esp_err_t send_json(httpd_req_t *request, const char *body)
{
    httpd_resp_set_type(request, "application/json");
    httpd_resp_set_hdr(request, "Cache-Control", "no-store");
    return httpd_resp_send(request, body, HTTPD_RESP_USE_STRLEN);
}

esp_err_t page_handler(httpd_req_t *request)
{
    auto *service = static_cast<deck_setup_service_t *>(request->user_ctx);
    if (!mark_activity(service)) {
        httpd_resp_set_status(request, "503 Service Unavailable");
        return send_json(request, "{\"error\":\"setup_inactive\"}");
    }
    char page[2'048];
    if (!deck_setup_http_render_page(page, sizeof(page))) {
        return httpd_resp_send_500(request);
    }
    httpd_resp_set_type(request, "text/html; charset=utf-8");
    httpd_resp_set_hdr(request, "Cache-Control", "no-store");
    return httpd_resp_send(request, page, HTTPD_RESP_USE_STRLEN);
}

esp_err_t render_status(deck_setup_service_t *service, httpd_req_t *request)
{
    deck_setup_snapshot_t snapshot{};
    deck_setup_scan_result_t networks[kMaximumScanResults]{};
    size_t network_count = 0;
    if (!copy_state(
            service,
            &snapshot,
            networks,
            kMaximumScanResults,
            &network_count
        )) {
        return httpd_resp_send_500(request);
    }
    char response[2'048];
    if (!deck_setup_http_render_status(
            &snapshot,
            networks,
            network_count,
            response,
            sizeof(response)
        )) {
        return httpd_resp_send_500(request);
    }
    return send_json(request, response);
}

esp_err_t status_handler(httpd_req_t *request)
{
    auto *service = static_cast<deck_setup_service_t *>(request->user_ctx);
    if (!mark_activity(service)) {
        httpd_resp_set_status(request, "503 Service Unavailable");
        return send_json(request, "{\"error\":\"setup_inactive\"}");
    }
    return render_status(service, request);
}

esp_err_t scan_handler(httpd_req_t *request)
{
    auto *service = static_cast<deck_setup_service_t *>(request->user_ctx);
    if (!mark_activity(service)) {
        httpd_resp_set_status(request, "503 Service Unavailable");
        return send_json(request, "{\"error\":\"setup_inactive\"}");
    }
    if (xSemaphoreTake(service->network_mutex, portMAX_DELAY) != pdTRUE) {
        return httpd_resp_send_500(request);
    }

    wifi_ap_record_t records[kMaximumScanResults]{};
    uint16_t record_count = static_cast<uint16_t>(kMaximumScanResults);
    esp_err_t scan_result = esp_wifi_scan_start(nullptr, true);
    if (scan_result == ESP_OK) {
        scan_result = esp_wifi_scan_get_ap_records(&record_count, records);
    }
    if (scan_result != ESP_OK) {
        (void)esp_wifi_clear_ap_list();
        xSemaphoreGive(service->network_mutex);
        httpd_resp_set_status(request, "503 Service Unavailable");
        return send_json(request, "{\"error\":\"scan_unavailable\"}");
    }

    if (xSemaphoreTake(service->state_mutex, portMAX_DELAY) == pdTRUE) {
        service->scan_count = std::min(static_cast<size_t>(record_count), kMaximumScanResults);
        for (size_t index = 0; index < service->scan_count; ++index) {
            const size_t ssid_size = strnlen(
                reinterpret_cast<const char *>(records[index].ssid),
                sizeof(records[index].ssid)
            );
            const size_t copy_size =
                std::min(ssid_size, static_cast<size_t>(DECK_SETUP_SCAN_SSID_CAPACITY - 1));
            std::memcpy(service->scan_results[index].ssid, records[index].ssid, copy_size);
            service->scan_results[index].ssid[copy_size] = '\0';
            service->scan_results[index].rssi = records[index].rssi;
            service->scan_results[index].secure = records[index].authmode != WIFI_AUTH_OPEN;
        }
        xSemaphoreGive(service->state_mutex);
    }
    xSemaphoreGive(service->network_mutex);
    return render_status(service, request);
}

const char *start_http_server(deck_setup_service_t *service)
{
    httpd_config_t config = HTTPD_DEFAULT_CONFIG();
    config.stack_size = 8'192;
    config.max_uri_handlers = 3;
    if (httpd_start(&service->http_server, &config) != ESP_OK) {
        service->http_server = nullptr;
        return "http_start";
    }

    httpd_uri_t page{};
    page.uri = "/";
    page.method = HTTP_GET;
    page.handler = page_handler;
    page.user_ctx = service;
    httpd_uri_t status{};
    status.uri = "/api/status";
    status.method = HTTP_GET;
    status.handler = status_handler;
    status.user_ctx = service;
    httpd_uri_t scan{};
    scan.uri = "/api/scan";
    scan.method = HTTP_POST;
    scan.handler = scan_handler;
    scan.user_ctx = service;
    if (httpd_register_uri_handler(service->http_server, &page) != ESP_OK ||
        httpd_register_uri_handler(service->http_server, &status) != ESP_OK ||
        httpd_register_uri_handler(service->http_server, &scan) != ESP_OK) {
        (void)httpd_stop(service->http_server);
        service->http_server = nullptr;
        return "http_routes";
    }
    return nullptr;
}

void stop_network_locked(deck_setup_service_t *service)
{
    if (service->http_server != nullptr) {
        (void)httpd_stop(service->http_server);
        service->http_server = nullptr;
    }
    if (service->network_active) {
        (void)esp_wifi_stop();
        service->network_active = false;
    }
    if (xSemaphoreTake(service->state_mutex, portMAX_DELAY) == pdTRUE) {
        service->scan_count = 0;
        xSemaphoreGive(service->state_mutex);
    }
}

const char *start_network_locked(
    deck_setup_service_t *service,
    const deck_setup_snapshot_t *snapshot
)
{
    wifi_config_t access_point{};
    const size_t ssid_size = strnlen(snapshot->ssid, sizeof(snapshot->ssid));
    const size_t password_size = strnlen(snapshot->password, sizeof(snapshot->password));
    std::memcpy(access_point.ap.ssid, snapshot->ssid, ssid_size);
    std::memcpy(access_point.ap.password, snapshot->password, password_size);
    access_point.ap.ssid_len = static_cast<uint8_t>(ssid_size);
    access_point.ap.channel = kSetupChannel;
    access_point.ap.authmode = WIFI_AUTH_WPA2_PSK;
    access_point.ap.max_connection = kMaximumClients;
    access_point.ap.pmf_cfg.required = true;

    if (esp_wifi_set_mode(WIFI_MODE_APSTA) != ESP_OK) {
        return "wifi_mode";
    }
    if (esp_wifi_set_config(WIFI_IF_AP, &access_point) != ESP_OK) {
        return "wifi_config";
    }
    if (esp_wifi_start() != ESP_OK) {
        return "wifi_start";
    }
    service->network_active = true;
    const char *http_error = start_http_server(service);
    if (http_error != nullptr) {
        stop_network_locked(service);
        return http_error;
    }
    return nullptr;
}

const char *initialize_wifi(deck_setup_service_t *service)
{
    const esp_err_t nvs_result = nvs_flash_init();
    if (nvs_result != ESP_OK) {
        return "nvs_init";
    }
    const esp_err_t netif_result = esp_netif_init();
    if (netif_result != ESP_OK && netif_result != ESP_ERR_INVALID_STATE) {
        return "netif_init";
    }
    const esp_err_t event_result = esp_event_loop_create_default();
    if (event_result != ESP_OK && event_result != ESP_ERR_INVALID_STATE) {
        return "event_loop";
    }
    if (esp_netif_create_default_wifi_ap() == nullptr ||
        esp_netif_create_default_wifi_sta() == nullptr) {
        return "netif_create";
    }
    wifi_init_config_t wifi_config = WIFI_INIT_CONFIG_DEFAULT();
    if (esp_wifi_init(&wifi_config) != ESP_OK) {
        return "wifi_init";
    }
    if (esp_wifi_set_storage(WIFI_STORAGE_RAM) != ESP_OK) {
        return "wifi_storage";
    }
    return nullptr;
}

void fail_session(deck_setup_service_t *service, const char *stage)
{
    if (xSemaphoreTake(service->state_mutex, portMAX_DELAY) == pdTRUE) {
        (void)deck_setup_mode_stop(service->mode);
        xSemaphoreGive(service->state_mutex);
    }
    notify(service, DECK_SETUP_SERVICE_ERROR, stage);
}

void enter_session(deck_setup_service_t *service, deck_setup_reason_t reason)
{
    deck_setup_snapshot_t snapshot{};
    deck_setup_mode_result_t result = DECK_SETUP_MODE_ERROR;
    if (xSemaphoreTake(service->state_mutex, portMAX_DELAY) == pdTRUE) {
        result = deck_setup_mode_enter(service->mode, reason, monotonic_ms());
        (void)snapshot_locked(service, &snapshot);
        xSemaphoreGive(service->state_mutex);
    }
    if (result != DECK_SETUP_MODE_STARTED && result != DECK_SETUP_MODE_RESTARTED) {
        fail_session(service, "credentials");
        return;
    }

    if (xSemaphoreTake(service->network_mutex, portMAX_DELAY) != pdTRUE) {
        fail_session(service, "network_lock");
        return;
    }
    stop_network_locked(service);
    const char *error_stage = start_network_locked(service, &snapshot);
    xSemaphoreGive(service->network_mutex);
    if (error_stage != nullptr) {
        fail_session(service, error_stage);
        return;
    }
    notify(service, DECK_SETUP_SERVICE_ACTIVE, nullptr);
}

void service_task(void *task_context)
{
    auto *service = static_cast<deck_setup_service_t *>(task_context);
    const char *initialization_error = initialize_wifi(service);
    if (initialization_error != nullptr) {
        service->accepting_commands.store(false, std::memory_order_release);
        fail_session(service, initialization_error);
        vTaskDelete(nullptr);
        return;
    }
    service->accepting_commands.store(true, std::memory_order_release);

    if (!service->has_valid_wifi_config) {
        enter_session(service, DECK_SETUP_REASON_NO_WIFI);
    } else {
        notify(service, DECK_SETUP_SERVICE_INACTIVE, nullptr);
    }

    while (true) {
        Command command{};
        if (xQueueReceive(service->commands, &command, kServicePollTicks) == pdTRUE &&
            command == Command::enter_from_boot) {
            enter_session(service, DECK_SETUP_REASON_BOOT_LONG_PRESS);
        }

        deck_setup_mode_result_t tick_result = DECK_SETUP_MODE_UNCHANGED;
        if (xSemaphoreTake(service->state_mutex, portMAX_DELAY) == pdTRUE) {
            tick_result = deck_setup_mode_tick(service->mode, monotonic_ms());
            xSemaphoreGive(service->state_mutex);
        }
        if (tick_result == DECK_SETUP_MODE_STOPPED) {
            if (xSemaphoreTake(service->network_mutex, portMAX_DELAY) == pdTRUE) {
                stop_network_locked(service);
                xSemaphoreGive(service->network_mutex);
            }
            notify(service, DECK_SETUP_SERVICE_INACTIVE, nullptr);
        }
    }
}

void release_unstarted(deck_setup_service_t *service)
{
    if (service == nullptr) {
        return;
    }
    deck_setup_mode_destroy(service->mode);
    if (service->commands != nullptr) {
        vQueueDelete(service->commands);
    }
    if (service->state_mutex != nullptr) {
        vSemaphoreDelete(service->state_mutex);
    }
    if (service->network_mutex != nullptr) {
        vSemaphoreDelete(service->network_mutex);
    }
    delete service;
}

}  // namespace

deck_setup_service_t *deck_setup_service_start(
    bool has_valid_wifi_config,
    deck_setup_service_event_fn callback,
    void *callback_context
)
{
    if (callback == nullptr) {
        return nullptr;
    }
    auto *service = new (std::nothrow) deck_setup_service_t{};
    if (service == nullptr) {
        return nullptr;
    }
    service->callback = callback;
    service->callback_context = callback_context;
    service->has_valid_wifi_config = has_valid_wifi_config;
    service->state_mutex = xSemaphoreCreateMutex();
    service->network_mutex = xSemaphoreCreateMutex();
    // A BOOT request means "enter now" rather than "enter N times". Coalesce
    // repeated notifications while the service task is busy with a scan or restart.
    service->commands = xQueueCreate(1, sizeof(Command));
    const deck_setup_mode_config_t mode_config = {
        static_cast<uint64_t>(CONFIG_DECK_SETUP_INACTIVITY_TIMEOUT_SECONDS) * 1'000U,
        fill_random,
        nullptr,
    };
    service->mode = deck_setup_mode_create(&mode_config);
    if (service->state_mutex == nullptr || service->network_mutex == nullptr ||
        service->commands == nullptr || service->mode == nullptr) {
        release_unstarted(service);
        return nullptr;
    }
    if (xTaskCreatePinnedToCore(
            service_task,
            "deck_setup",
            kServiceTaskStackBytes,
            service,
            kServiceTaskPriority,
            nullptr,
            0
        ) != pdPASS) {
        release_unstarted(service);
        return nullptr;
    }
    return service;
}

bool deck_setup_service_enter_from_boot(deck_setup_service_t *service)
{
    if (service == nullptr ||
        !service->accepting_commands.load(std::memory_order_acquire)) {
        return false;
    }
    const Command command = Command::enter_from_boot;
    return xQueueOverwrite(service->commands, &command) == pdPASS;
}
