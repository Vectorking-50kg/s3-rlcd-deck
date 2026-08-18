#include "deck_lan_pairing.h"
#include "deck_device_protocol.h"
#include "deck_pairing_v2_transaction.h"

#include <atomic>
#include <climits>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <inttypes.h>
#include <new>
#include <mutex>
#include <ctime>

#include "esp_srp.h"
#include "esp_mac.h"
#include "esp_random.h"
#include "esp_timer.h"
#include "esp_websocket_client.h"
#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"
#include "freertos/queue.h"
#include "freertos/task.h"
#include "mdns.h"
#include "protocomm.h"
#include "protocomm_httpd.h"
#include "protocomm_security2.h"
#include "psa/crypto.h"

namespace {

uint64_t monotonic_ms()
{
    return static_cast<uint64_t>(esp_timer_get_time() / 1'000);
}

void encode_hex(const uint8_t *input, size_t size, char *output)
{
    static constexpr char hexadecimal[] = "0123456789abcdef";
    for (size_t index = 0; index < size; ++index) {
        output[index * 2U] = hexadecimal[input[index] >> 4U];
        output[index * 2U + 1U] = hexadecimal[input[index] & 0x0fU];
    }
    output[size * 2U] = '\0';
}

bool pairing_sha256(
    void *,
    const uint8_t *input,
    size_t input_size,
    uint8_t output[32]
)
{
    size_t output_size = 0;
    return input != nullptr && output != nullptr && psa_crypto_init() == PSA_SUCCESS &&
           psa_hash_compute(
               PSA_ALG_SHA_256,
               input,
               input_size,
               output,
               32,
               &output_size
           ) == PSA_SUCCESS &&
           output_size == 32;
}

bool pairing_random(void *, uint8_t *output, size_t size)
{
    if (output == nullptr || size == 0) {
        return false;
    }
    esp_fill_random(output, size);
    return true;
}

bool pairing_identity(
    void *,
    char *device_id,
    size_t device_id_capacity,
    char *device_identity,
    size_t device_identity_capacity
)
{
    return deck_companion_device_identity(
        device_id,
        device_id_capacity,
        device_identity,
        device_identity_capacity
    );
}

bool format_utc(uint64_t unix_ms, char *output, size_t capacity)
{
    const time_t seconds = static_cast<time_t>(unix_ms / 1'000U);
    std::tm utc{};
    if (gmtime_r(&seconds, &utc) == nullptr) {
        return false;
    }
    const unsigned milliseconds = static_cast<unsigned>(unix_ms % 1'000U);
    int size = 0;
    if (milliseconds == 0) {
        size = std::snprintf(
            output,
            capacity,
            "%04d-%02d-%02dT%02d:%02d:%02dZ",
            utc.tm_year + 1900,
            utc.tm_mon + 1,
            utc.tm_mday,
            utc.tm_hour,
            utc.tm_min,
            utc.tm_sec
        );
    } else {
        char fraction[4]{};
        (void)std::snprintf(fraction, sizeof(fraction), "%03u", milliseconds);
        size_t fraction_size = 3;
        while (fraction_size > 0 && fraction[fraction_size - 1] == '0') {
            --fraction_size;
        }
        fraction[fraction_size] = '\0';
        size = std::snprintf(
            output,
            capacity,
            "%04d-%02d-%02dT%02d:%02d:%02d.%sZ",
            utc.tm_year + 1900,
            utc.tm_mon + 1,
            utc.tm_mday,
            utc.tm_hour,
            utc.tm_min,
            utc.tm_sec,
            fraction
        );
    }
    return size > 0 && static_cast<size_t>(size) < capacity;
}

constexpr char kPairingUsername[] = "s3deck-pairing-v2";
constexpr char kSecurityEndpoint[] = "pairing/session";
constexpr char kTransactionEndpoint[] = "pairing/transaction";
constexpr char kServiceType[] = "_s3rlcd-pair";
constexpr char kServiceProtocol[] = "_tcp";
constexpr uint16_t kPairingPort = 3232;
constexpr int64_t kWindowLifetimeUs = 120'000'000;
constexpr EventBits_t kOwnerStopped = BIT0;
constexpr TickType_t kQueuePoll = pdMS_TO_TICKS(100);
constexpr TickType_t kStopTimeout = pdMS_TO_TICKS(2'000);
constexpr TickType_t kLinkEventTimeout = pdMS_TO_TICKS(10'000);
constexpr TickType_t kLinkSendTimeout = pdMS_TO_TICKS(2'000);
constexpr TickType_t kLinkRetryDelay = pdMS_TO_TICKS(500);
constexpr TickType_t kLinkStageDelay = pdMS_TO_TICKS(250);
constexpr size_t kLinkAttemptCount = 3;
constexpr char kDeviceLinkPath[] = "/api/v1/device/link";
constexpr char kSubprotocol[] = "s3-rlcd-deck.v1";
constexpr char kBoard[] = "esp32-s3-rlcd-4.2";

enum class CommandType : uint8_t {
    open,
    cancel,
    start_link_proof,
    stop,
};

struct Command {
    CommandType type;
};

enum class LinkEventType : uint8_t {
    connected,
    heartbeat,
    disconnected,
};

struct LinkEvent {
    LinkEventType type = LinkEventType::disconnected;
    uint32_t generation = 0;
    size_t data_size = 0;
    char data[512]{};
};

struct LinkCallbackContext {
    deck_lan_pairing_t *pairing = nullptr;
    uint32_t generation = 0;
};

void secure_clear(void *buffer, size_t size)
{
    volatile uint8_t *bytes = static_cast<volatile uint8_t *>(buffer);
    while (size > 0) {
        *bytes++ = 0;
        --size;
    }
}

bool encode_window_id(const uint8_t bytes[16], char output[23])
{
    static constexpr char alphabet[] =
        "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
    size_t input = 0;
    size_t encoded = 0;
    while (input + 3 <= 16) {
        const uint32_t value = (static_cast<uint32_t>(bytes[input]) << 16U) |
                               (static_cast<uint32_t>(bytes[input + 1]) << 8U) |
                               static_cast<uint32_t>(bytes[input + 2]);
        output[encoded++] = alphabet[(value >> 18U) & 0x3fU];
        output[encoded++] = alphabet[(value >> 12U) & 0x3fU];
        output[encoded++] = alphabet[(value >> 6U) & 0x3fU];
        output[encoded++] = alphabet[value & 0x3fU];
        input += 3;
    }
    const uint32_t tail = (static_cast<uint32_t>(bytes[15]) << 16U);
    output[encoded++] = alphabet[(tail >> 18U) & 0x3fU];
    output[encoded++] = alphabet[(tail >> 12U) & 0x3fU];
    output[encoded] = '\0';
    return encoded == 22;
}

bool random_code(char code[7])
{
    constexpr uint32_t range = 1'000'000U;
    constexpr uint32_t limit = UINT32_MAX - (UINT32_MAX % range);
    uint32_t value = 0;
    do {
        value = esp_random();
    } while (value >= limit);
    const int written = std::snprintf(code, 7, "%06" PRIu32, value % range);
    return written == 6;
}

}  // namespace

struct deck_lan_pairing {
    deck_companion_profiles_t *profiles;
    deck_lan_pairing_event_fn callback;
    void *callback_context;
    QueueHandle_t commands;
    QueueHandle_t link_events;
    EventGroupHandle_t lifecycle;
    TaskHandle_t task;
    protocomm_t *protocomm;
    deck_pairing_v2_transaction_t *transaction;
    esp_websocket_client_handle_t link_client;
    LinkCallbackContext link_callback;
    protocomm_security2_params_t security;
    char *salt;
    char *verifier;
    int verifier_length;
    char code[7];
    char window_id[23];
    char window_nonce[33];
    char firmware_version[33];
    char hostname[40];
    char instance[48];
    int64_t expires_at_us;
    uint32_t proof_count;
    uint32_t link_generation;
    uint32_t last_remaining_seconds;
    deck_lan_pairing_state_t current_state;
    bool mdns_initialized;
    bool advertised;
    bool transport_started;
    bool committed;
    int64_t close_after_us;
    std::mutex transaction_mutex;
    std::atomic<bool> stop_requested;
    std::atomic<bool> profile_committed_pending;
    std::atomic<bool> first_window_requested;
};

namespace {

void websocket_event(
    void *argument,
    esp_event_base_t,
    int32_t event_id,
    void *event_data
)
{
    const auto *callback = static_cast<const LinkCallbackContext *>(argument);
    deck_lan_pairing_t *pairing = callback != nullptr ? callback->pairing : nullptr;
    if (pairing == nullptr || callback->generation == 0 || pairing->link_events == nullptr) {
        return;
    }
    LinkEvent event{};
    event.generation = callback->generation;
    if (event_id == WEBSOCKET_EVENT_CONNECTED) {
        event.type = LinkEventType::connected;
    } else if (event_id == WEBSOCKET_EVENT_DISCONNECTED ||
               event_id == WEBSOCKET_EVENT_CLOSED ||
               event_id == WEBSOCKET_EVENT_ERROR) {
        event.type = LinkEventType::disconnected;
    } else if (event_id == WEBSOCKET_EVENT_DATA && event_data != nullptr) {
        const auto *data = static_cast<const esp_websocket_event_data_t *>(event_data);
        if (data->op_code != 1U || data->data_len <= 0 || data->payload_offset != 0 ||
            data->payload_len != data->data_len ||
            static_cast<size_t>(data->data_len) >= sizeof(event.data) ||
            data->data_ptr == nullptr) {
            return;
        }
        event.type = LinkEventType::heartbeat;
        event.data_size = static_cast<size_t>(data->data_len);
        std::memcpy(event.data, data->data_ptr, event.data_size);
        event.data[event.data_size] = '\0';
    } else {
        return;
    }
    (void)xQueueSend(pairing->link_events, &event, 0);
    secure_clear(event.data, sizeof(event.data));
}

void publish(deck_lan_pairing_t *pairing, deck_lan_pairing_state_t state, const char *error)
{
    pairing->current_state = state;
    if (pairing->callback == nullptr) {
        return;
    }
    uint32_t remaining = 0;
    const bool window_visible =
        state == DECK_LAN_PAIRING_ACTIVE ||
        state == DECK_LAN_PAIRING_AUTHENTICATING ||
        state == DECK_LAN_PAIRING_PROOF_VERIFIED;
    if (window_visible) {
        const int64_t delta = pairing->expires_at_us - esp_timer_get_time();
        if (delta > 0) {
            remaining = static_cast<uint32_t>((delta + 999'999) / 1'000'000);
        }
    }
    deck_lan_pairing_event_t event{};
    event.state = state;
    if (window_visible) {
        std::memcpy(event.code, pairing->code, sizeof(event.code));
    }
    event.remaining_seconds = remaining;
    event.proof_count = pairing->proof_count;
    event.error_stage = error;
    pairing->callback(pairing->callback_context, &event);
    secure_clear(event.code, sizeof(event.code));
}

void clear_window_secrets(deck_lan_pairing_t *pairing)
{
    secure_clear(pairing->code, sizeof(pairing->code));
    secure_clear(pairing->window_id, sizeof(pairing->window_id));
    secure_clear(pairing->window_nonce, sizeof(pairing->window_nonce));
    if (pairing->salt != nullptr) {
        secure_clear(pairing->salt, pairing->security.salt_len);
        std::free(pairing->salt);
        pairing->salt = nullptr;
    }
    if (pairing->verifier != nullptr) {
        secure_clear(pairing->verifier, static_cast<size_t>(pairing->verifier_length));
        std::free(pairing->verifier);
        pairing->verifier = nullptr;
    }
    pairing->security = {};
    pairing->verifier_length = 0;
    pairing->expires_at_us = 0;
    pairing->last_remaining_seconds = 0;
    pairing->committed = false;
    pairing->close_after_us = 0;
    if (pairing->transaction != nullptr) {
        const std::lock_guard<std::mutex> lock(pairing->transaction_mutex);
        deck_pairing_v2_transaction_reset(pairing->transaction);
    }
}

bool stop_window(deck_lan_pairing_t *pairing)
{
    bool success = true;
    if (pairing->link_client != nullptr) {
        const esp_err_t stopped = esp_websocket_client_stop(pairing->link_client);
        if ((stopped != ESP_OK && stopped != ESP_ERR_INVALID_STATE) ||
            esp_websocket_client_destroy(pairing->link_client) != ESP_OK) {
            success = false;
        } else {
            pairing->link_client = nullptr;
        }
    }
    if (pairing->advertised) {
        if (mdns_service_remove(kServiceType, kServiceProtocol) == ESP_OK) {
            pairing->advertised = false;
        } else {
            success = false;
        }
    }
    if (pairing->transport_started) {
        if (protocomm_httpd_stop(pairing->protocomm) == ESP_OK) {
            pairing->transport_started = false;
        } else {
            success = false;
        }
    }
    if (!success) {
        return false;
    }
    if (pairing->link_events != nullptr) {
        xQueueReset(pairing->link_events);
    }
    if (pairing->protocomm != nullptr) {
        protocomm_delete(pairing->protocomm);
        pairing->protocomm = nullptr;
    }
    clear_window_secrets(pairing);
    return true;
}

esp_err_t transaction_handler(
    uint32_t,
    const uint8_t *input,
    ssize_t input_length,
    uint8_t **output,
    ssize_t *output_length,
    void *context
)
{
    auto *pairing = static_cast<deck_lan_pairing_t *>(context);
    if (pairing == nullptr || input == nullptr || output == nullptr ||
        output_length == nullptr || input_length <= 0 ||
        input_length > DECK_PAIRING_V2_MAX_DOCUMENT_BYTES) {
        return ESP_ERR_INVALID_ARG;
    }
    auto *response = static_cast<uint8_t *>(
        std::malloc(DECK_PAIRING_V2_MAX_DOCUMENT_BYTES)
    );
    if (response == nullptr) {
        return ESP_ERR_NO_MEM;
    }
    size_t response_size = 0;
    deck_pairing_v2_transaction_action_t action = DECK_PAIRING_V2_ACTION_NONE;
    deck_pairing_v2_transaction_result_t result = DECK_PAIRING_V2_TRANSACTION_INVALID;
    {
        const std::lock_guard<std::mutex> lock(pairing->transaction_mutex);
        result = deck_pairing_v2_transaction_exchange(
            pairing->transaction,
            reinterpret_cast<const char *>(input),
            static_cast<size_t>(input_length),
            reinterpret_cast<char *>(response),
            DECK_PAIRING_V2_MAX_DOCUMENT_BYTES,
            &response_size,
            &action
        );
    }
    if (result != DECK_PAIRING_V2_TRANSACTION_OK || response_size == 0 ||
        response_size > static_cast<size_t>(SSIZE_MAX)) {
        secure_clear(response, DECK_PAIRING_V2_MAX_DOCUMENT_BYTES);
        std::free(response);
        return result == DECK_PAIRING_V2_TRANSACTION_STORAGE_FAILURE
                   ? ESP_ERR_NO_MEM
                   : ESP_ERR_INVALID_STATE;
    }
    if (action == DECK_PAIRING_V2_ACTION_START_LINK_PROOF) {
        const Command command{CommandType::start_link_proof};
        if (xQueueSend(pairing->commands, &command, 0) != pdTRUE) {
            const std::lock_guard<std::mutex> lock(pairing->transaction_mutex);
            deck_pairing_v2_transaction_reset(pairing->transaction);
            secure_clear(response, DECK_PAIRING_V2_MAX_DOCUMENT_BYTES);
            std::free(response);
            return ESP_ERR_TIMEOUT;
        }
    } else if (action == DECK_PAIRING_V2_ACTION_PROFILE_COMMITTED) {
        pairing->profile_committed_pending.store(true, std::memory_order_release);
    }
    *output = response;
    *output_length = static_cast<ssize_t>(response_size);
    return ESP_OK;
}

bool send_link_text(
    esp_websocket_client_handle_t client,
    const char *message,
    size_t message_size
)
{
    return client != nullptr && message != nullptr && message_size <= static_cast<size_t>(INT_MAX) &&
           esp_websocket_client_send_text(
               client,
               message,
               static_cast<int>(message_size),
               kLinkSendTimeout
           ) == static_cast<int>(message_size);
}

bool send_link_hello(
    deck_lan_pairing_t *pairing,
    const deck_pairing_v2_link_request_t &request
)
{
    char message[384]{};
    const int size = std::snprintf(
        message,
        sizeof(message),
        "{\"type\":\"device.hello\",\"protocol_version\":1,"
        "\"device_id\":\"%s\",\"firmware_version\":\"%s\","
        "\"board\":\"%s\",\"capabilities\":[\"display\"],"
        "\"serial_state\":\"disarmed\",\"serial_session_id\":0}",
        request.device_id,
        pairing->firmware_version,
        kBoard
    );
    const bool sent = size > 0 && static_cast<size_t>(size) < sizeof(message) &&
                      send_link_text(
                          pairing->link_client,
                          message,
                          static_cast<size_t>(size)
                      );
    secure_clear(message, sizeof(message));
    return sent;
}

bool send_link_heartbeat(deck_lan_pairing_t *pairing, uint64_t server_utc_ms)
{
    char utc[32]{};
    char message[320]{};
    const uint64_t now = monotonic_ms();
    const int size = format_utc(server_utc_ms, utc, sizeof(utc))
                         ? std::snprintf(
                               message,
                               sizeof(message),
                               "{\"type\":\"device.heartbeat\","
                               "\"protocol_version\":1,\"utc\":\"%s\","
                               "\"monotonic_ms\":%llu,\"tx_queue_depth\":0,"
                               "\"tx_queue_capacity\":1,\"rx_queue_depth\":0,"
                               "\"rx_queue_capacity\":1}",
                               utc,
                               static_cast<unsigned long long>(now)
                           )
                         : -1;
    const bool sent = size > 0 && static_cast<size_t>(size) < sizeof(message) &&
                      send_link_text(
                          pairing->link_client,
                          message,
                          static_cast<size_t>(size)
                      );
    secure_clear(utc, sizeof(utc));
    secure_clear(message, sizeof(message));
    return sent;
}

void destroy_link_client(deck_lan_pairing_t *pairing)
{
    if (pairing->link_client == nullptr) {
        return;
    }
    const esp_err_t stopped = esp_websocket_client_stop(pairing->link_client);
    if (stopped == ESP_OK || stopped == ESP_ERR_INVALID_STATE) {
        (void)esp_websocket_client_destroy(pairing->link_client);
        pairing->link_client = nullptr;
    }
    pairing->link_callback.generation = 0;
    if (pairing->link_events != nullptr) {
        xQueueReset(pairing->link_events);
    }
}

bool run_link_attempt(
    deck_lan_pairing_t *pairing,
    const deck_pairing_v2_link_request_t &request
)
{
    char uri[160]{};
    constexpr size_t headers_capacity = 1'024;
    auto *headers = static_cast<char *>(std::calloc(headers_capacity, 1));
    auto *event = new (std::nothrow) LinkEvent{};
    if (headers == nullptr || event == nullptr) {
        std::free(headers);
        delete event;
        return false;
    }
    const int uri_size = std::snprintf(
        uri,
        sizeof(uri),
        "wss://%s%s",
        request.secret.hub_address,
        kDeviceLinkPath
    );
    const int headers_size = std::snprintf(
        headers,
        headers_capacity,
        "Authorization: Bearer %s\r\nX-Device-ID: %s\r\n"
        "X-Device-Identity: %s\r\nX-Protocol-Version: 1\r\n",
        request.secret.token,
        request.device_id,
        request.device_identity
    );
    if (uri_size <= 0 || static_cast<size_t>(uri_size) >= sizeof(uri) ||
        headers_size <= 0 || static_cast<size_t>(headers_size) >= headers_capacity) {
        secure_clear(headers, headers_capacity);
        std::free(headers);
        delete event;
        return false;
    }
    xQueueReset(pairing->link_events);
    ++pairing->link_generation;
    if (pairing->link_generation == 0) {
        ++pairing->link_generation;
    }
    pairing->link_callback = {pairing, pairing->link_generation};
    esp_websocket_client_config_t config{};
    config.uri = uri;
    config.disable_auto_reconnect = true;
    config.user_context = pairing;
    config.task_prio = 2;
    config.task_stack = 6'144;
    config.buffer_size = static_cast<int>(sizeof(event->data) - 1U);
    config.cert_pem = reinterpret_cast<const char *>(request.secret.certificate_der);
    config.cert_len = request.secret.certificate_der_size;
    config.transport = WEBSOCKET_TRANSPORT_OVER_SSL;
    config.subprotocol = kSubprotocol;
    config.headers = headers;
    config.skip_cert_common_name_check = true;
    config.network_timeout_ms = 5'000;
    pairing->link_client = esp_websocket_client_init(&config);
    bool started = false;
    if (pairing->link_client != nullptr) {
        started = esp_websocket_register_events(
                      pairing->link_client,
                      WEBSOCKET_EVENT_ANY,
                      websocket_event,
                      &pairing->link_callback
                  ) == ESP_OK &&
                  esp_websocket_client_start(pairing->link_client) == ESP_OK;
    }
    secure_clear(headers, headers_capacity);
    std::free(headers);
    if (!started) {
        destroy_link_client(pairing);
        delete event;
        return false;
    }
    const int64_t deadline = esp_timer_get_time() + 10'000'000;
    bool hello_sent = false;
    bool proven = false;
    while (!proven && esp_timer_get_time() < deadline &&
           !pairing->stop_requested.load(std::memory_order_acquire)) {
        secure_clear(event, sizeof(*event));
        if (xQueueReceive(pairing->link_events, event, kQueuePoll) != pdTRUE) {
            continue;
        }
        if (event->generation != pairing->link_generation) {
            continue;
        }
        if (event->type == LinkEventType::connected) {
            hello_sent = send_link_hello(pairing, request);
        } else if (event->type == LinkEventType::heartbeat && hello_sent) {
            deck_device_heartbeat_t heartbeat{};
            const deck_device_heartbeat_result_t parsed =
                deck_device_protocol_parse_heartbeat(
                    event->data,
                    event->data_size,
                    0,
                    false,
                    &heartbeat
                );
            if (parsed == DECK_DEVICE_HEARTBEAT_VALID &&
                send_link_heartbeat(pairing, heartbeat.utc_unix_ms)) {
                const std::lock_guard<std::mutex> lock(pairing->transaction_mutex);
                proven = deck_pairing_v2_transaction_mark_link_proven(
                    pairing->transaction,
                    request.session_id,
                    request.transaction_id,
                    heartbeat.utc_unix_ms
                );
            }
        } else if (event->type == LinkEventType::disconnected) {
            break;
        }
    }
    secure_clear(event, sizeof(*event));
    delete event;
    destroy_link_client(pairing);
    return proven;
}

bool run_link_proof(deck_lan_pairing_t *pairing)
{
    auto *request = new (std::nothrow) deck_pairing_v2_link_request_t{};
    if (request == nullptr) {
        return false;
    }
    bool requested = false;
    {
        const std::lock_guard<std::mutex> lock(pairing->transaction_mutex);
        requested = deck_pairing_v2_transaction_link_request(pairing->transaction, request);
    }
    if (!requested) {
        delete request;
        return false;
    }
    vTaskDelay(kLinkStageDelay);
    bool proven = false;
    for (size_t attempt = 0; attempt < kLinkAttemptCount && !proven; ++attempt) {
        if (attempt != 0) {
            vTaskDelay(kLinkRetryDelay);
        }
        proven = run_link_attempt(pairing, *request);
    }
    deck_pairing_v2_link_request_clear(request);
    delete request;
    return proven;
}

bool start_window(deck_lan_pairing_t *pairing)
{
    if (!stop_window(pairing)) {
        return false;
    }
    uint8_t window_bytes[16]{};
    esp_fill_random(window_bytes, sizeof(window_bytes));
    const bool generated = random_code(pairing->code) &&
                           encode_window_id(window_bytes, pairing->window_id);
    encode_hex(window_bytes, sizeof(window_bytes), pairing->window_nonce);
    secure_clear(window_bytes, sizeof(window_bytes));
    if (!generated) {
        clear_window_secrets(pairing);
        return false;
    }
    bool transaction_started = false;
    {
        const std::lock_guard<std::mutex> lock(pairing->transaction_mutex);
        transaction_started = deck_pairing_v2_transaction_begin_window(
            pairing->transaction,
            pairing->window_nonce
        );
    }
    if (!transaction_started) {
        clear_window_secrets(pairing);
        return false;
    }
    if (esp_srp_gen_salt_verifier(
            kPairingUsername,
            sizeof(kPairingUsername) - 1,
            pairing->code,
            6,
            &pairing->salt,
            16,
            &pairing->verifier,
            &pairing->verifier_length
        ) != ESP_OK ||
        pairing->salt == nullptr || pairing->verifier == nullptr ||
        pairing->verifier_length <= 0 || pairing->verifier_length > UINT16_MAX) {
        clear_window_secrets(pairing);
        return false;
    }
    pairing->security = {
        pairing->salt,
        16,
        pairing->verifier,
        static_cast<uint16_t>(pairing->verifier_length),
    };
    pairing->protocomm = protocomm_new();
    if (pairing->protocomm == nullptr) {
        clear_window_secrets(pairing);
        return false;
    }
    protocomm_httpd_config_t http_config{};
    http_config.ext_handle_provided = false;
    http_config.data.config = PROTOCOMM_HTTPD_DEFAULT_CONFIG();
    http_config.data.config.port = kPairingPort;
    http_config.data.config.stack_size = 6'144;
    if (protocomm_httpd_start(pairing->protocomm, &http_config) != ESP_OK) {
        stop_window(pairing);
        return false;
    }
    pairing->transport_started = true;
    if (protocomm_set_security(
            pairing->protocomm,
            kSecurityEndpoint,
            &protocomm_security2,
            &pairing->security
        ) != ESP_OK ||
        protocomm_add_endpoint(
            pairing->protocomm,
            kTransactionEndpoint,
            transaction_handler,
            pairing
        ) != ESP_OK) {
        stop_window(pairing);
        return false;
    }
    mdns_txt_item_t txt[] = {
        {"pv", "2"},
        {"model", "s3-rlcd-deck"},
        {"pairable", "1"},
        {"iid", pairing->window_id},
    };
    if (mdns_service_add(
            pairing->instance,
            kServiceType,
            kServiceProtocol,
            kPairingPort,
            txt,
            sizeof(txt) / sizeof(txt[0])
        ) != ESP_OK) {
        stop_window(pairing);
        return false;
    }
    pairing->advertised = true;
    pairing->expires_at_us = esp_timer_get_time() + kWindowLifetimeUs;
    pairing->last_remaining_seconds = UINT32_MAX;
    return true;
}

bool initialize_mdns(deck_lan_pairing_t *pairing)
{
    uint8_t mac[6]{};
    if (esp_read_mac(mac, ESP_MAC_WIFI_STA) != ESP_OK) {
        return false;
    }
    const int hostname_size = std::snprintf(
        pairing->hostname,
        sizeof(pairing->hostname),
        "s3-rlcd-deck-%02x%02x%02x",
        mac[3],
        mac[4],
        mac[5]
    );
    const int instance_size = std::snprintf(
        pairing->instance,
        sizeof(pairing->instance),
        "S3 RLCD Deck %02X%02X",
        mac[4],
        mac[5]
    );
    secure_clear(mac, sizeof(mac));
    if (hostname_size <= 0 || static_cast<size_t>(hostname_size) >= sizeof(pairing->hostname) ||
        instance_size <= 0 || static_cast<size_t>(instance_size) >= sizeof(pairing->instance)) {
        return false;
    }
    if (mdns_init() != ESP_OK) {
        return false;
    }
    pairing->mdns_initialized = true;
    if (mdns_hostname_set(pairing->hostname) != ESP_OK ||
        mdns_instance_name_set(pairing->instance) != ESP_OK) {
        mdns_free();
        pairing->mdns_initialized = false;
        return false;
    }
    return true;
}

void owner_task(void *context)
{
    auto *pairing = static_cast<deck_lan_pairing_t *>(context);
    if (!initialize_mdns(pairing)) {
        publish(pairing, DECK_LAN_PAIRING_ERROR, "mdns");
    } else {
        publish(pairing, DECK_LAN_PAIRING_IDLE, nullptr);
    }
    while (true) {
        Command command{};
        if (xQueueReceive(pairing->commands, &command, kQueuePoll) == pdTRUE) {
            switch (command.type) {
                case CommandType::open:
                    if (!pairing->mdns_initialized || !start_window(pairing)) {
                        publish(pairing, DECK_LAN_PAIRING_ERROR, "window_start");
                    } else {
                        publish(pairing, DECK_LAN_PAIRING_ACTIVE, nullptr);
                    }
                    break;
                case CommandType::cancel:
                    if (stop_window(pairing)) {
                        publish(pairing, DECK_LAN_PAIRING_IDLE, nullptr);
                    } else {
                        publish(pairing, DECK_LAN_PAIRING_ERROR, "window_stop");
                    }
                    break;
                case CommandType::start_link_proof:
                    publish(pairing, DECK_LAN_PAIRING_AUTHENTICATING, nullptr);
                    if (run_link_proof(pairing)) {
                        ++pairing->proof_count;
                        publish(pairing, DECK_LAN_PAIRING_PROOF_VERIFIED, nullptr);
                    } else {
                        if (stop_window(pairing)) {
                            publish(pairing, DECK_LAN_PAIRING_ERROR, "link_proof");
                        } else {
                            publish(pairing, DECK_LAN_PAIRING_ERROR, "link_stop");
                        }
                    }
                    break;
                case CommandType::stop:
                    pairing->stop_requested.store(true);
                    break;
            }
        }
        if (pairing->profile_committed_pending.exchange(
                false,
                std::memory_order_acq_rel
            )) {
            pairing->committed = true;
            pairing->close_after_us = esp_timer_get_time() + 2'000'000;
            publish(pairing, DECK_LAN_PAIRING_PAIRED, nullptr);
        }
        if (pairing->committed && pairing->close_after_us > 0 &&
            esp_timer_get_time() >= pairing->close_after_us) {
            if (!stop_window(pairing)) {
                publish(pairing, DECK_LAN_PAIRING_ERROR, "paired_stop");
            }
        }
        if (pairing->expires_at_us > 0) {
            const int64_t now = esp_timer_get_time();
            if (!pairing->committed && now >= pairing->expires_at_us) {
                if (stop_window(pairing)) {
                    publish(pairing, DECK_LAN_PAIRING_EXPIRED, nullptr);
                } else {
                    publish(pairing, DECK_LAN_PAIRING_ERROR, "expiry_stop");
                }
            } else if (!pairing->committed) {
                const uint32_t remaining =
                    static_cast<uint32_t>((pairing->expires_at_us - now + 999'999) / 1'000'000);
                if (remaining != pairing->last_remaining_seconds) {
                    pairing->last_remaining_seconds = remaining;
                    publish(pairing, pairing->current_state, nullptr);
                }
            }
        }
        if (pairing->stop_requested.load()) {
            if (stop_window(pairing)) {
                break;
            }
            pairing->stop_requested.store(false);
            publish(pairing, DECK_LAN_PAIRING_ERROR, "owner_stop");
        }
    }
    if (pairing->mdns_initialized) {
        mdns_free();
        pairing->mdns_initialized = false;
    }
    xEventGroupSetBits(pairing->lifecycle, kOwnerStopped);
    vTaskSuspend(nullptr);
}

bool send_command(deck_lan_pairing_t *pairing, CommandType type)
{
    if (pairing == nullptr || pairing->commands == nullptr) {
        return false;
    }
    const Command command{type};
    return xQueueSend(pairing->commands, &command, 0) == pdTRUE;
}

}  // namespace

deck_lan_pairing_t *deck_lan_pairing_start(
    deck_companion_profiles_t *profiles,
    const char *firmware_version,
    deck_lan_pairing_event_fn callback,
    void *callback_context
)
{
    if (profiles == nullptr || firmware_version == nullptr ||
        std::strlen(firmware_version) == 0 || std::strlen(firmware_version) > 32U) {
        return nullptr;
    }
    auto *pairing = new (std::nothrow) deck_lan_pairing_t{};
    if (pairing == nullptr) {
        return nullptr;
    }
    pairing->profiles = profiles;
    pairing->callback = callback;
    pairing->callback_context = callback_context;
    std::memcpy(
        pairing->firmware_version,
        firmware_version,
        std::strlen(firmware_version) + 1U
    );
    pairing->link_callback.pairing = pairing;
    pairing->commands = xQueueCreate(4, sizeof(Command));
    pairing->link_events = xQueueCreate(4, sizeof(LinkEvent));
    pairing->lifecycle = xEventGroupCreate();
    const deck_pairing_v2_transaction_options_t transaction_options = {
        profiles,
        {pairing_sha256, nullptr},
        pairing_random,
        pairing_identity,
        nullptr,
    };
    pairing->transaction = deck_pairing_v2_transaction_create(&transaction_options);
    if (pairing->commands == nullptr || pairing->link_events == nullptr ||
        pairing->lifecycle == nullptr || pairing->transaction == nullptr ||
        xTaskCreatePinnedToCore(
            owner_task,
            "deck_pair_v2",
            6'144,
            pairing,
            tskIDLE_PRIORITY + 4,
            &pairing->task,
            0
        ) != pdPASS) {
        if (pairing->commands != nullptr) {
            vQueueDelete(pairing->commands);
        }
        if (pairing->link_events != nullptr) {
            vQueueDelete(pairing->link_events);
        }
        if (pairing->lifecycle != nullptr) {
            vEventGroupDelete(pairing->lifecycle);
        }
        deck_pairing_v2_transaction_destroy(pairing->transaction);
        secure_clear(pairing->firmware_version, sizeof(pairing->firmware_version));
        delete pairing;
        return nullptr;
    }
    return pairing;
}

bool deck_lan_pairing_open(deck_lan_pairing_t *pairing)
{
    return send_command(pairing, CommandType::open);
}

bool deck_lan_pairing_open_if_unpaired(deck_lan_pairing_t *pairing)
{
    if (pairing == nullptr || pairing->profiles == nullptr) {
        return false;
    }
    auto *snapshot = new (std::nothrow) deck_companion_profiles_snapshot_t{};
    if (snapshot == nullptr) {
        return false;
    }
    const bool unpaired = deck_companion_profiles_snapshot(pairing->profiles, snapshot) &&
                          snapshot->count == 0;
    delete snapshot;
    if (!unpaired) {
        return false;
    }
    bool expected = false;
    if (!pairing->first_window_requested.compare_exchange_strong(
            expected,
            true,
            std::memory_order_acq_rel
        )) {
        return true;
    }
    if (!send_command(pairing, CommandType::open)) {
        pairing->first_window_requested.store(false, std::memory_order_release);
        return false;
    }
    return true;
}

bool deck_lan_pairing_cancel(deck_lan_pairing_t *pairing)
{
    return send_command(pairing, CommandType::cancel);
}

bool deck_lan_pairing_stop(deck_lan_pairing_t *pairing)
{
    if (pairing == nullptr) {
        return true;
    }
    if (!pairing->stop_requested.load()) {
        if (!send_command(pairing, CommandType::stop)) {
            return false;
        }
        pairing->stop_requested.store(true);
    }
    const EventBits_t bits = xEventGroupWaitBits(
        pairing->lifecycle,
        kOwnerStopped,
        pdFALSE,
        pdTRUE,
        kStopTimeout
    );
    if ((bits & kOwnerStopped) == 0) {
        return false;
    }
    vTaskDelete(pairing->task);
    vQueueDelete(pairing->commands);
    vQueueDelete(pairing->link_events);
    vEventGroupDelete(pairing->lifecycle);
    deck_pairing_v2_transaction_destroy(pairing->transaction);
    pairing->transaction = nullptr;
    secure_clear(pairing->firmware_version, sizeof(pairing->firmware_version));
    secure_clear(pairing->hostname, sizeof(pairing->hostname));
    secure_clear(pairing->instance, sizeof(pairing->instance));
    delete pairing;
    return true;
}
