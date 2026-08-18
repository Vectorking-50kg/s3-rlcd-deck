#include "deck_setup_service.h"

#include "deck_setup_command_queue.h"
#include "deck_setup_response_barrier.h"

#include "deck_setup_http.h"
#include "deck_setup_confirmation.h"
#include "deck_device_settings_nvs.h"
#include "deck_companion_pairing_esp.h"
#include "deck_companion_profiles_nvs.h"
#include "deck_wifi_config_nvs.h"
#include "sdkconfig.h"

#include <algorithm>
#include <atomic>
#include <cstdio>
#include <cstring>
#include <memory>
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

static_assert(DECK_SETUP_PAIR_ACK_SIZE == DECK_SETUP_RESPONSE_ACK_SIZE);

constexpr EventBits_t kProfilesReadyBit = BIT4;
constexpr EventBits_t kProfilesFailedBit = BIT5;

constexpr uint8_t kSetupChannel = 1;
constexpr uint8_t kMaximumClients = 4;
constexpr size_t kMaximumScanResults = 10;
constexpr uint32_t kServiceTaskStackBytes = 6'144;
constexpr UBaseType_t kServiceTaskPriority = 2;
constexpr uint32_t kPublisherTaskStackBytes = 4'096;
constexpr UBaseType_t kPublisherTaskPriority = 1;
constexpr TickType_t kServicePollTicks = pdMS_TO_TICKS(100);
constexpr TickType_t kWifiEventTimeoutTicks = pdMS_TO_TICKS(2'000);
constexpr uint64_t kPairResponseTimeoutMs = 2'000;
constexpr size_t kErrorStageCapacity = 24;
constexpr EventBits_t kApStartedBit = BIT0;
constexpr EventBits_t kApStoppedBit = BIT1;
constexpr EventBits_t kStaGotIpBit = BIT2;
constexpr uint64_t kValidationTimeoutMs =
    static_cast<uint64_t>(CONFIG_DECK_WIFI_VALIDATION_TIMEOUT_SECONDS) * 1'000U;
constexpr uint64_t kClearConfirmationLifetimeMs = 60'000;

using Command = deck_setup_command_t;
namespace CommandType {
constexpr auto enter_from_boot = DECK_SETUP_COMMAND_ENTER_FROM_BOOT;
constexpr auto submit_wifi = DECK_SETUP_COMMAND_SUBMIT_WIFI;
constexpr auto validation_connected = DECK_SETUP_COMMAND_VALIDATION_CONNECTED;
constexpr auto validation_auth_failed = DECK_SETUP_COMMAND_VALIDATION_AUTH_FAILED;
constexpr auto validation_connection_failed =
    DECK_SETUP_COMMAND_VALIDATION_CONNECTION_FAILED;
constexpr auto submit_temperature_offset =
    DECK_SETUP_COMMAND_SUBMIT_TEMPERATURE_OFFSET;
constexpr auto clear_wifi = DECK_SETUP_COMMAND_CLEAR_WIFI;
constexpr auto pair_companion = DECK_SETUP_COMMAND_PAIR_COMPANION;
constexpr auto select_companion = DECK_SETUP_COMMAND_SELECT_COMPANION;
constexpr auto set_companion_priority = DECK_SETUP_COMMAND_SET_COMPANION_PRIORITY;
constexpr auto revoke_companion = DECK_SETUP_COMMAND_REVOKE_COMPANION;
}  // namespace CommandType

enum class EnterSessionResult : uint8_t {
    handled,
    retry_after_shutdown,
};

struct ServiceNotification {
    deck_setup_service_state_t state;
    deck_setup_snapshot_t setup;
    deck_wifi_config_snapshot_t wifi;
    deck_device_settings_snapshot_t settings;
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

bool redeem_companion(
    void *,
    const char *hub_address,
    const char *pairing_address,
    const char *pairing_code,
    deck_companion_pairing_credential_t *credential
)
{
    return deck_companion_pairing_esp_redeem(
        nullptr,
        hub_address,
        pairing_address,
        pairing_code,
        credential
    );
}

void secure_clear(char *buffer, size_t size)
{
    if (buffer == nullptr) {
        return;
    }
    volatile char *bytes = buffer;
    for (size_t index = 0; index < size; ++index) {
        bytes[index] = '\0';
    }
}

}  // namespace

struct deck_setup_service {
    deck_setup_mode_t *mode;
    deck_setup_service_event_fn callback;
    void *callback_context;
    SemaphoreHandle_t state_mutex;
    SemaphoreHandle_t network_mutex;
    deck_setup_command_queue_t *commands;
    QueueHandle_t notifications;
    TaskHandle_t publisher_task;
    TaskHandle_t service_task;
    EventGroupHandle_t wifi_events;
    esp_event_handler_instance_t wifi_event_handler;
    esp_event_handler_instance_t ip_event_handler;
    httpd_handle_t http_server;
    deck_wifi_nvs_storage_t *wifi_storage;
    deck_wifi_config_t *wifi_config;
    deck_device_settings_nvs_storage_t *settings_storage;
    deck_device_settings_t *settings;
    deck_companion_profiles_nvs_storage_t *companion_storage;
    deck_companion_profiles_t *companion_profiles;
    deck_setup_confirmation_t *clear_confirmation;
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
    deck_setup_response_barrier_t *pair_response_barrier;
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
    deck_device_settings_snapshot_t *settings,
    deck_companion_profiles_snapshot_t *companions,
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
    bool settings_copied = true;
    if (settings != nullptr) {
        *settings = {};
        settings->temperature_offset_tenths_c =
            DECK_DEVICE_SETTINGS_DEFAULT_TEMPERATURE_OFFSET_TENTHS_C;
        settings_copied = service->settings == nullptr ||
                          deck_device_settings_snapshot(service->settings, settings);
    }
    bool companions_copied = true;
    if (companions != nullptr) {
        *companions = {};
        companions_copied = service->companion_profiles == nullptr ||
                            deck_companion_profiles_snapshot(
                                service->companion_profiles,
                                companions
                            );
    }
    const bool copied = snapshot_locked(service, snapshot) && wifi_copied && settings_copied &&
                        companions_copied;
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
    deck_device_settings_snapshot_t settings{};
    size_t unused_count = 0;
    if (!copy_state(
            service,
            &snapshot,
            &wifi,
            &settings,
            nullptr,
            nullptr,
            0,
            &unused_count
        )) {
        return;
    }
    ServiceNotification notification{state, snapshot, wifi, settings, {}};
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
            notification.settings,
            notification.error_stage[0] == '\0' ? nullptr : notification.error_stage,
        };
        service->callback(service->callback_context, &event);
    }
}

