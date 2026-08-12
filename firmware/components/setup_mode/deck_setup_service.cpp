#include "deck_setup_service.h"

#include "deck_setup_http.h"
#include "deck_wifi_config_nvs.h"
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
#include "freertos/event_groups.h"
#include "freertos/queue.h"
#include "freertos/semphr.h"
#include "freertos/task.h"
#include "lwip/inet.h"
#include "lwip/sockets.h"
#include "nvs_flash.h"

namespace {

constexpr uint8_t kSetupChannel = 1;
constexpr uint8_t kMaximumClients = 4;
constexpr size_t kMaximumScanResults = 10;
constexpr uint32_t kServiceTaskStackBytes = 6'144;
constexpr UBaseType_t kServiceTaskPriority = 2;
constexpr uint32_t kPublisherTaskStackBytes = 4'096;
constexpr UBaseType_t kPublisherTaskPriority = 1;
constexpr TickType_t kServicePollTicks = pdMS_TO_TICKS(100);
constexpr TickType_t kWifiEventTimeoutTicks = pdMS_TO_TICKS(2'000);
constexpr size_t kErrorStageCapacity = 24;
constexpr EventBits_t kApStartedBit = BIT0;
constexpr EventBits_t kApStoppedBit = BIT1;
constexpr EventBits_t kStaGotIpBit = BIT2;
constexpr uint64_t kValidationTimeoutMs =
    static_cast<uint64_t>(CONFIG_DECK_WIFI_VALIDATION_TIMEOUT_SECONDS) * 1'000U;

enum class CommandType : uint8_t {
    enter_from_boot,
    submit_wifi,
    validation_connected,
    validation_auth_failed,
    validation_connection_failed,
};

struct Command {
    CommandType type;
    deck_wifi_credentials_t credentials;
};

enum class EnterSessionResult : uint8_t {
    handled,
    retry_after_shutdown,
};

struct ServiceNotification {
    deck_setup_service_state_t state;
    deck_setup_snapshot_t setup;
    deck_wifi_config_snapshot_t wifi;
    char error_stage[kErrorStageCapacity];
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
    QueueHandle_t notifications;
    TaskHandle_t publisher_task;
    EventGroupHandle_t wifi_events;
    esp_event_handler_instance_t wifi_event_handler;
    esp_event_handler_instance_t ip_event_handler;
    httpd_handle_t http_server;
    deck_wifi_nvs_storage_t *wifi_storage;
    deck_wifi_config_t *wifi_config;
    deck_setup_scan_result_t scan_results[kMaximumScanResults];
    size_t scan_count;
    bool wifi_started;
    bool network_active;
    bool stop_requested;
    bool stop_keeps_wifi;
    bool shutdown_pending;
    bool active_station_restore_pending;
    std::atomic<bool> suppress_disconnect{false};
    std::atomic<bool> command_overflow{false};
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
    deck_wifi_config_snapshot_t *wifi,
    deck_setup_scan_result_t *networks,
    size_t network_capacity,
    size_t *network_count
)
{
    if (service == nullptr || snapshot == nullptr || network_count == nullptr ||
        xSemaphoreTake(service->state_mutex, portMAX_DELAY) != pdTRUE) {
        return false;
    }
    bool wifi_copied = true;
    if (wifi != nullptr) {
        *wifi = {};
        wifi_copied = service->wifi_config == nullptr ||
                      deck_wifi_config_snapshot(service->wifi_config, wifi);
    }
    const bool copied = snapshot_locked(service, snapshot) && wifi_copied;
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
    deck_wifi_config_snapshot_t wifi{};
    size_t unused_count = 0;
    if (!copy_state(service, &snapshot, &wifi, nullptr, 0, &unused_count)) {
        return;
    }
    ServiceNotification notification{state, snapshot, wifi, {}};
    if (error_stage != nullptr) {
        const size_t copy_size =
            std::min(strlen(error_stage), sizeof(notification.error_stage) - 1);
        std::memcpy(notification.error_stage, error_stage, copy_size);
        notification.error_stage[copy_size] = '\0';
    }
    (void)xQueueOverwrite(service->notifications, &notification);
}

void publisher_task(void *task_context)
{
    auto *service = static_cast<deck_setup_service_t *>(task_context);
    while (true) {
        ServiceNotification notification{};
        if (xQueueReceive(service->notifications, &notification, portMAX_DELAY) != pdTRUE) {
            continue;
        }
        const deck_setup_service_event_t event = {
            notification.state,
            notification.setup,
            notification.wifi,
            notification.error_stage[0] == '\0' ? nullptr : notification.error_stage,
        };
        service->callback(service->callback_context, &event);
    }
}

bool enqueue_command(deck_setup_service_t *service, const Command &command)
{
    if (service == nullptr ||
        xQueueSend(service->commands, &command, 0) != pdPASS) {
        if (service != nullptr) {
            service->command_overflow.store(true, std::memory_order_release);
        }
        return false;
    }
    return true;
}

bool authentication_failure(uint8_t reason)
{
    return reason == WIFI_REASON_AUTH_EXPIRE || reason == WIFI_REASON_ASSOC_NOT_AUTHED ||
           reason == WIFI_REASON_MIC_FAILURE ||
           reason == WIFI_REASON_4WAY_HANDSHAKE_TIMEOUT ||
           reason == WIFI_REASON_GROUP_KEY_UPDATE_TIMEOUT ||
           reason == WIFI_REASON_802_1X_AUTH_FAILED || reason == WIFI_REASON_AUTH_FAIL ||
           reason == WIFI_REASON_HANDSHAKE_TIMEOUT;
}

void wifi_event(void *context, esp_event_base_t, int32_t event_id, void *event_data)
{
    auto *service = static_cast<deck_setup_service_t *>(context);
    if (event_id == WIFI_EVENT_AP_START) {
        xEventGroupSetBits(service->wifi_events, kApStartedBit);
    } else if (event_id == WIFI_EVENT_AP_STOP) {
        xEventGroupSetBits(service->wifi_events, kApStoppedBit);
    } else if (event_id == WIFI_EVENT_STA_DISCONNECTED && event_data != nullptr &&
               service->accepting_commands.load(std::memory_order_acquire)) {
        if (service->suppress_disconnect.exchange(false, std::memory_order_acq_rel)) {
            return;
        }
        const auto *disconnected =
            static_cast<const wifi_event_sta_disconnected_t *>(event_data);
        const Command command = {
            authentication_failure(disconnected->reason)
                ? CommandType::validation_auth_failed
                : CommandType::validation_connection_failed,
            {},
        };
        (void)enqueue_command(service, command);
    }
}

void ip_event(void *context, esp_event_base_t, int32_t event_id, void *)
{
    auto *service = static_cast<deck_setup_service_t *>(context);
    if (event_id != IP_EVENT_STA_GOT_IP) {
        return;
    }
    xEventGroupSetBits(service->wifi_events, kStaGotIpBit);
    if (service->accepting_commands.load(std::memory_order_acquire)) {
        const Command command = {CommandType::validation_connected, {}};
        (void)enqueue_command(service, command);
    }
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
    deck_wifi_config_snapshot_t wifi{};
    deck_setup_scan_result_t networks[kMaximumScanResults]{};
    size_t network_count = 0;
    if (!copy_state(
            service,
            &snapshot,
            &wifi,
            networks,
            kMaximumScanResults,
            &network_count
        )) {
        return httpd_resp_send_500(request);
    }
    char response[2'048];
    if (!deck_setup_http_render_status(
            &snapshot,
            &wifi,
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

    deck_setup_scan_observation_t observations[kMaximumScanResults]{};
    const size_t observation_count =
        std::min(static_cast<size_t>(record_count), kMaximumScanResults);
    for (size_t index = 0; index < observation_count; ++index) {
        observations[index] = {
            records[index].ssid,
            strnlen(
                reinterpret_cast<const char *>(records[index].ssid),
                sizeof(records[index].ssid)
            ),
            records[index].rssi,
            records[index].authmode != WIFI_AUTH_OPEN,
        };
    }
    deck_setup_scan_result_t converted[kMaximumScanResults]{};
    size_t converted_count = 0;
    const bool converted_ok = deck_setup_http_convert_scan_results(
        observations,
        observation_count,
        converted,
        kMaximumScanResults,
        &converted_count
    );
    xSemaphoreGive(service->network_mutex);
    if (converted_ok && xSemaphoreTake(service->state_mutex, portMAX_DELAY) == pdTRUE) {
        std::memcpy(
            service->scan_results,
            converted,
            converted_count * sizeof(converted[0])
        );
        service->scan_count = converted_count;
        xSemaphoreGive(service->state_mutex);
    }
    if (!converted_ok) {
        return httpd_resp_send_500(request);
    }
    return render_status(service, request);
}

esp_err_t wifi_handler(httpd_req_t *request)
{
    auto *service = static_cast<deck_setup_service_t *>(request->user_ctx);
    if (!mark_activity(service)) {
        httpd_resp_set_status(request, "503 Service Unavailable");
        return send_json(request, "{\"accepted\":false,\"error\":\"setup_inactive\"}");
    }
    constexpr size_t kMaximumBodySize = 256;
    if (request->content_len == 0 || request->content_len > kMaximumBodySize) {
        httpd_resp_set_status(request, "400 Bad Request");
        return send_json(request, "{\"accepted\":false,\"error\":\"malformed\"}");
    }
    char body[kMaximumBodySize];
    size_t received = 0;
    while (received < request->content_len) {
        const int result = httpd_req_recv(
            request,
            body + received,
            request->content_len - received
        );
        if (result <= 0) {
            httpd_resp_set_status(request, "400 Bad Request");
            return send_json(request, "{\"accepted\":false,\"error\":\"malformed\"}");
        }
        received += static_cast<size_t>(result);
    }

    Command command{CommandType::submit_wifi, {}};
    const deck_setup_wifi_request_result_t parsed = deck_setup_http_parse_wifi_request(
        body,
        received,
        &command.credentials
    );
    if (parsed != DECK_SETUP_WIFI_REQUEST_OK) {
        deck_wifi_credentials_clear(&command.credentials);
        httpd_resp_set_status(request, "400 Bad Request");
        if (parsed == DECK_SETUP_WIFI_REQUEST_INVALID_SSID) {
            return send_json(request, "{\"accepted\":false,\"error\":\"invalid_ssid\"}");
        }
        if (parsed == DECK_SETUP_WIFI_REQUEST_INVALID_PASSWORD) {
            return send_json(request, "{\"accepted\":false,\"error\":\"invalid_password\"}");
        }
        return send_json(request, "{\"accepted\":false,\"error\":\"malformed\"}");
    }
    const bool queued = deck_setup_service_submit_wifi(service, &command.credentials);
    deck_wifi_credentials_clear(&command.credentials);
    if (!queued) {
        httpd_resp_set_status(request, "503 Service Unavailable");
        return send_json(request, "{\"accepted\":false,\"error\":\"busy\"}");
    }
    httpd_resp_set_status(request, "202 Accepted");
    return send_json(request, "{\"accepted\":true,\"state\":\"queued\"}");
}

esp_err_t accept_ap_session(httpd_handle_t, int socket_fd)
{
    sockaddr_storage local_address{};
    socklen_t address_size = sizeof(local_address);
    if (getsockname(
            socket_fd,
            reinterpret_cast<sockaddr *>(&local_address),
            &address_size
        ) != 0 || local_address.ss_family != AF_INET) {
        return ESP_FAIL;
    }
    const auto *ipv4 = reinterpret_cast<const sockaddr_in *>(&local_address);
    return ipv4->sin_addr.s_addr == inet_addr("192.168.4.1") ? ESP_OK : ESP_FAIL;
}

using HttpHandler = esp_err_t (*)(httpd_req_t *);

HttpHandler handler_for_route(deck_setup_http_route_t route)
{
    switch (route) {
        case DECK_SETUP_HTTP_PAGE:
            return page_handler;
        case DECK_SETUP_HTTP_STATUS:
            return status_handler;
        case DECK_SETUP_HTTP_SCAN:
            return scan_handler;
        case DECK_SETUP_HTTP_WIFI:
            return wifi_handler;
        case DECK_SETUP_HTTP_NOT_FOUND:
        case DECK_SETUP_HTTP_METHOD_NOT_ALLOWED:
        default:
            return nullptr;
    }
}

const char *start_http_server(deck_setup_service_t *service)
{
    size_t route_count = 0;
    const deck_setup_http_route_spec_t *routes = deck_setup_http_routes(&route_count);
    if (routes == nullptr || route_count == 0 || route_count > UINT16_MAX) {
        return "http_routes";
    }
    httpd_config_t config = HTTPD_DEFAULT_CONFIG();
    config.stack_size = 8'192;
    config.max_uri_handlers = static_cast<uint16_t>(route_count);
    config.open_fn = accept_ap_session;
    if (httpd_start(&service->http_server, &config) != ESP_OK) {
        service->http_server = nullptr;
        return "http_start";
    }

    for (size_t index = 0; index < route_count; ++index) {
        httpd_uri_t registration{};
        registration.uri = routes[index].path;
        registration.method = routes[index].method == DECK_SETUP_HTTP_GET
                                  ? HTTP_GET
                                  : HTTP_POST;
        registration.handler = handler_for_route(routes[index].route);
        registration.user_ctx = service;
        if (registration.handler == nullptr ||
            httpd_register_uri_handler(service->http_server, &registration) != ESP_OK) {
            (void)httpd_stop(service->http_server);
            service->http_server = nullptr;
            return "http_routes";
        }
    }
    return nullptr;
}

void fill_station_config(
    const deck_wifi_credentials_t *credentials,
    wifi_config_t *station
);

bool restore_active_station_locked(
    deck_setup_service_t *service,
    const deck_wifi_credentials_t *credentials
)
{
    wifi_config_t station{};
    fill_station_config(credentials, &station);
    if (esp_wifi_set_mode(WIFI_MODE_STA) != ESP_OK ||
        esp_wifi_set_config(WIFI_IF_STA, &station) != ESP_OK) {
        return false;
    }
    if (!service->wifi_started) {
        if (esp_wifi_start() != ESP_OK) {
            return false;
        }
        service->wifi_started = true;
    }
    xEventGroupClearBits(service->wifi_events, kStaGotIpBit);
    return esp_wifi_connect() == ESP_OK;
}

const char *stop_network(deck_setup_service_t *service)
{
    const char *error_stage = nullptr;
    deck_wifi_config_snapshot_t wifi{};
    deck_wifi_credentials_t active_credentials{};
    bool has_active_credentials = false;
    if (xSemaphoreTake(service->state_mutex, portMAX_DELAY) == pdTRUE) {
        (void)deck_wifi_config_snapshot(service->wifi_config, &wifi);
        has_active_credentials = deck_wifi_config_active_credentials(
            service->wifi_config,
            &active_credentials
        );
        if (service->active_station_restore_pending && has_active_credentials) {
            (void)deck_wifi_config_active_connection(service->wifi_config, false);
        }
        xSemaphoreGive(service->state_mutex);
    }
    // Never hold network_mutex while httpd_stop waits for in-flight handlers:
    // a scan handler may need that mutex before it can finish.
    if (service->http_server != nullptr) {
        if (httpd_stop(service->http_server) == ESP_OK) {
            service->http_server = nullptr;
        } else {
            error_stage = "http_stop";
        }
    }
    if (xSemaphoreTake(service->network_mutex, portMAX_DELAY) != pdTRUE) {
        deck_wifi_credentials_clear(&active_credentials);
        return error_stage == nullptr ? "network_lock" : error_stage;
    }
    if (service->network_active) {
        if (!service->stop_requested) {
            xEventGroupClearBits(service->wifi_events, kApStoppedBit);
            const bool keep_wifi = wifi.has_active;
            const esp_err_t stop_result = keep_wifi
                                              ? esp_wifi_set_mode(WIFI_MODE_STA)
                                              : esp_wifi_stop();
            if (stop_result != ESP_OK) {
                if (error_stage == nullptr) {
                    error_stage = "wifi_stop";
                }
            } else {
                service->stop_requested = true;
                service->stop_keeps_wifi = keep_wifi;
            }
        }
        if (service->stop_requested) {
            const EventBits_t events = xEventGroupWaitBits(
                service->wifi_events,
                kApStoppedBit,
                pdTRUE,
                pdTRUE,
                kWifiEventTimeoutTicks
            );
            if ((events & kApStoppedBit) != 0) {
                service->network_active = false;
                service->wifi_started = service->stop_keeps_wifi;
                service->stop_requested = false;
                service->stop_keeps_wifi = false;
            } else if (error_stage == nullptr) {
                error_stage = "wifi_stop_event";
            }
        }
    }
    if (!service->network_active && service->active_station_restore_pending) {
        if (!has_active_credentials ||
            !restore_active_station_locked(service, &active_credentials)) {
            if (error_stage == nullptr) {
                error_stage = "wifi_restore";
            }
        } else {
            service->active_station_restore_pending = false;
        }
    }
    xSemaphoreGive(service->network_mutex);
    deck_wifi_credentials_clear(&active_credentials);
    if (xSemaphoreTake(service->state_mutex, portMAX_DELAY) == pdTRUE) {
        service->scan_count = 0;
        xSemaphoreGive(service->state_mutex);
    }
    service->shutdown_pending = error_stage != nullptr;
    return error_stage;
}

const char *start_network(
    deck_setup_service_t *service,
    const deck_setup_snapshot_t *snapshot
)
{
    deck_wifi_config_snapshot_t wifi{};
    if (xSemaphoreTake(service->state_mutex, portMAX_DELAY) != pdTRUE) {
        return "state_lock";
    }
    (void)deck_wifi_config_snapshot(service->wifi_config, &wifi);
    if (wifi.has_active) {
        (void)deck_wifi_config_active_connection(service->wifi_config, false);
    }
    xSemaphoreGive(service->state_mutex);
    service->active_station_restore_pending = wifi.has_active;

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

    if (xSemaphoreTake(service->network_mutex, portMAX_DELAY) != pdTRUE) {
        return "network_lock";
    }
    const char *error_stage = nullptr;
    if (service->wifi_started) {
        if (esp_wifi_stop() != ESP_OK) {
            error_stage = "wifi_restart";
        } else {
            service->wifi_started = false;
        }
    }
    if (error_stage == nullptr && esp_wifi_set_mode(WIFI_MODE_APSTA) != ESP_OK) {
        error_stage = "wifi_mode";
    }
    if (error_stage == nullptr && esp_wifi_set_config(WIFI_IF_AP, &access_point) != ESP_OK) {
        error_stage = "wifi_config";
    } else if (error_stage == nullptr) {
        xEventGroupClearBits(service->wifi_events, kApStartedBit);
    }
    if (error_stage == nullptr && esp_wifi_start() != ESP_OK) {
        error_stage = "wifi_start";
    } else if (error_stage == nullptr) {
        service->wifi_started = true;
        service->network_active = true;
        service->stop_requested = false;
        const EventBits_t events = xEventGroupWaitBits(
            service->wifi_events,
            kApStartedBit,
            pdTRUE,
            pdTRUE,
            kWifiEventTimeoutTicks
        );
        if ((events & kApStartedBit) == 0) {
            error_stage = "wifi_start_event";
        }
    }
    xSemaphoreGive(service->network_mutex);
    if (error_stage != nullptr) {
        if (service->network_active) {
            (void)stop_network(service);
        }
        return error_stage;
    }
    const char *http_error = start_http_server(service);
    if (http_error != nullptr) {
        (void)stop_network(service);
        return http_error;
    }
    service->shutdown_pending = false;
    return nullptr;
}

void fill_station_config(
    const deck_wifi_credentials_t *credentials,
    wifi_config_t *station
)
{
    *station = {};
    const size_t ssid_size = strnlen(credentials->ssid, sizeof(credentials->ssid));
    const size_t password_size = strnlen(credentials->password, sizeof(credentials->password));
    std::memcpy(station->sta.ssid, credentials->ssid, ssid_size);
    std::memcpy(station->sta.password, credentials->password, password_size);
    station->sta.scan_method = WIFI_ALL_CHANNEL_SCAN;
    station->sta.sort_method = WIFI_CONNECT_AP_BY_SIGNAL;
    station->sta.threshold.authmode = WIFI_AUTH_OPEN;
    station->sta.pmf_cfg.capable = true;
    station->sta.pmf_cfg.required = false;
}

bool begin_wifi_validation(void *context, const deck_wifi_credentials_t *credentials)
{
    auto *service = static_cast<deck_setup_service_t *>(context);
    if (service == nullptr || credentials == nullptr ||
        xSemaphoreTake(service->network_mutex, portMAX_DELAY) != pdTRUE) {
        return false;
    }
    wifi_config_t station{};
    fill_station_config(credentials, &station);

    service->suppress_disconnect.store(true, std::memory_order_release);
    const esp_err_t disconnect_result = esp_wifi_disconnect();
    if (disconnect_result != ESP_OK) {
        service->suppress_disconnect.store(false, std::memory_order_release);
    }
    const bool started = service->wifi_started &&
                         (disconnect_result == ESP_OK ||
                          disconnect_result == ESP_ERR_WIFI_NOT_CONNECT) &&
                         esp_wifi_set_config(WIFI_IF_STA, &station) == ESP_OK &&
                         esp_wifi_connect() == ESP_OK;
    xSemaphoreGive(service->network_mutex);
    return started;
}

void cancel_wifi_validation(void *context)
{
    auto *service = static_cast<deck_setup_service_t *>(context);
    if (service == nullptr ||
        xSemaphoreTake(service->network_mutex, portMAX_DELAY) != pdTRUE) {
        return;
    }
    (void)esp_wifi_disconnect();
    xSemaphoreGive(service->network_mutex);
}

bool connect_active_station(deck_setup_service_t *service)
{
    deck_wifi_credentials_t credentials{};
    if (xSemaphoreTake(service->state_mutex, portMAX_DELAY) != pdTRUE) {
        return false;
    }
    const bool has_active = deck_wifi_config_active_credentials(
        service->wifi_config,
        &credentials
    );
    xSemaphoreGive(service->state_mutex);
    if (!has_active || xSemaphoreTake(service->network_mutex, portMAX_DELAY) != pdTRUE) {
        deck_wifi_credentials_clear(&credentials);
        return false;
    }

    wifi_config_t station{};
    fill_station_config(&credentials, &station);
    xEventGroupClearBits(service->wifi_events, kStaGotIpBit);
    bool started = esp_wifi_set_mode(WIFI_MODE_STA) == ESP_OK &&
                   esp_wifi_set_config(WIFI_IF_STA, &station) == ESP_OK &&
                   esp_wifi_start() == ESP_OK;
    if (started) {
        service->wifi_started = true;
        started = esp_wifi_connect() == ESP_OK;
    }
    EventBits_t events = 0;
    if (started) {
        events = xEventGroupWaitBits(
            service->wifi_events,
            kStaGotIpBit,
            pdTRUE,
            pdTRUE,
            pdMS_TO_TICKS(kValidationTimeoutMs)
        );
    }
    xSemaphoreGive(service->network_mutex);
    deck_wifi_credentials_clear(&credentials);
    const bool connected = started && (events & kStaGotIpBit) != 0;
    if (xSemaphoreTake(service->state_mutex, portMAX_DELAY) == pdTRUE) {
        (void)deck_wifi_config_active_connection(service->wifi_config, connected);
        xSemaphoreGive(service->state_mutex);
    }
    return connected;
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
    if (esp_event_handler_instance_register(
            WIFI_EVENT,
            ESP_EVENT_ANY_ID,
            wifi_event,
            service,
            &service->wifi_event_handler
        ) != ESP_OK) {
        return "wifi_events";
    }
    if (esp_event_handler_instance_register(
            IP_EVENT,
            IP_EVENT_STA_GOT_IP,
            ip_event,
            service,
            &service->ip_event_handler
        ) != ESP_OK) {
        return "ip_events";
    }
    if (esp_wifi_set_storage(WIFI_STORAGE_RAM) != ESP_OK) {
        return "wifi_storage";
    }
    service->wifi_storage = deck_wifi_nvs_storage_open();
    deck_wifi_storage_adapter_t storage_adapter{};
    if (service->wifi_storage == nullptr ||
        !deck_wifi_nvs_storage_adapter(service->wifi_storage, &storage_adapter)) {
        return "wifi_store";
    }
    const deck_wifi_config_options_t options = {
        storage_adapter,
        {begin_wifi_validation, cancel_wifi_validation, service},
        kValidationTimeoutMs,
    };
    service->wifi_config = deck_wifi_config_create(&options);
    if (service->wifi_config == nullptr) {
        return "wifi_config";
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

EnterSessionResult enter_session(deck_setup_service_t *service, deck_setup_reason_t reason)
{
    const char *stop_error = stop_network(service);
    if (stop_error != nullptr) {
        fail_session(service, stop_error);
        return EnterSessionResult::retry_after_shutdown;
    }

    deck_setup_snapshot_t snapshot{};
    deck_setup_mode_result_t result = DECK_SETUP_MODE_ERROR;
    if (xSemaphoreTake(service->state_mutex, portMAX_DELAY) == pdTRUE) {
        result = deck_setup_mode_enter(service->mode, reason, monotonic_ms());
        (void)snapshot_locked(service, &snapshot);
        xSemaphoreGive(service->state_mutex);
    }
    if (result != DECK_SETUP_MODE_STARTED && result != DECK_SETUP_MODE_RESTARTED) {
        fail_session(service, "credentials");
        return EnterSessionResult::handled;
    }

    const char *error_stage = start_network(service, &snapshot);
    if (error_stage != nullptr) {
        fail_session(service, error_stage);
        return EnterSessionResult::handled;
    }
    notify(service, DECK_SETUP_SERVICE_ACTIVE, nullptr);
    return EnterSessionResult::handled;
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
    deck_wifi_config_snapshot_t initial_wifi{};
    if (!deck_wifi_config_snapshot(service->wifi_config, &initial_wifi)) {
        service->accepting_commands.store(false, std::memory_order_release);
        fail_session(service, "wifi_snapshot");
        vTaskDelete(nullptr);
        return;
    }
    if (deck_wifi_config_recovery_required(&initial_wifi)) {
        enter_session(service, DECK_SETUP_REASON_NO_WIFI);
    } else if (connect_active_station(service)) {
        notify(service, DECK_SETUP_SERVICE_INACTIVE, nullptr);
    } else {
        enter_session(service, DECK_SETUP_REASON_NO_WIFI);
    }
    service->accepting_commands.store(true, std::memory_order_release);

    bool boot_enter_pending = false;
    while (true) {
        Command command{};
        if (xQueueReceive(service->commands, &command, kServicePollTicks) == pdTRUE) {
            switch (command.type) {
                case CommandType::enter_from_boot:
                    boot_enter_pending = true;
                    break;
                case CommandType::submit_wifi: {
                    deck_wifi_submit_result_t result = DECK_WIFI_SUBMIT_STORAGE_ERROR;
                    bool setup_active = false;
                    if (xSemaphoreTake(service->state_mutex, portMAX_DELAY) == pdTRUE) {
                        deck_setup_snapshot_t setup{};
                        setup_active = snapshot_locked(service, &setup) && setup.active;
                        if (setup_active) {
                            result = deck_wifi_config_submit(
                                service->wifi_config,
                                &command.credentials,
                                monotonic_ms()
                            );
                        }
                        xSemaphoreGive(service->state_mutex);
                    }
                    deck_wifi_credentials_clear(&command.credentials);
                    (void)result;
                    notify(
                        service,
                        setup_active ? DECK_SETUP_SERVICE_ACTIVE
                                     : DECK_SETUP_SERVICE_INACTIVE,
                        nullptr
                    );
                    break;
                }
                case CommandType::validation_connected:
                case CommandType::validation_auth_failed:
                case CommandType::validation_connection_failed: {
                    const deck_wifi_validation_result_t result =
                        command.type == CommandType::validation_connected
                            ? DECK_WIFI_VALIDATION_CONNECTED
                            : command.type == CommandType::validation_auth_failed
                                  ? DECK_WIFI_VALIDATION_AUTH_FAILED
                                  : DECK_WIFI_VALIDATION_CONNECTION_FAILED;
                    bool transitioned = false;
                    deck_wifi_config_snapshot_t wifi{};
                    if (xSemaphoreTake(service->state_mutex, portMAX_DELAY) == pdTRUE) {
                        transitioned = deck_wifi_config_validation_result(
                            service->wifi_config,
                            result
                        );
                        (void)deck_wifi_config_snapshot(service->wifi_config, &wifi);
                        if (transitioned && wifi.state == DECK_WIFI_CONFIG_ACTIVE) {
                            (void)deck_setup_mode_stop(service->mode);
                        }
                        xSemaphoreGive(service->state_mutex);
                    }
                    if (transitioned && wifi.state == DECK_WIFI_CONFIG_ACTIVE) {
                        service->active_station_restore_pending = false;
                        const char *stop_error = stop_network(service);
                        notify(
                            service,
                            stop_error == nullptr ? DECK_SETUP_SERVICE_INACTIVE
                                                  : DECK_SETUP_SERVICE_ERROR,
                            stop_error
                        );
                    } else if (transitioned) {
                        notify(service, DECK_SETUP_SERVICE_ACTIVE, nullptr);
                    } else if (wifi.state == DECK_WIFI_CONFIG_STORAGE_ERROR) {
                        notify(service, DECK_SETUP_SERVICE_ERROR, "wifi_commit");
                    } else {
                        bool runtime_changed = false;
                        bool setup_active = false;
                        if (xSemaphoreTake(service->state_mutex, portMAX_DELAY) == pdTRUE) {
                            runtime_changed = deck_wifi_config_active_connection(
                                service->wifi_config,
                                command.type == CommandType::validation_connected
                            );
                            deck_setup_snapshot_t setup{};
                            (void)snapshot_locked(service, &setup);
                            setup_active = setup.active;
                            xSemaphoreGive(service->state_mutex);
                        }
                        if (runtime_changed &&
                            command.type != CommandType::validation_connected &&
                            !setup_active) {
                            (void)enter_session(service, DECK_SETUP_REASON_NO_WIFI);
                        } else if (runtime_changed) {
                            notify(
                                service,
                                setup_active ? DECK_SETUP_SERVICE_ACTIVE
                                             : DECK_SETUP_SERVICE_INACTIVE,
                                nullptr
                            );
                        }
                    }
                    break;
                }
            }
        }
        if (service->command_overflow.exchange(false, std::memory_order_acq_rel)) {
            notify(service, DECK_SETUP_SERVICE_ERROR, "command_queue");
        }

        deck_setup_mode_result_t tick_result = DECK_SETUP_MODE_UNCHANGED;
        bool validation_timed_out = false;
        if (xSemaphoreTake(service->state_mutex, portMAX_DELAY) == pdTRUE) {
            const uint64_t now_ms = monotonic_ms();
            deck_wifi_config_snapshot_t wifi{};
            if (deck_wifi_config_snapshot(service->wifi_config, &wifi) &&
                wifi.state == DECK_WIFI_CONFIG_VALIDATING) {
                (void)deck_setup_mode_activity(service->mode, now_ms);
            }
            tick_result = deck_setup_mode_tick(service->mode, now_ms);
            validation_timed_out = deck_wifi_config_tick(
                service->wifi_config,
                now_ms
            );
            xSemaphoreGive(service->state_mutex);
        }
        if (validation_timed_out) {
            notify(service, DECK_SETUP_SERVICE_ACTIVE, nullptr);
        }
        if (tick_result == DECK_SETUP_MODE_STOPPED || service->shutdown_pending) {
            const char *stop_error = stop_network(service);
            if (stop_error == nullptr) {
                notify(service, DECK_SETUP_SERVICE_INACTIVE, nullptr);
            } else {
                notify(service, DECK_SETUP_SERVICE_ERROR, stop_error);
            }
        }
        if (boot_enter_pending && !service->shutdown_pending) {
            boot_enter_pending = enter_session(
                                     service,
                                     DECK_SETUP_REASON_BOOT_LONG_PRESS
                                 ) == EnterSessionResult::retry_after_shutdown;
        }
    }
}

void release_unstarted(deck_setup_service_t *service)
{
    if (service == nullptr) {
        return;
    }
    deck_setup_mode_destroy(service->mode);
    deck_wifi_config_destroy(service->wifi_config);
    deck_wifi_nvs_storage_close(service->wifi_storage);
    if (service->publisher_task != nullptr) {
        vTaskDelete(service->publisher_task);
    }
    if (service->commands != nullptr) {
        vQueueDelete(service->commands);
    }
    if (service->notifications != nullptr) {
        vQueueDelete(service->notifications);
    }
    if (service->wifi_events != nullptr) {
        vEventGroupDelete(service->wifi_events);
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
    service->state_mutex = xSemaphoreCreateMutex();
    service->network_mutex = xSemaphoreCreateMutex();
    // A BOOT request means "enter now" rather than "enter N times". Coalesce
    // repeated notifications while the service task is busy with a scan or restart.
    service->commands = xQueueCreate(8, sizeof(Command));
    // State publication is latest-only: a slow external callback must not leave
    // stale credentials or ACTIVE state queued ahead of the terminal state.
    service->notifications = xQueueCreate(1, sizeof(ServiceNotification));
    service->wifi_events = xEventGroupCreate();
    const deck_setup_mode_config_t mode_config = {
        static_cast<uint64_t>(CONFIG_DECK_SETUP_INACTIVITY_TIMEOUT_SECONDS) * 1'000U,
        fill_random,
        nullptr,
    };
    service->mode = deck_setup_mode_create(&mode_config);
    if (service->state_mutex == nullptr || service->network_mutex == nullptr ||
        service->commands == nullptr || service->notifications == nullptr ||
        service->wifi_events == nullptr || service->mode == nullptr) {
        release_unstarted(service);
        return nullptr;
    }
    if (xTaskCreatePinnedToCore(
            publisher_task,
            "setup_events",
            kPublisherTaskStackBytes,
            service,
            kPublisherTaskPriority,
            &service->publisher_task,
            0
        ) != pdPASS) {
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
    const Command command = {CommandType::enter_from_boot, {}};
    return enqueue_command(service, command);
}

bool deck_setup_service_submit_wifi(
    deck_setup_service_t *service,
    const deck_wifi_credentials_t *credentials
)
{
    if (service == nullptr || credentials == nullptr ||
        !service->accepting_commands.load(std::memory_order_acquire)) {
        return false;
    }
    deck_setup_snapshot_t setup{};
    if (xSemaphoreTake(service->state_mutex, portMAX_DELAY) != pdTRUE) {
        return false;
    }
    const bool setup_active = snapshot_locked(service, &setup) && setup.active;
    xSemaphoreGive(service->state_mutex);
    if (!setup_active) {
        return false;
    }
    const Command command = {CommandType::submit_wifi, *credentials};
    return enqueue_command(service, command);
}