bool enqueue_command(deck_setup_service_t *service, const Command &command)
{
    if (service == nullptr ||
        !deck_setup_command_queue_try_send(service->commands, &command)) {
        if (service != nullptr) {
            service->command_overflow.store(true, std::memory_order_release);
        }
        return false;
    }
    if (service->service_task != nullptr) {
        xTaskNotifyGive(service->service_task);
    }
    return true;
}

bool wait_for_pair_response(
    deck_setup_service_t *service,
    uint32_t response_generation
)
{
    const uint64_t deadline_ms = monotonic_ms() + kPairResponseTimeoutMs;
    while (!deck_setup_response_barrier_is_complete(
        service->pair_response_barrier,
        response_generation
    )) {
        const uint64_t now_ms = monotonic_ms();
        if (now_ms >= deadline_ms) {
            return false;
        }
        (void)ulTaskNotifyTake(
            pdTRUE,
            pdMS_TO_TICKS(static_cast<uint32_t>(deadline_ms - now_ms))
        );
    }
    deck_setup_response_barrier_release(
        service->pair_response_barrier,
        response_generation
    );
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
        Command command{};
        command.type = authentication_failure(disconnected->reason)
                           ? CommandType::validation_auth_failed
                           : CommandType::validation_connection_failed;
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
        Command command{};
        command.type = CommandType::validation_connected;
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

bool receive_body(
    httpd_req_t *request,
    char *body,
    size_t body_capacity,
    size_t *received
)
{
    if (request == nullptr || body == nullptr || received == nullptr ||
        request->content_len == 0 || request->content_len > body_capacity) {
        return false;
    }
    *received = 0;
    while (*received < request->content_len) {
        const int result = httpd_req_recv(
            request,
            body + *received,
            request->content_len - *received
        );
        if (result <= 0) {
            return false;
        }
        *received += static_cast<size_t>(result);
    }
    return true;
}

esp_err_t page_handler(httpd_req_t *request)
{
    auto *service = static_cast<deck_setup_service_t *>(request->user_ctx);
    if (!mark_activity(service)) {
        httpd_resp_set_status(request, "503 Service Unavailable");
        return send_json(request, "{\"error\":\"setup_inactive\"}");
    }
    const char *page = deck_setup_http_page();
    if (page == nullptr) {
        return httpd_resp_send_500(request);
    }
    httpd_resp_set_type(request, "text/html; charset=utf-8");
    httpd_resp_set_hdr(request, "Cache-Control", "no-store");
    return httpd_resp_send(request, page, HTTPD_RESP_USE_STRLEN);
}

esp_err_t legacy_pairing_page_handler(httpd_req_t *request)
{
    auto *service = static_cast<deck_setup_service_t *>(request->user_ctx);
    if (!mark_activity(service)) {
        httpd_resp_set_status(request, "503 Service Unavailable");
        return send_json(request, "{\"error\":\"setup_inactive\"}");
    }
    const char *page = deck_setup_http_legacy_pairing_page();
    if (page == nullptr) {
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
    deck_device_settings_snapshot_t settings{};
    deck_companion_profiles_snapshot_t companions{};
    deck_setup_scan_result_t networks[kMaximumScanResults]{};
    size_t network_count = 0;
    if (!copy_state(
            service,
            &snapshot,
            &wifi,
            &settings,
            &companions,
            networks,
            kMaximumScanResults,
            &network_count
        )) {
        return httpd_resp_send_500(request);
    }
    constexpr size_t kStatusResponseCapacity = 8'192;
    const std::unique_ptr<char[]> response(
        new (std::nothrow) char[kStatusResponseCapacity]
    );
    if (response == nullptr) {
        return httpd_resp_send_500(request);
    }
    if (!deck_setup_http_render_status(
            &snapshot,
            &wifi,
            &settings,
            &companions,
            networks,
            network_count,
            response.get(),
            kStatusResponseCapacity
        )) {
        return httpd_resp_send_500(request);
    }
    return send_json(request, response.get());
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
    char body[kMaximumBodySize];
    size_t received = 0;
    if (!receive_body(request, body, sizeof(body), &received)) {
        secure_clear(body, sizeof(body));
        httpd_resp_set_status(request, "400 Bad Request");
        return send_json(request, "{\"accepted\":false,\"error\":\"malformed\"}");
    }

    Command command{};
    command.type = CommandType::submit_wifi;
    const deck_setup_wifi_request_result_t parsed = deck_setup_http_parse_wifi_request(
        body,
        received,
        &command.credentials
    );
    if (parsed != DECK_SETUP_WIFI_REQUEST_OK) {
        deck_wifi_credentials_clear(&command.credentials);
        secure_clear(body, sizeof(body));
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
    secure_clear(body, sizeof(body));
    if (!queued) {
        httpd_resp_set_status(request, "503 Service Unavailable");
        return send_json(request, "{\"accepted\":false,\"error\":\"busy\"}");
    }
    httpd_resp_set_status(request, "202 Accepted");
    return send_json(request, "{\"accepted\":true,\"state\":\"queued\"}");
}

esp_err_t temperature_handler(httpd_req_t *request)
{
    auto *service = static_cast<deck_setup_service_t *>(request->user_ctx);
    if (!mark_activity(service)) {
        httpd_resp_set_status(request, "503 Service Unavailable");
        return send_json(request, "{\"accepted\":false,\"error\":\"setup_inactive\"}");
    }
    char body[64];
    size_t received = 0;
    if (!receive_body(request, body, sizeof(body), &received)) {
        httpd_resp_set_status(request, "400 Bad Request");
        return send_json(request, "{\"accepted\":false,\"error\":\"malformed\"}");
    }
    int16_t offset = 0;
    const deck_setup_temperature_request_result_t parsed =
        deck_setup_http_parse_temperature_request(body, received, &offset);
    if (parsed != DECK_SETUP_TEMPERATURE_REQUEST_OK) {
        httpd_resp_set_status(request, "400 Bad Request");
        switch (parsed) {
            case DECK_SETUP_TEMPERATURE_REQUEST_NOT_NUMERIC:
                return send_json(request, "{\"accepted\":false,\"error\":\"not_numeric\"}");
            case DECK_SETUP_TEMPERATURE_REQUEST_OUT_OF_RANGE:
                return send_json(request, "{\"accepted\":false,\"error\":\"out_of_range\"}");
            case DECK_SETUP_TEMPERATURE_REQUEST_NOT_EXACT_TENTH:
                return send_json(request, "{\"accepted\":false,\"error\":\"not_exact_tenth\"}");
            case DECK_SETUP_TEMPERATURE_REQUEST_MALFORMED:
            case DECK_SETUP_TEMPERATURE_REQUEST_OK:
            default:
                return send_json(request, "{\"accepted\":false,\"error\":\"malformed\"}");
        }
    }
    if (!deck_setup_service_submit_temperature_offset(service, offset)) {
        httpd_resp_set_status(request, "503 Service Unavailable");
        return send_json(request, "{\"accepted\":false,\"error\":\"busy\"}");
    }
    httpd_resp_set_status(request, "202 Accepted");
    return send_json(request, "{\"accepted\":true,\"state\":\"queued\"}");
}

esp_err_t wifi_clear_request_handler(httpd_req_t *request)
{
    auto *service = static_cast<deck_setup_service_t *>(request->user_ctx);
    if (!mark_activity(service)) {
        httpd_resp_set_status(request, "503 Service Unavailable");
        return send_json(request, "{\"accepted\":false,\"error\":\"setup_inactive\"}");
    }
    char token[DECK_SETUP_CONFIRMATION_TOKEN_CAPACITY];
    if (!deck_setup_service_request_wifi_clear(service, token, sizeof(token))) {
        httpd_resp_set_status(request, "503 Service Unavailable");
        return send_json(request, "{\"accepted\":false,\"error\":\"confirmation_unavailable\"}");
    }
    char response[96];
    const int size = snprintf(
        response,
        sizeof(response),
        "{\"accepted\":true,\"token\":\"%s\",\"expires_in_ms\":%u}",
        token,
        static_cast<unsigned>(kClearConfirmationLifetimeMs)
    );
    if (size < 0 || static_cast<size_t>(size) >= sizeof(response)) {
        return httpd_resp_send_500(request);
    }
    const esp_err_t send_result = send_json(request, response);
    secure_clear(token, sizeof(token));
    secure_clear(response, sizeof(response));
    return send_result;
}

esp_err_t wifi_clear_confirm_handler(httpd_req_t *request)
{
    auto *service = static_cast<deck_setup_service_t *>(request->user_ctx);
    if (!mark_activity(service)) {
        httpd_resp_set_status(request, "503 Service Unavailable");
        return send_json(request, "{\"accepted\":false,\"error\":\"setup_inactive\"}");
    }
    char body[64];
    size_t received = 0;
    char token[DECK_SETUP_CONFIRMATION_TOKEN_CAPACITY];
    if (!receive_body(request, body, sizeof(body), &received) ||
        !deck_setup_http_parse_confirmation_request(
            body,
            received,
            token,
            sizeof(token)
        )) {
        secure_clear(token, sizeof(token));
        secure_clear(body, sizeof(body));
        httpd_resp_set_status(request, "400 Bad Request");
        return send_json(request, "{\"accepted\":false,\"error\":\"malformed\"}");
    }
    const deck_setup_wifi_clear_confirm_result_t result =
        deck_setup_service_confirm_wifi_clear(service, token);
    secure_clear(token, sizeof(token));
    secure_clear(body, sizeof(body));
    switch (result) {
        case DECK_SETUP_WIFI_CLEAR_QUEUED:
            httpd_resp_set_status(request, "202 Accepted");
            return send_json(request, "{\"accepted\":true,\"state\":\"queued\"}");
        case DECK_SETUP_WIFI_CLEAR_INACTIVE:
            httpd_resp_set_status(request, "503 Service Unavailable");
            return send_json(request, "{\"accepted\":false,\"error\":\"setup_inactive\"}");
        case DECK_SETUP_WIFI_CLEAR_NOT_ISSUED:
            httpd_resp_set_status(request, "409 Conflict");
            return send_json(request, "{\"accepted\":false,\"error\":\"not_issued\"}");
        case DECK_SETUP_WIFI_CLEAR_MISMATCH:
            httpd_resp_set_status(request, "403 Forbidden");
            return send_json(request, "{\"accepted\":false,\"error\":\"confirmation_mismatch\"}");
        case DECK_SETUP_WIFI_CLEAR_EXPIRED:
            httpd_resp_set_status(request, "410 Gone");
            return send_json(request, "{\"accepted\":false,\"error\":\"confirmation_expired\"}");
        case DECK_SETUP_WIFI_CLEAR_BUSY:
        default:
            httpd_resp_set_status(request, "503 Service Unavailable");
            return send_json(request, "{\"accepted\":false,\"error\":\"busy\"}");
    }
}

bool extract_ipv4_address(
    const sockaddr_storage &socket_address,
    uint8_t ipv4[4]
);

esp_err_t companion_pair_handler(httpd_req_t *request)
{
    auto *service = static_cast<deck_setup_service_t *>(request->user_ctx);
    if (!mark_activity(service)) {
        httpd_resp_set_status(request, "503 Service Unavailable");
        return send_json(request, "{\"accepted\":false,\"error\":\"setup_inactive\"}");
    }
    char body[160];
    size_t received = 0;
    deck_companion_pair_request_t pair_request{};
    if (!receive_body(request, body, sizeof(body), &received)) {
        secure_clear(body, sizeof(body));
        httpd_resp_set_status(request, "400 Bad Request");
        return send_json(request, "{\"accepted\":false,\"error\":\"malformed\"}");
    }
    const deck_setup_companion_request_result_t parsed =
        deck_setup_http_parse_companion_pair_request(
            body,
            received,
            &pair_request
        );
    secure_clear(body, sizeof(body));
    if (parsed != DECK_SETUP_COMPANION_REQUEST_OK) {
        secure_clear(
            reinterpret_cast<char *>(&pair_request),
            sizeof(pair_request)
        );
        httpd_resp_set_status(request, "400 Bad Request");
        if (parsed == DECK_SETUP_COMPANION_REQUEST_INVALID_ADDRESS) {
            return send_json(request, "{\"accepted\":false,\"error\":\"invalid_address\"}");
        }
        if (parsed == DECK_SETUP_COMPANION_REQUEST_INVALID_CODE) {
            return send_json(request, "{\"accepted\":false,\"error\":\"invalid_code\"}");
        }
        return send_json(request, "{\"accepted\":false,\"error\":\"malformed\"}");
    }
    sockaddr_storage peer_address{};
    socklen_t peer_size = sizeof(peer_address);
    const int socket_fd = httpd_req_to_sockfd(request);
    uint16_t hub_port = 0;
    char peer_ip[INET_ADDRSTRLEN]{};
    uint8_t peer_ipv4[4]{};
    const bool peer_valid =
        socket_fd >= 0 &&
        getpeername(
            socket_fd,
            reinterpret_cast<sockaddr *>(&peer_address),
            &peer_size
        ) == 0 &&
        extract_ipv4_address(peer_address, peer_ipv4) &&
        inet_ntop(
            AF_INET,
            peer_ipv4,
            peer_ip,
            sizeof(peer_ip)
        ) != nullptr &&
        deck_companion_hub_address_port(pair_request.hub_address, &hub_port);
    const int pairing_size = peer_valid
                                 ? std::snprintf(
                                       pair_request.pairing_address,
                                       sizeof(pair_request.pairing_address),
                                       "%s:%u",
                                       peer_ip,
                                       static_cast<unsigned>(hub_port)
                                   )
                                 : -1;
    if (pairing_size <= 0 || static_cast<size_t>(pairing_size) >=
                                 sizeof(pair_request.pairing_address)) {
        secure_clear(
            reinterpret_cast<char *>(&pair_request),
            sizeof(pair_request)
        );
        httpd_resp_set_status(request, "400 Bad Request");
        return send_json(request, "{\"accepted\":false,\"error\":\"invalid_peer\"}");
    }
    Command command{};
    command.type = CommandType::pair_companion;
    command.companion_pair = pair_request;
    uint8_t response_ack[DECK_SETUP_RESPONSE_ACK_SIZE];
    fill_random(nullptr, response_ack, sizeof(response_ack));
    uint32_t peer_id = 0;
    std::memcpy(&peer_id, peer_ipv4, sizeof(peer_id));
    command.response_generation = deck_setup_response_barrier_issue(
        service->pair_response_barrier,
        peer_id,
        response_ack
    );
    const bool queued = command.response_generation != 0 &&
                        enqueue_command(service, command);
    if (!queued && command.response_generation != 0) {
        deck_setup_response_barrier_release(
            service->pair_response_barrier,
            command.response_generation
        );
    }
    secure_clear(reinterpret_cast<char *>(&pair_request), sizeof(pair_request));
    secure_clear(
        reinterpret_cast<char *>(&command.companion_pair),
        sizeof(command.companion_pair)
    );
    if (!queued) {
        secure_clear(reinterpret_cast<char *>(response_ack), sizeof(response_ack));
        httpd_resp_set_status(request, "503 Service Unavailable");
        return send_json(request, "{\"accepted\":false,\"error\":\"busy\"}");
    }
    httpd_resp_set_status(request, "202 Accepted");
    char response[96] = "{\"accepted\":true,\"state\":\"queued\",\"response_ack\":\"";
    constexpr char kHex[] = "0123456789abcdef";
    size_t response_size = std::strlen(response);
    for (size_t index = 0; index < sizeof(response_ack); ++index) {
        response[response_size++] = kHex[response_ack[index] >> 4U];
        response[response_size++] = kHex[response_ack[index] & 0x0fU];
    }
    response[response_size++] = '"';
    response[response_size++] = '}';
    response[response_size] = '\0';
    secure_clear(reinterpret_cast<char *>(response_ack), sizeof(response_ack));
    const esp_err_t result = send_json(request, response);
    secure_clear(response, sizeof(response));
    return result;
}

esp_err_t companion_pair_ack_handler(httpd_req_t *request)
{
    auto *service = static_cast<deck_setup_service_t *>(request->user_ctx);
    if (!mark_activity(service)) {
        httpd_resp_set_status(request, "503 Service Unavailable");
        return send_json(request, "{\"accepted\":false,\"error\":\"setup_inactive\"}");
    }
    char body[48];
    size_t received = 0;
    uint8_t response_ack[DECK_SETUP_RESPONSE_ACK_SIZE]{};
    if (!receive_body(request, body, sizeof(body), &received) ||
        !deck_setup_http_parse_pair_ack_request(body, received, response_ack)) {
        secure_clear(body, sizeof(body));
        secure_clear(reinterpret_cast<char *>(response_ack), sizeof(response_ack));
        httpd_resp_set_status(request, "400 Bad Request");
        return send_json(request, "{\"accepted\":false,\"error\":\"malformed\"}");
    }
    secure_clear(body, sizeof(body));
    sockaddr_storage peer_address{};
    socklen_t peer_size = sizeof(peer_address);
    const int socket_fd = httpd_req_to_sockfd(request);
    uint8_t peer_ipv4[4]{};
    if (socket_fd < 0 ||
        getpeername(socket_fd, reinterpret_cast<sockaddr *>(&peer_address), &peer_size) != 0 ||
        !extract_ipv4_address(peer_address, peer_ipv4)) {
        secure_clear(reinterpret_cast<char *>(response_ack), sizeof(response_ack));
        httpd_resp_set_status(request, "400 Bad Request");
        return send_json(request, "{\"accepted\":false,\"error\":\"invalid_peer\"}");
    }
    uint32_t peer_id = 0;
    std::memcpy(&peer_id, peer_ipv4, sizeof(peer_id));
    if (!deck_setup_response_barrier_acknowledge(
            service->pair_response_barrier,
            peer_id,
            response_ack
        )) {
        secure_clear(reinterpret_cast<char *>(response_ack), sizeof(response_ack));
        httpd_resp_set_status(request, "409 Conflict");
        return send_json(request, "{\"accepted\":false,\"error\":\"not_pending\"}");
    }
    secure_clear(reinterpret_cast<char *>(response_ack), sizeof(response_ack));
    if (service->service_task != nullptr) {
        xTaskNotifyGive(service->service_task);
    }
    httpd_resp_set_status(request, "202 Accepted");
    return send_json(request, "{\"accepted\":true,\"state\":\"acknowledged\"}");
}

esp_err_t companion_profile_handler(httpd_req_t *request, bool revoke)
{
    auto *service = static_cast<deck_setup_service_t *>(request->user_ctx);
    if (!mark_activity(service)) {
        httpd_resp_set_status(request, "503 Service Unavailable");
        return send_json(request, "{\"accepted\":false,\"error\":\"setup_inactive\"}");
    }
    char body[128];
    char profile_id[DECK_COMPANION_PROFILE_ID_CAPACITY];
    size_t received = 0;
    if (!receive_body(request, body, sizeof(body), &received) ||
        !deck_setup_http_parse_companion_profile_request(
            body,
            received,
            profile_id,
            sizeof(profile_id)
        )) {
        secure_clear(body, sizeof(body));
        secure_clear(profile_id, sizeof(profile_id));
        httpd_resp_set_status(request, "400 Bad Request");
        return send_json(request, "{\"accepted\":false,\"error\":\"malformed\"}");
    }
    const bool queued = revoke
                            ? deck_setup_service_revoke_companion(service, profile_id)
                            : deck_setup_service_select_companion(service, profile_id);
    secure_clear(body, sizeof(body));
    secure_clear(profile_id, sizeof(profile_id));
    if (!queued) {
        httpd_resp_set_status(request, "503 Service Unavailable");
        return send_json(request, "{\"accepted\":false,\"error\":\"busy\"}");
    }
    httpd_resp_set_status(request, "202 Accepted");
    return send_json(request, "{\"accepted\":true,\"state\":\"queued\"}");
}

esp_err_t companion_select_handler(httpd_req_t *request)
{
    return companion_profile_handler(request, false);
}

esp_err_t companion_revoke_handler(httpd_req_t *request)
{
    return companion_profile_handler(request, true);
}

bool extract_ipv4_address(
    const sockaddr_storage &socket_address,
    uint8_t ipv4[4]
)
{
    const uint8_t *address = nullptr;
    size_t address_size = 0;
    if (socket_address.ss_family == AF_INET) {
        const auto *ipv4_address = reinterpret_cast<const sockaddr_in *>(&socket_address);
        address = reinterpret_cast<const uint8_t *>(&ipv4_address->sin_addr.s_addr);
        address_size = sizeof(ipv4_address->sin_addr.s_addr);
    } else if (socket_address.ss_family == AF_INET6) {
        const auto *ipv6_address = reinterpret_cast<const sockaddr_in6 *>(&socket_address);
        address = reinterpret_cast<const uint8_t *>(&ipv6_address->sin6_addr);
        address_size = sizeof(ipv6_address->sin6_addr);
    }
    return deck_setup_http_extract_ipv4(address, address_size, ipv4);
}

esp_err_t companion_priority_handler(httpd_req_t *request)
{
    auto *service = static_cast<deck_setup_service_t *>(request->user_ctx);
    if (!mark_activity(service)) {
        httpd_resp_set_status(request, "503 Service Unavailable");
        return send_json(request, "{\"accepted\":false,\"error\":\"setup_inactive\"}");
    }
    char body[192]{};
    char profile_id[DECK_COMPANION_PROFILE_ID_CAPACITY]{};
    int32_t priority = 0;
    size_t received = 0;
    const bool parsed = receive_body(request, body, sizeof(body), &received) &&
                        deck_setup_http_parse_companion_priority_request(
                            body,
                            received,
                            profile_id,
                            sizeof(profile_id),
                            &priority
                        );
    secure_clear(body, sizeof(body));
    if (!parsed) {
        secure_clear(profile_id, sizeof(profile_id));
        httpd_resp_set_status(request, "400 Bad Request");
        return send_json(request, "{\"accepted\":false,\"error\":\"malformed\"}");
    }
    const bool queued = deck_setup_service_set_companion_priority(
        service,
        profile_id,
        priority
    );
    secure_clear(profile_id, sizeof(profile_id));
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
        ) != 0) {
        return ESP_FAIL;
    }
    uint8_t local_ipv4[4]{};
    return extract_ipv4_address(local_address, local_ipv4) &&
                   deck_setup_http_address_is_setup_gateway(local_ipv4, sizeof(local_ipv4))
               ? ESP_OK : ESP_FAIL;
}

using HttpHandler = esp_err_t (*)(httpd_req_t *);

HttpHandler handler_for_route(deck_setup_http_route_t route)
{
    switch (route) {
        case DECK_SETUP_HTTP_PAGE:
            return page_handler;
        case DECK_SETUP_HTTP_LEGACY_PAIRING_PAGE:
            return legacy_pairing_page_handler;
        case DECK_SETUP_HTTP_STATUS:
            return status_handler;
        case DECK_SETUP_HTTP_SCAN:
            return scan_handler;
        case DECK_SETUP_HTTP_WIFI:
            return wifi_handler;
        case DECK_SETUP_HTTP_TEMPERATURE:
            return temperature_handler;
        case DECK_SETUP_HTTP_WIFI_CLEAR_REQUEST:
            return wifi_clear_request_handler;
        case DECK_SETUP_HTTP_WIFI_CLEAR_CONFIRM:
            return wifi_clear_confirm_handler;
        case DECK_SETUP_HTTP_COMPANION_PAIR:
            return companion_pair_handler;
        case DECK_SETUP_HTTP_COMPANION_PAIR_ACK:
            return companion_pair_ack_handler;
        case DECK_SETUP_HTTP_COMPANION_SELECT:
            return companion_select_handler;
        case DECK_SETUP_HTTP_COMPANION_PRIORITY:
            return companion_priority_handler;
        case DECK_SETUP_HTTP_COMPANION_REVOKE:
            return companion_revoke_handler;
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
    config.stack_size = 12'288;
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
    service->suppress_disconnect.store(true, std::memory_order_release);
    if (esp_wifi_disconnect() != ESP_OK) {
        service->suppress_disconnect.store(false, std::memory_order_release);
    }
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
    if (nvs_flash_init_partition("companion_nvs") != ESP_OK) {
        return "companion_nvs_init";
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
    service->settings_storage = deck_device_settings_nvs_storage_open();
    deck_device_settings_storage_adapter_t settings_adapter{};
    if (service->settings_storage == nullptr ||
        !deck_device_settings_nvs_storage_adapter(
            service->settings_storage,
            &settings_adapter
        )) {
        return "settings_store";
    }
    const deck_device_settings_options_t settings_options = {settings_adapter};
    service->settings = deck_device_settings_create(&settings_options);
    if (service->settings == nullptr) {
        return "settings_config";
    }
    service->companion_storage = deck_companion_profiles_nvs_storage_open();
    deck_companion_storage_adapter_t companion_storage_adapter{};
    if (service->companion_storage == nullptr ||
        !deck_companion_profiles_nvs_storage_adapter(
            service->companion_storage,
            &companion_storage_adapter
        )) {
        return "companion_store";
    }
    const deck_companion_profiles_options_t companion_options = {
        companion_storage_adapter,
        {redeem_companion, service},
    };
    service->companion_profiles = deck_companion_profiles_create(&companion_options);
    if (service->companion_profiles == nullptr) {
        return "companion_profiles";
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
        xEventGroupSetBits(service->wifi_events, kProfilesFailedBit);
        service->accepting_commands.store(false, std::memory_order_release);
        fail_session(service, initialization_error);
        vTaskDelete(nullptr);
        return;
    }
    xEventGroupSetBits(service->wifi_events, kProfilesReadyBit);
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
        bool received = deck_setup_command_queue_try_receive(
            service->commands,
            &command
        );
        if (!received) {
            (void)ulTaskNotifyTake(pdTRUE, kServicePollTicks);
            received = deck_setup_command_queue_try_receive(
                service->commands,
                &command
            );
        }
        if (received) {
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
                case CommandType::submit_temperature_offset: {
                    deck_device_settings_update_result_t result =
                        DECK_DEVICE_SETTINGS_STORAGE_FAILURE;
                    bool setup_active = false;
                    if (xSemaphoreTake(service->state_mutex, portMAX_DELAY) == pdTRUE) {
                        deck_setup_snapshot_t setup{};
                        setup_active = snapshot_locked(service, &setup) && setup.active;
                        if (setup_active) {
                            result = deck_device_settings_submit_temperature_offset(
                                service->settings,
                                command.temperature_offset_tenths_c
                            );
                        }
                        xSemaphoreGive(service->state_mutex);
                    }
                    notify(
                        service,
                        result == DECK_DEVICE_SETTINGS_STORAGE_FAILURE
                            ? DECK_SETUP_SERVICE_ERROR
                            : setup_active ? DECK_SETUP_SERVICE_ACTIVE
                                           : DECK_SETUP_SERVICE_INACTIVE,
                        result == DECK_DEVICE_SETTINGS_STORAGE_FAILURE
                            ? "temperature_commit"
                            : nullptr
                    );
                    break;
                }
                case CommandType::clear_wifi: {
                    deck_wifi_clear_result_t result = DECK_WIFI_CLEAR_STORAGE_ERROR;
                    bool setup_active = false;
                    if (xSemaphoreTake(service->state_mutex, portMAX_DELAY) == pdTRUE) {
                        deck_setup_snapshot_t setup{};
                        setup_active = snapshot_locked(service, &setup) && setup.active;
                        if (setup_active) {
                            result = deck_wifi_config_clear(service->wifi_config);
                        }
                        xSemaphoreGive(service->state_mutex);
                    }
                    if (result == DECK_WIFI_CLEAR_CLEARED) {
                        service->active_station_restore_pending = false;
                        cancel_wifi_validation(service);
                    }
                    notify(
                        service,
                        result == DECK_WIFI_CLEAR_STORAGE_ERROR
                            ? DECK_SETUP_SERVICE_ERROR
                            : setup_active ? DECK_SETUP_SERVICE_ACTIVE
                                           : DECK_SETUP_SERVICE_INACTIVE,
                        result == DECK_WIFI_CLEAR_STORAGE_ERROR ? "wifi_clear" : nullptr
                    );
                    break;
                }
                case CommandType::pair_companion: {
                    deck_companion_pair_result_t result =
                        DECK_COMPANION_PAIR_STORAGE_FAILURE;
                    bool setup_active = false;
                    if (xSemaphoreTake(service->state_mutex, portMAX_DELAY) == pdTRUE) {
                        deck_setup_snapshot_t setup{};
                        setup_active = snapshot_locked(service, &setup) && setup.active;
                        xSemaphoreGive(service->state_mutex);
                    }
                    if (setup_active) {
                        result = deck_companion_profiles_pair(
                            service->companion_profiles,
                            &command.companion_pair
                        );
                    }
                    secure_clear(
                        reinterpret_cast<char *>(&command.companion_pair),
                        sizeof(command.companion_pair)
                    );
                    if (result == DECK_COMPANION_PAIR_PAIRED) {
                        const bool response_complete = wait_for_pair_response(
                            service,
                            command.response_generation
                        );
                        if (!response_complete) {
                            deck_setup_response_barrier_release(
                                service->pair_response_barrier,
                                command.response_generation
                            );
                            notify(service, DECK_SETUP_SERVICE_ERROR, "pair_response");
                            break;
                        }
                        if (xSemaphoreTake(service->state_mutex, portMAX_DELAY) == pdTRUE) {
                            (void)deck_setup_mode_stop(service->mode);
                            xSemaphoreGive(service->state_mutex);
                        }
                        const char *stop_error = stop_network(service);
                        notify(
                            service,
                            stop_error == nullptr ? DECK_SETUP_SERVICE_INACTIVE
                                                  : DECK_SETUP_SERVICE_ERROR,
                            stop_error
                        );
                        break;
                    }
                    deck_setup_response_barrier_release(
                        service->pair_response_barrier,
                        command.response_generation
                    );
                    const bool storage_failure =
                        result == DECK_COMPANION_PAIR_STORAGE_FAILURE;
                    notify(
                        service,
                        storage_failure ? DECK_SETUP_SERVICE_ERROR
                                        : setup_active ? DECK_SETUP_SERVICE_ACTIVE
                                                       : DECK_SETUP_SERVICE_INACTIVE,
                        storage_failure ? "companion_commit" : nullptr
                    );
                    break;
                }
                case CommandType::select_companion:
                case CommandType::set_companion_priority:
                case CommandType::revoke_companion: {
                    deck_companion_profile_update_result_t result =
                        DECK_COMPANION_PROFILE_STORAGE_FAILURE;
                    bool setup_active = false;
                    if (xSemaphoreTake(service->state_mutex, portMAX_DELAY) == pdTRUE) {
                        deck_setup_snapshot_t setup{};
                        setup_active = snapshot_locked(service, &setup) && setup.active;
                        if (setup_active) {
                            if (command.type == CommandType::select_companion) {
                                result = deck_companion_profiles_select_active(
                                    service->companion_profiles,
                                    command.companion_profile_id
                                );
                            } else if (command.type ==
                                       CommandType::set_companion_priority) {
                                result = deck_companion_profiles_set_priority(
                                    service->companion_profiles,
                                    command.companion_profile_id,
                                    command.companion_priority
                                );
                            } else {
                                result = deck_companion_profiles_revoke(
                                    service->companion_profiles,
                                    command.companion_profile_id
                                );
                            }
                        }
                        xSemaphoreGive(service->state_mutex);
                    }
                    secure_clear(
                        command.companion_profile_id,
                        sizeof(command.companion_profile_id)
                    );
                    const bool storage_failure =
                        result == DECK_COMPANION_PROFILE_STORAGE_FAILURE;
                    notify(
                        service,
                        storage_failure ? DECK_SETUP_SERVICE_ERROR
                                        : setup_active ? DECK_SETUP_SERVICE_ACTIVE
                                                       : DECK_SETUP_SERVICE_INACTIVE,
                        storage_failure ? "companion_commit" : nullptr
                    );
                    break;
                }
            }
            deck_setup_command_clear(&command);
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
    deck_device_settings_destroy(service->settings);
    deck_device_settings_nvs_storage_close(service->settings_storage);
    deck_companion_profiles_destroy(service->companion_profiles);
    deck_companion_profiles_nvs_storage_close(service->companion_storage);
    deck_setup_confirmation_destroy(service->clear_confirmation);
    if (service->publisher_task != nullptr) {
        vTaskDelete(service->publisher_task);
    }
    if (service->commands != nullptr) {
        deck_setup_command_queue_destroy(service->commands);
    }
    deck_setup_response_barrier_destroy(service->pair_response_barrier);
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
    service->commands = deck_setup_command_queue_create();
    service->pair_response_barrier = deck_setup_response_barrier_create(
        DECK_SETUP_COMMAND_QUEUE_CAPACITY
    );
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
    const deck_setup_confirmation_options_t confirmation_options = {
        kClearConfirmationLifetimeMs,
        fill_random,
        nullptr,
    };
    service->clear_confirmation = deck_setup_confirmation_create(
        &confirmation_options
    );
    if (service->state_mutex == nullptr || service->network_mutex == nullptr ||
        service->commands == nullptr || service->pair_response_barrier == nullptr ||
        service->notifications == nullptr ||
        service->wifi_events == nullptr || service->mode == nullptr ||
        service->clear_confirmation == nullptr) {
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
            &service->service_task,
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
    Command command{};
    command.type = CommandType::enter_from_boot;
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
    Command command{};
    command.type = CommandType::submit_wifi;
    command.credentials = *credentials;
    const bool queued = enqueue_command(service, command);
    deck_wifi_credentials_clear(&command.credentials);
    return queued;
}

bool deck_setup_service_submit_temperature_offset(
    deck_setup_service_t *service,
    int16_t temperature_offset_tenths_c
)
{
    if (service == nullptr ||
        temperature_offset_tenths_c <
            DECK_DEVICE_SETTINGS_MINIMUM_TEMPERATURE_OFFSET_TENTHS_C ||
        temperature_offset_tenths_c >
            DECK_DEVICE_SETTINGS_MAXIMUM_TEMPERATURE_OFFSET_TENTHS_C ||
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
    Command command{};
    command.type = CommandType::submit_temperature_offset;
    command.temperature_offset_tenths_c = temperature_offset_tenths_c;
    return enqueue_command(service, command);
}

namespace {

bool queue_companion_profile_command(
    deck_setup_service_t *service,
    const char *profile_id,
    deck_setup_command_type_t type
)
{
    if (service == nullptr || profile_id == nullptr ||
        strnlen(profile_id, DECK_COMPANION_PROFILE_ID_CAPACITY) != 71 ||
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
    Command command{};
    command.type = type;
    std::memcpy(command.companion_profile_id, profile_id, 72);
    const bool queued = enqueue_command(service, command);
    secure_clear(
        command.companion_profile_id,
        sizeof(command.companion_profile_id)
    );
    return queued;
}

}  // namespace

bool deck_setup_service_select_companion(
    deck_setup_service_t *service,
    const char *profile_id
)
{
    return queue_companion_profile_command(
        service,
        profile_id,
        CommandType::select_companion
    );
}

bool deck_setup_service_revoke_companion(
    deck_setup_service_t *service,
    const char *profile_id
)
{
    return queue_companion_profile_command(
        service,
        profile_id,
        CommandType::revoke_companion
    );
}

bool deck_setup_service_set_companion_priority(
    deck_setup_service_t *service,
    const char *profile_id,
    int32_t priority
)
{
    if (service == nullptr || profile_id == nullptr ||
        strnlen(profile_id, DECK_COMPANION_PROFILE_ID_CAPACITY) != 71 ||
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
    Command command{};
    command.type = CommandType::set_companion_priority;
    command.companion_priority = priority;
    std::memcpy(command.companion_profile_id, profile_id, 72);
    const bool queued = enqueue_command(service, command);
    secure_clear(
        command.companion_profile_id,
        sizeof(command.companion_profile_id)
    );
    return queued;
}

deck_companion_profiles_t *deck_setup_service_wait_companion_profiles(
    deck_setup_service_t *service,
    uint32_t timeout_ms
)
{
    if (service == nullptr || timeout_ms == 0) {
        return nullptr;
    }
    const EventBits_t bits = xEventGroupWaitBits(
        service->wifi_events,
        kProfilesReadyBit | kProfilesFailedBit,
        pdFALSE,
        pdFALSE,
        pdMS_TO_TICKS(timeout_ms)
    );
    return (bits & kProfilesReadyBit) != 0 ? service->companion_profiles : nullptr;
}

bool deck_setup_service_request_wifi_clear(
    deck_setup_service_t *service,
    char *token,
    size_t token_capacity
)
{
    if (service == nullptr || token == nullptr ||
        !service->accepting_commands.load(std::memory_order_acquire) ||
        xSemaphoreTake(service->state_mutex, portMAX_DELAY) != pdTRUE) {
        return false;
    }
    deck_setup_snapshot_t setup{};
    const bool active = snapshot_locked(service, &setup) && setup.active;
    const bool issued = active && deck_setup_confirmation_issue(
                                      service->clear_confirmation,
                                      setup.session_id,
                                      monotonic_ms(),
                                      token,
                                      token_capacity
                                  );
    xSemaphoreGive(service->state_mutex);
    return issued;
}

deck_setup_wifi_clear_confirm_result_t deck_setup_service_confirm_wifi_clear(
    deck_setup_service_t *service,
    const char *token
)
{
    if (service == nullptr || token == nullptr ||
        !service->accepting_commands.load(std::memory_order_acquire) ||
        xSemaphoreTake(service->state_mutex, portMAX_DELAY) != pdTRUE) {
        return DECK_SETUP_WIFI_CLEAR_INACTIVE;
    }
    deck_setup_snapshot_t setup{};
    if (!snapshot_locked(service, &setup) || !setup.active) {
        xSemaphoreGive(service->state_mutex);
        return DECK_SETUP_WIFI_CLEAR_INACTIVE;
    }
    const deck_setup_confirmation_result_t confirmation =
        deck_setup_confirmation_consume(
            service->clear_confirmation,
            setup.session_id,
            token,
            monotonic_ms()
        );
    xSemaphoreGive(service->state_mutex);
    switch (confirmation) {
        case DECK_SETUP_CONFIRMATION_NOT_ISSUED:
            return DECK_SETUP_WIFI_CLEAR_NOT_ISSUED;
        case DECK_SETUP_CONFIRMATION_MISMATCH:
            return DECK_SETUP_WIFI_CLEAR_MISMATCH;
        case DECK_SETUP_CONFIRMATION_EXPIRED:
            return DECK_SETUP_WIFI_CLEAR_EXPIRED;
        case DECK_SETUP_CONFIRMATION_CONFIRMED:
        default:
            break;
    }
    Command command{};
    command.type = CommandType::clear_wifi;
    return enqueue_command(service, command) ? DECK_SETUP_WIFI_CLEAR_QUEUED
                                             : DECK_SETUP_WIFI_CLEAR_BUSY;
}
