#include "deck_companion_link.h"

#include "deck_ai_snapshot_store_nvs.h"
#include "deck_companion_failover.h"
#include "deck_companion_link_frame.h"
#include "deck_companion_link_message.h"
#include "deck_companion_link_timing.h"
#include "deck_companion_transport_authority.h"
#include "deck_companion_pairing_esp.h"
#include "deck_device_protocol.h"
#include "deck_ota_protocol.h"
#include "deck_ota_service.h"
#include "deck_serial_frame.h"
#include "deck_serial_request_tracker.h"

#include <atomic>
#include <cstdio>
#include <cstring>
#include <ctime>
#include <memory>
#include <mutex>
#include <new>

#include "esp_timer.h"
#include "esp_system.h"
#include "esp_websocket_client.h"
#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"
#include "freertos/queue.h"
#include "freertos/task.h"
#include "psa/crypto.h"

namespace {

constexpr uint32_t kTaskStackBytes = 8'192;
constexpr UBaseType_t kTaskPriority = 2;
constexpr TickType_t kPollTicks = pdMS_TO_TICKS(10);
constexpr TickType_t kSendTimeoutTicks = pdMS_TO_TICKS(2'000);
constexpr TickType_t kReceiveBackpressureTicks = pdMS_TO_TICKS(2'000);
constexpr uint64_t kHeartbeatIntervalMs = 10'000;
constexpr uint64_t kHeartbeatTimeoutMs = 30'000;
constexpr uint64_t kProfilePollMs = 500;
constexpr size_t kMaximumMessageBytes = 16 * 1'024;
constexpr size_t kFrameChunkBytes = 1'024;
constexpr EventBits_t kStoppedBit = BIT0;
constexpr EventBits_t kStartBit = BIT1;
constexpr EventBits_t kAbortBit = BIT2;
constexpr char kSubprotocol[] = "s3-rlcd-deck.v1";
constexpr char kBoard[] = "esp32-s3-rlcd-4.2";

enum class TransportEventType : uint8_t {
    connected,
    disconnected,
    data,
    wake,
};

struct TransportEvent {
    TransportEventType type = TransportEventType::wake;
    uint32_t transport_generation = 0;
    int payload_length = 0;
    int payload_offset = 0;
    uint8_t opcode = 0;
    bool final = false;
    size_t data_size = 0;
    char data[kFrameChunkBytes]{};
};

struct TransportCallbackContext {
    deck_companion_link_t *link = nullptr;
    std::atomic<uint32_t> generation{0};
};

void secure_clear(void *value, size_t size)
{
    auto *bytes = static_cast<volatile uint8_t *>(value);
    while (size != 0) {
        *bytes++ = 0;
        --size;
    }
}

uint64_t monotonic_ms()
{
    return static_cast<uint64_t>(esp_timer_get_time() / 1'000);
}

bool terminated(const char *value, size_t capacity)
{
    return value != nullptr && std::memchr(value, '\0', capacity) != nullptr;
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
        std::snprintf(fraction, sizeof(fraction), "%03u", milliseconds);
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

}  // namespace

struct deck_companion_link {
    deck_companion_profiles_t *profiles = nullptr;
    char firmware_version[33]{};
    char device_id[18]{};
    char device_identity[44]{};
    QueueHandle_t events = nullptr;
    EventGroupHandle_t lifecycle = nullptr;
    TaskHandle_t task = nullptr;
    esp_websocket_client_handle_t client = nullptr;
    std::unique_ptr<deck_companion_profile_secret_t> secret;
    std::unique_ptr<char[]> frame;
    deck_companion_link_frame_t frame_assembler{};
    deck_ai_snapshot_store_t *snapshots = nullptr;
    deck_ota_service_t *ota = nullptr;
    deck_companion_failover_t failover{};
    deck_companion_profiles_snapshot_t profiles_snapshot{};
    bool has_profiles_snapshot = false;
    uint32_t target_profile_generation = 0;
    uint64_t next_profile_poll_ms = 0;
    uint64_t next_connect_ms = 0;
    deck_companion_link_timing_t timing{};
    uint64_t server_utc_ms = 0;
    uint64_t server_monotonic_ms = 0;
    bool has_server_monotonic = false;
    deck_companion_trusted_clock_t trusted_clock{};
    std::atomic<bool> stop_requested{false};
    std::atomic<uint32_t> queue_overflow_generation{0};
    uint32_t transport_generation = 0;
    TransportCallbackContext transport_callback{};
    deck_companion_transport_authority_t transport_authority{};
    mutable std::mutex mutex;
    deck_companion_link_snapshot_t snapshot{};
    std::mutex serial_mutex;
    deck_serial_service_t *serial = nullptr;
    deck_serial_session_snapshot_t published_serial{};
    bool has_published_serial = false;
    std::atomic<bool> serial_publication_dirty{false};
    deck_serial_request_tracker_t serial_requests{};
    bool serial_history_active = false;
    bool serial_stream_ready = false;
    uint64_t serial_history_session_id = 0;
    uint64_t serial_history_cursor = 0;
    uint64_t sent_serial_session_id = 0;
    uint64_t sent_serial_sequence = 0;
    deck_serial_frame_order_t accepted_web_order{};
    uint64_t pending_serial_revoke_epoch = 0;
    bool serial_transport_fenced = false;
};

namespace {

bool advance_failover(
    deck_companion_link_t *link,
    uint64_t now,
    deck_companion_failover_event_t event
);

void update_state(deck_companion_link_t *link, deck_companion_link_state_t state)
{
    const std::lock_guard<std::mutex> lock(link->mutex);
    link->snapshot.state = state;
}

void increment_error(deck_companion_link_t *link)
{
    const std::lock_guard<std::mutex> lock(link->mutex);
    if (link->snapshot.error_count != UINT32_MAX) {
        ++link->snapshot.error_count;
    }
}

bool state_is_online(const deck_companion_link_t *link)
{
    const std::lock_guard<std::mutex> lock(link->mutex);
    return link->snapshot.state == DECK_COMPANION_LINK_ONLINE;
}

bool sequence_after(uint64_t candidate, uint64_t previous)
{
    const uint64_t difference = candidate - previous;
    return difference != 0 && difference < (UINT64_C(1) << 63U);
}

bool begin_serial_transport_revoke(deck_companion_link_t *link)
{
    const std::lock_guard<std::mutex> lock(link->serial_mutex);
    if (link->serial == nullptr) {
        link->pending_serial_revoke_epoch = 0;
        link->serial_transport_fenced = true;
        return true;
    }
    if (link->pending_serial_revoke_epoch != 0) {
        return true;
    }
    return deck_serial_service_revoke_web_transport(
        link->serial,
        &link->pending_serial_revoke_epoch
    );
}

bool serial_transport_revoked(deck_companion_link_t *link)
{
    const std::lock_guard<std::mutex> lock(link->serial_mutex);
    if (link->serial == nullptr) {
        link->pending_serial_revoke_epoch = 0;
        link->serial_transport_fenced = true;
        return true;
    }
    if (link->pending_serial_revoke_epoch == 0) {
        return false;
    }
    if (deck_serial_service_web_transport_revoked(
            link->serial,
            link->pending_serial_revoke_epoch
        )) {
        link->pending_serial_revoke_epoch = 0;
        link->serial_transport_fenced = true;
        return true;
    }
    return false;
}

bool ensure_serial_transport_revoked(deck_companion_link_t *link)
{
    {
        const std::lock_guard<std::mutex> lock(link->serial_mutex);
        if (link->serial_transport_fenced) {
            return true;
        }
    }
    return begin_serial_transport_revoke(link) &&
           serial_transport_revoked(link);
}

void clear_secret(deck_companion_link_t *link)
{
    if (link->secret != nullptr) {
        deck_companion_profile_secret_clear(link->secret.get());
        link->secret.reset();
    }
}

void disconnect_transport(deck_companion_link_t *link)
{
    {
        const std::lock_guard<std::mutex> lock(link->serial_mutex);
        link->serial_transport_fenced = false;
    }
    esp_websocket_client_handle_t client = link->client;
    link->client = nullptr;
    {
        const std::lock_guard<std::mutex> lock(link->mutex);
        ++link->transport_generation;
        if (link->transport_generation == 0) {
            ++link->transport_generation;
        }
        deck_companion_transport_invalidate(&link->transport_authority);
        link->snapshot.state = link->snapshot.has_active_profile
                                   ? DECK_COMPANION_LINK_OFFLINE
                                   : DECK_COMPANION_LINK_UNPAIRED;
    }
    // Retire the data source before any bounded queue wait or transport
    // teardown. UI readers must observe STALE for the whole switch window.
    (void)begin_serial_transport_revoke(link);
    if (client != nullptr) {
        (void)esp_websocket_client_stop(client);
        (void)esp_websocket_client_destroy(client);
    }
    if (link->frame != nullptr && link->frame_assembler.message_size != 0) {
        secure_clear(link->frame.get(), link->frame_assembler.message_size);
    }
    deck_companion_link_frame_reset(&link->frame_assembler);
    link->has_server_monotonic = false;
    link->has_published_serial = false;
    deck_serial_request_transport_reset(&link->serial_requests);
    link->serial_history_active = false;
    link->serial_stream_ready = false;
    link->serial_history_session_id = 0;
    link->serial_history_cursor = 0;
    deck_serial_frame_order_reset(&link->accepted_web_order);
    link->timing = {};
}

void schedule_retry(deck_companion_link_t *link, uint64_t now, bool failure)
{
    disconnect_transport(link);
    if (failure) {
        increment_error(link);
    }
    uint32_t attempts = 0;
    {
        const std::lock_guard<std::mutex> lock(link->mutex);
        if (link->snapshot.reconnect_attempts != UINT32_MAX) {
            ++link->snapshot.reconnect_attempts;
        }
        attempts = link->snapshot.reconnect_attempts;
        link->snapshot.state = DECK_COMPANION_LINK_OFFLINE;
    }
    link->next_connect_ms = now + deck_companion_link_retry_delay_ms(attempts);
    (void)advance_failover(
        link,
        now,
        DECK_COMPANION_FAILOVER_TRANSPORT_FAILED
    );
}

void websocket_event(
    void *argument,
    esp_event_base_t,
    int32_t event_id,
    void *event_data
)
{
    const auto *callback = static_cast<const TransportCallbackContext *>(argument);
    deck_companion_link_t *link = callback != nullptr ? callback->link : nullptr;
    const uint32_t callback_generation =
        callback != nullptr
            ? callback->generation.load(std::memory_order_acquire)
            : 0;
    if (link == nullptr || callback_generation == 0 ||
        link->stop_requested.load(std::memory_order_acquire)) {
        return;
    }
    TransportEvent event{};
    event.transport_generation = callback_generation;
    if (event_id == WEBSOCKET_EVENT_CONNECTED) {
        event.type = TransportEventType::connected;
    } else if (event_id == WEBSOCKET_EVENT_DISCONNECTED ||
               event_id == WEBSOCKET_EVENT_CLOSED ||
               event_id == WEBSOCKET_EVENT_ERROR) {
        event.type = TransportEventType::disconnected;
    } else if (event_id == WEBSOCKET_EVENT_DATA && event_data != nullptr) {
        const auto *data = static_cast<const esp_websocket_event_data_t *>(event_data);
        if (data->op_code >= 8U) {
            return;
        }
        if (data->data_len < 0 || static_cast<size_t>(data->data_len) > sizeof(event.data)) {
            link->queue_overflow_generation.store(
                callback_generation,
                std::memory_order_release
            );
            return;
        }
        event.type = TransportEventType::data;
        event.payload_length = data->payload_len;
        event.payload_offset = data->payload_offset;
        event.opcode = data->op_code;
        event.final = data->fin;
        event.data_size = static_cast<size_t>(data->data_len);
        if (event.data_size != 0 && data->data_ptr != nullptr) {
            std::memcpy(event.data, data->data_ptr, event.data_size);
        }
    } else {
        return;
    }
    const TickType_t queue_wait = event.type == TransportEventType::data
                                      ? kReceiveBackpressureTicks
                                      : 0;
    if (xQueueSend(link->events, &event, queue_wait) != pdPASS) {
        link->queue_overflow_generation.store(
            callback_generation,
            std::memory_order_release
        );
    }
}

bool certificate_matches(const deck_companion_profile_secret_t &secret)
{
    if (secret.certificate_der_size == 0 ||
        secret.certificate_der_size > sizeof(secret.certificate_der) ||
        std::strlen(secret.certificate_fingerprint) != 71) {
        return false;
    }
    uint8_t digest[32]{};
    size_t digest_size = 0;
    if (psa_crypto_init() != PSA_SUCCESS ||
        psa_hash_compute(
            PSA_ALG_SHA_256,
            secret.certificate_der,
            secret.certificate_der_size,
            digest,
            sizeof(digest),
            &digest_size
        ) != PSA_SUCCESS ||
        digest_size != sizeof(digest)) {
        return false;
    }
    const bool matches = deck_device_protocol_fingerprint_matches_sha256(
        digest,
        secret.certificate_fingerprint
    );
    secure_clear(digest, sizeof(digest));
    return matches;
}

bool start_transport(deck_companion_link_t *link)
{
    if (link->secret == nullptr || !certificate_matches(*link->secret)) {
        return false;
    }
    char uri[160]{};
    char headers[320]{};
    const int uri_size = std::snprintf(
        uri,
        sizeof(uri),
        "wss://%s/api/v1/device/link",
        link->secret->hub_address
    );
    const int headers_size = std::snprintf(
        headers,
        sizeof(headers),
        "Authorization: Bearer %s\r\nX-Device-ID: %s\r\nX-Device-Identity: %s\r\nX-Protocol-Version: 1\r\n",
        link->secret->token,
        link->device_id,
        link->device_identity
    );
    if (uri_size <= 0 || static_cast<size_t>(uri_size) >= sizeof(uri) ||
        headers_size <= 0 || static_cast<size_t>(headers_size) >= sizeof(headers)) {
        secure_clear(headers, sizeof(headers));
        return false;
    }
    deck_companion_link_timing_begin_connection(
        &link->timing,
        monotonic_ms(),
        kHeartbeatTimeoutMs
    );
    esp_websocket_client_config_t config{};
    config.uri = uri;
    config.disable_auto_reconnect = true;
    config.user_context = link;
    config.task_prio = 2;
    config.task_stack = 6'144;
    config.buffer_size = static_cast<int>(kFrameChunkBytes);
    config.cert_pem = reinterpret_cast<const char *>(link->secret->certificate_der);
    config.cert_len = link->secret->certificate_der_size;
    config.transport = WEBSOCKET_TRANSPORT_OVER_SSL;
    config.subprotocol = kSubprotocol;
    config.headers = headers;
    config.skip_cert_common_name_check = true;
    config.network_timeout_ms = 5'000;
    config.ping_interval_sec = 10;
    config.pingpong_timeout_sec = 30;
    esp_websocket_client_handle_t client = esp_websocket_client_init(&config);
    bool started = false;
    if (client != nullptr) {
        uint32_t generation = 0;
        {
            const std::lock_guard<std::mutex> lock(link->mutex);
            ++link->transport_generation;
            if (link->transport_generation == 0) {
                ++link->transport_generation;
            }
            generation = link->transport_generation;
            if (!deck_companion_transport_begin(
                    &link->transport_authority,
                    generation
                )) {
                secure_clear(headers, sizeof(headers));
                (void)esp_websocket_client_destroy(client);
                return false;
            }
        }
        link->transport_callback.generation.store(
            generation,
            std::memory_order_release
        );
        link->client = client;
        started = esp_websocket_register_events(
                      client,
                      WEBSOCKET_EVENT_ANY,
                      websocket_event,
                      &link->transport_callback
                  ) == ESP_OK &&
                  esp_websocket_client_start(client) == ESP_OK;
    }
    secure_clear(headers, sizeof(headers));
    if (!started) {
        if (client != nullptr) {
            if (link->client == client) {
                link->client = nullptr;
            }
            (void)esp_websocket_client_destroy(client);
        }
        {
            const std::lock_guard<std::mutex> lock(link->mutex);
            ++link->transport_generation;
            if (link->transport_generation == 0) {
                ++link->transport_generation;
            }
            deck_companion_transport_invalidate(&link->transport_authority);
        }
        return false;
    }
    update_state(link, DECK_COMPANION_LINK_CONNECTING);
    return true;
}

bool send_text(deck_companion_link_t *link, const char *message, size_t size)
{
    return link->client != nullptr && size <= static_cast<size_t>(INT_MAX) &&
           esp_websocket_client_send_text(
               link->client,
               message,
               static_cast<int>(size),
               kSendTimeoutTicks
           ) == static_cast<int>(size);
}

bool send_binary(deck_companion_link_t *link, const uint8_t *message, size_t size)
{
    return link->client != nullptr && message != nullptr &&
           size <= static_cast<size_t>(INT_MAX) &&
           esp_websocket_client_send_bin(
               link->client,
               reinterpret_cast<const char *>(message),
               static_cast<int>(size),
               kSendTimeoutTicks
           ) == static_cast<int>(size);
}

bool read_serial_snapshot(
    deck_companion_link_t *link,
    deck_serial_session_snapshot_t *snapshot
)
{
    if (snapshot == nullptr) {
        return false;
    }
    const std::lock_guard<std::mutex> lock(link->serial_mutex);
    if (link->serial == nullptr) {
        *snapshot = {};
        snapshot->state = DECK_SERIAL_DISARMED;
        return true;
    }
    return deck_serial_service_snapshot(link->serial, snapshot);
}

const char *serial_state_name(deck_serial_state_t state)
{
    switch (state) {
        case DECK_SERIAL_USB_TX:
            return "usb_tx";
        case DECK_SERIAL_WEB_TX:
            return "web_tx";
        case DECK_SERIAL_DISARMED:
        default:
            return "disarmed";
    }
}

bool same_serial_publication(
    const deck_serial_session_snapshot_t &left,
    const deck_serial_session_snapshot_t &right
)
{
    return left.state == right.state && left.session_id == right.session_id &&
           left.owner_generation == right.owner_generation &&
           left.lease_id == right.lease_id;
}

bool send_hello(deck_companion_link_t *link)
{
    deck_serial_session_snapshot_t serial{};
    if (!read_serial_snapshot(link, &serial)) {
        return false;
    }
    char message[320]{};
    const uint64_t wire_session_id =
        serial.state == DECK_SERIAL_DISARMED ? 0 : serial.session_id;
    const int size = std::snprintf(
        message,
        sizeof(message),
        "{\"type\":\"device.hello\",\"protocol_version\":1,\"device_id\":\"%s\",\"firmware_version\":\"%s\",\"board\":\"%s\",\"capabilities\":[\"display\",\"ota\",\"serial\"],\"serial_state\":\"%s\",\"serial_session_id\":%llu}",
        link->device_id,
        link->firmware_version,
        kBoard,
        serial_state_name(serial.state),
        static_cast<unsigned long long>(wire_session_id)
    );
    const bool sent = size > 0 && static_cast<size_t>(size) < sizeof(message) &&
                      send_text(link, message, static_cast<size_t>(size));
    if (sent) {
        link->published_serial = serial;
        link->has_published_serial = true;
        link->serial_stream_ready = serial.state == DECK_SERIAL_DISARMED;
        link->serial_history_active = false;
        link->serial_history_session_id = 0;
        link->serial_history_cursor = 0;
    }
    return sent;
}

bool send_serial_state_if_changed(deck_companion_link_t *link)
{
    deck_serial_session_snapshot_t serial{};
    if (!read_serial_snapshot(link, &serial)) {
        return false;
    }
    if (link->has_published_serial &&
        same_serial_publication(link->published_serial, serial)) {
        return true;
    }
    const bool session_changed =
        !link->has_published_serial ||
        link->published_serial.session_id != serial.session_id ||
        (link->published_serial.state == DECK_SERIAL_DISARMED) !=
            (serial.state == DECK_SERIAL_DISARMED);
    char message[256]{};
    const uint64_t wire_session_id =
        serial.state == DECK_SERIAL_DISARMED ? 0 : serial.session_id;
    const int size = std::snprintf(
        message,
        sizeof(message),
        "{\"type\":\"serial.state\",\"protocol_version\":1,\"serial_state\":\"%s\",\"serial_session_id\":%llu,\"owner_generation\":%llu,\"lease_id\":%llu}",
        serial_state_name(serial.state),
        static_cast<unsigned long long>(wire_session_id),
        static_cast<unsigned long long>(serial.owner_generation),
        static_cast<unsigned long long>(serial.lease_id)
    );
    if (size <= 0 || static_cast<size_t>(size) >= sizeof(message) ||
        !send_text(link, message, static_cast<size_t>(size))) {
        return false;
    }
    link->published_serial = serial;
    link->has_published_serial = true;
    if (session_changed) {
        link->serial_stream_ready = serial.state == DECK_SERIAL_DISARMED;
        link->serial_history_active = false;
        link->serial_history_session_id = 0;
        link->serial_history_cursor = 0;
        if (serial.state == DECK_SERIAL_DISARMED) {
            link->sent_serial_session_id = 0;
            link->sent_serial_sequence = 0;
            deck_serial_frame_order_reset(&link->accepted_web_order);
        }
    }
    return true;
}

bool send_serial_block(
    deck_companion_link_t *link,
    deck_serial_routed_block_t *block
)
{
    if (block == nullptr || block->session_id == 0 || block->sequence == 0 ||
        block->length == 0 || block->length > sizeof(block->bytes)) {
        return false;
    }
    uint8_t document[DECK_SERIAL_FRAME_MAX_BYTES]{};
    const size_t size = deck_serial_frame_encode(
        DECK_SERIAL_FRAME_TARGET_RX,
        block->session_id,
        block->sequence,
        block->monotonic_ms,
        block->bytes,
        block->length,
        document,
        sizeof(document)
    );
    const bool sent = size != 0 && send_binary(link, document, size);
    secure_clear(document, sizeof(document));
    if (sent) {
        link->sent_serial_session_id = block->session_id;
        link->sent_serial_sequence = block->sequence;
    }
    secure_clear(block, sizeof(*block));
    return sent;
}

bool send_one_live_serial_block(deck_companion_link_t *link, bool *progressed)
{
    *progressed = false;
    deck_serial_routed_block_t block{};
    deck_serial_router_copy_result_t result = DECK_SERIAL_ROUTER_COPY_EMPTY;
    {
        const std::lock_guard<std::mutex> lock(link->serial_mutex);
        if (link->serial == nullptr) {
            return true;
        }
        result = deck_serial_service_take(
            link->serial,
            DECK_SERIAL_SINK_WSS,
            &block
        );
    }
    if (result == DECK_SERIAL_ROUTER_COPY_EMPTY ||
        result == DECK_SERIAL_ROUTER_COPY_INVALID) {
        return true;
    }
    if (result != DECK_SERIAL_ROUTER_COPY_OK || block.length == 0 ||
        block.length > sizeof(block.bytes)) {
        return false;
    }
    *progressed = true;
    if (link->sent_serial_session_id == block.session_id &&
        !sequence_after(block.sequence, link->sent_serial_sequence)) {
        secure_clear(&block, sizeof(block));
        return true;
    }
    return send_serial_block(link, &block);
}

bool send_one_history_serial_block(
    deck_companion_link_t *link,
    bool *progressed
)
{
    *progressed = false;
    if (!link->serial_history_active) {
        return true;
    }
    deck_serial_routed_block_t block{};
    deck_serial_router_copy_result_t result = DECK_SERIAL_ROUTER_COPY_EMPTY;
    {
        const std::lock_guard<std::mutex> lock(link->serial_mutex);
        if (link->serial == nullptr) {
            return false;
        }
        result = deck_serial_service_copy_history_after(
            link->serial,
            link->serial_history_cursor,
            &block
        );
    }
    if (result == DECK_SERIAL_ROUTER_COPY_EMPTY) {
        link->serial_history_active = false;
        return true;
    }
    if ((result != DECK_SERIAL_ROUTER_COPY_OK &&
         result != DECK_SERIAL_ROUTER_COPY_GAP) ||
        block.session_id != link->serial_history_session_id) {
        secure_clear(&block, sizeof(block));
        return false;
    }
    if (result == DECK_SERIAL_ROUTER_COPY_OK &&
        link->serial_history_cursor != 0 &&
        !sequence_after(block.sequence, link->serial_history_cursor)) {
        secure_clear(&block, sizeof(block));
        return false;
    }
    if (!send_serial_block(link, &block)) {
        return false;
    }
    link->serial_history_cursor = link->sent_serial_sequence;
    *progressed = true;
    return true;
}

const char *serial_command_code_name(deck_serial_command_code_t code)
{
    switch (code) {
        case DECK_SERIAL_COMMAND_APPLIED:
            return "applied";
        case DECK_SERIAL_COMMAND_NO_CHANGE:
            return "no_change";
        case DECK_SERIAL_COMMAND_STALE_SESSION:
            return "stale_session";
        case DECK_SERIAL_COMMAND_STALE_REQUEST:
            return "stale_request";
        case DECK_SERIAL_COMMAND_UART_INSTALL_FAILED:
            return "uart_install_failed";
        case DECK_SERIAL_COMMAND_UART_UNINSTALL_FAILED:
            return "uart_uninstall_failed";
        case DECK_SERIAL_COMMAND_INVALID:
        default:
            return "invalid";
    }
}

bool send_pending_serial_owner_result(deck_companion_link_t *link)
{
    if (!link->serial_requests.pending) {
        return true;
    }
    deck_serial_command_result_t result{};
    {
        const std::lock_guard<std::mutex> lock(link->serial_mutex);
        if (link->serial == nullptr ||
            !deck_serial_service_command_result(
                link->serial,
                link->serial_requests.service_request_id,
                &result
            )) {
            return true;
        }
    }
    char message[320]{};
    const uint64_t wire_session_id =
        result.state == DECK_SERIAL_DISARMED ? 0 : result.session_id;
    const int size = std::snprintf(
        message,
        sizeof(message),
        "{\"type\":\"serial.owner.result\",\"protocol_version\":1,\"serial_session_id\":%llu,\"request_id\":%llu,\"code\":\"%s\",\"serial_state\":\"%s\",\"owner_generation\":%llu,\"lease_id\":%llu}",
        static_cast<unsigned long long>(wire_session_id),
        static_cast<unsigned long long>(
            link->serial_requests.external_request_id
        ),
        serial_command_code_name(result.code),
        serial_state_name(result.state),
        static_cast<unsigned long long>(result.owner_generation),
        static_cast<unsigned long long>(result.lease_id)
    );
    if (size <= 0 || static_cast<size_t>(size) >= sizeof(message) ||
        !send_text(link, message, static_cast<size_t>(size))) {
        return false;
    }
    uint64_t external_request_id = 0;
    if (!deck_serial_request_complete(
            &link->serial_requests,
            result.request_id,
            &external_request_id
        )) {
        return false;
    }
    return true;
}

bool handle_serial_control(
    deck_companion_link_t *link,
    const char *message,
    size_t message_size,
    bool *handled
)
{
    *handled = false;
    deck_device_serial_control_t control{};
    if (!deck_device_protocol_parse_serial_control(
            message,
            message_size,
            &control
        )) {
        return true;
    }
    *handled = true;
    const std::lock_guard<std::mutex> lock(link->serial_mutex);
    if (link->serial == nullptr) {
        return false;
    }
    deck_serial_session_snapshot_t serial{};
    if (!deck_serial_service_snapshot(link->serial, &serial) ||
        serial.state == DECK_SERIAL_DISARMED ||
        serial.session_id != control.session_id) {
        return false;
    }
    switch (control.kind) {
        case DECK_DEVICE_SERIAL_OWNER_REQUEST:
            {
                uint64_t service_request_id = 0;
                const deck_serial_request_begin_result_t begin =
                    deck_serial_request_begin(
                        &link->serial_requests,
                        control.request_id,
                        &service_request_id
                    );
                if (begin == DECK_SERIAL_REQUEST_REPLAY) {
                    return true;
                }
                if (begin != DECK_SERIAL_REQUEST_NEW) {
                    return false;
                }
                if (!deck_serial_service_request_web(
                        link->serial,
                        control.session_id,
                        service_request_id,
                        control.enable
                    )) {
                    deck_serial_request_transport_reset(
                        &link->serial_requests
                    );
                    return false;
                }
                return true;
            }
        case DECK_DEVICE_SERIAL_OWNER_ACTIVITY:
            if (serial.state != DECK_SERIAL_WEB_TX ||
                serial.lease_id != control.lease_id) {
                return false;
            }
            return deck_serial_service_web_activity(
                link->serial,
                control.session_id,
                control.lease_id
            );
        case DECK_DEVICE_SERIAL_HISTORY_REQUEST:
            link->serial_history_active = true;
            link->serial_stream_ready = true;
            link->serial_history_session_id = control.session_id;
            link->serial_history_cursor = control.after_sequence;
            link->sent_serial_session_id = control.session_id;
            link->sent_serial_sequence = control.after_sequence;
            return true;
        default:
            return false;
    }
}

bool handle_serial_binary(
    deck_companion_link_t *link,
    const uint8_t *document,
    size_t document_size
)
{
    deck_serial_frame_view_t frame{};
    if (!deck_serial_frame_decode(document, document_size, &frame) ||
        frame.channel != DECK_SERIAL_FRAME_WEB_TX) {
        return false;
    }
    const std::lock_guard<std::mutex> lock(link->serial_mutex);
    if (link->serial == nullptr) {
        return false;
    }
    deck_serial_session_snapshot_t serial{};
    if (!deck_serial_service_snapshot(link->serial, &serial) ||
        serial.state != DECK_SERIAL_WEB_TX || !serial.uart_installed ||
        serial.session_id != frame.session_id || serial.lease_id == 0) {
        return false;
    }
    if (!deck_serial_frame_order_accepts(&link->accepted_web_order, &frame)) {
        return false;
    }
    if (!deck_serial_service_submit_web(
            link->serial,
            frame.session_id,
            serial.lease_id,
            frame.payload,
            frame.payload_size
        )) {
        return false;
    }
    deck_serial_frame_order_commit(&link->accepted_web_order, &frame);
    return true;
}

bool send_heartbeat(deck_companion_link_t *link, uint64_t now)
{
    char utc[32]{};
    const uint64_t estimated_utc =
        link->server_utc_ms + (now - link->timing.last_server_heartbeat_ms);
    if (!format_utc(estimated_utc, utc, sizeof(utc))) {
        return false;
    }
    const UBaseType_t queued = uxQueueMessagesWaiting(link->events);
    char message[384]{};
    const int size = std::snprintf(
        message,
        sizeof(message),
        "{\"type\":\"device.heartbeat\",\"protocol_version\":1,\"utc\":\"%s\",\"monotonic_ms\":%llu,\"tx_queue_depth\":0,\"tx_queue_capacity\":4,\"rx_queue_depth\":%u,\"rx_queue_capacity\":4}",
        utc,
        static_cast<unsigned long long>(now),
        static_cast<unsigned>(queued)
    );
    return size > 0 && static_cast<size_t>(size) < sizeof(message) &&
           send_text(link, message, static_cast<size_t>(size));
}

void publish_profiles(
    deck_companion_link_t *link,
    const deck_companion_profiles_snapshot_t &profiles
)
{
    const std::lock_guard<std::mutex> lock(link->mutex);
    link->snapshot.has_active_profile = profiles.has_active;
    link->snapshot.profile_generation = profiles.generation;
    std::memset(
        link->snapshot.active_profile_id,
        0,
        sizeof(link->snapshot.active_profile_id)
    );
    if (profiles.has_active) {
        std::memcpy(
            link->snapshot.active_profile_id,
            profiles.active_profile_id,
            sizeof(link->snapshot.active_profile_id)
        );
    } else {
        link->snapshot.state = DECK_COMPANION_LINK_UNPAIRED;
        link->snapshot.failover_active = false;
        std::memset(
            link->snapshot.connection_profile_id,
            0,
            sizeof(link->snapshot.connection_profile_id)
        );
    }
}

bool connect_failover_target(
    deck_companion_link_t *link,
    const deck_companion_failover_action_t &action,
    uint64_t now
)
{
    std::unique_ptr<deck_companion_profile_secret_t> secret(
        new (std::nothrow) deck_companion_profile_secret_t{}
    );
    if (secret == nullptr ||
        !deck_companion_profiles_secret_for(
            link->profiles,
            action.profile_id,
            action.profile_generation,
            secret.get()
        )) {
        if (secret != nullptr) {
            deck_companion_profile_secret_clear(secret.get());
        }
        increment_error(link);
        return false;
    }
    disconnect_transport(link);
    clear_secret(link);
    link->secret = std::move(secret);
    link->target_profile_generation = action.profile_generation;
    link->next_connect_ms = now;
    {
        const std::lock_guard<std::mutex> lock(link->mutex);
        link->snapshot.state = DECK_COMPANION_LINK_OFFLINE;
        link->snapshot.reconnect_attempts = 0;
        link->snapshot.failover_active =
            std::strcmp(action.profile_id, link->snapshot.active_profile_id) != 0;
        std::memcpy(
            link->snapshot.connection_profile_id,
            action.profile_id,
            sizeof(link->snapshot.connection_profile_id)
        );
    }
    return true;
}

bool advance_failover(
    deck_companion_link_t *link,
    uint64_t now,
    deck_companion_failover_event_t event
)
{
    if (!link->has_profiles_snapshot) {
        return true;
    }
    deck_companion_failover_action_t action{};
    if (!deck_companion_failover_advance(
            &link->failover,
            &link->profiles_snapshot,
            now,
            event,
            &action
        )) {
        increment_error(link);
        link->failover.initialized = false;
        return false;
    }
    if (action.kind == DECK_COMPANION_FAILOVER_CONNECT) {
        const bool connected = connect_failover_target(link, action, now);
        if (!connected) {
            link->failover.initialized = false;
        }
        return connected;
    }
    const std::lock_guard<std::mutex> lock(link->mutex);
    link->snapshot.failover_active = link->failover.round_active;
    return true;
}

bool refresh_profile(deck_companion_link_t *link, uint64_t now)
{
    deck_companion_profiles_snapshot_t profiles{};
    if (!deck_companion_profiles_snapshot(link->profiles, &profiles)) {
        increment_error(link);
        return false;
    }
    link->profiles_snapshot = profiles;
    link->has_profiles_snapshot = true;
    publish_profiles(link, profiles);
    if (!profiles.has_active) {
        if (link->client != nullptr || link->secret != nullptr ||
            link->target_profile_generation != 0) {
            disconnect_transport(link);
        }
        clear_secret(link);
        link->target_profile_generation = 0;
        (void)advance_failover(
            link,
            now,
            DECK_COMPANION_FAILOVER_PROFILES_OBSERVED
        );
        return true;
    }
    return advance_failover(
        link,
        now,
        DECK_COMPANION_FAILOVER_PROFILES_OBSERVED
    );
}

bool transport_allows(
    const deck_companion_link_t *link,
    deck_companion_transport_message_t message
)
{
    const std::lock_guard<std::mutex> lock(link->mutex);
    return deck_companion_transport_allows(
        &link->transport_authority,
        link->transport_generation,
        message
    );
}

bool accept_heartbeat(
    deck_companion_link_t *link,
    const deck_device_heartbeat_t &heartbeat,
    uint64_t now
)
{
    const bool first_valid_heartbeat = !transport_allows(
        link,
        DECK_COMPANION_TRANSPORT_AI_SNAPSHOT
    );
    if (first_valid_heartbeat && link->secret == nullptr) {
        return false;
    }
    if (first_valid_heartbeat) {
        const deck_companion_profile_update_result_t activated =
            deck_companion_profiles_activate_on_success(
                link->profiles,
                link->secret->profile_id,
                link->target_profile_generation,
                heartbeat.utc_unix_ms
            );
        if (activated == DECK_COMPANION_PROFILE_STALE_GENERATION) {
            return refresh_profile(link, now);
        }
        if (activated != DECK_COMPANION_PROFILE_UPDATED) {
            increment_error(link);
            return false;
        }
        deck_companion_profiles_snapshot_t profiles{};
        if (!deck_companion_profiles_snapshot(link->profiles, &profiles)) {
            increment_error(link);
            return false;
        }
        link->profiles_snapshot = profiles;
        link->has_profiles_snapshot = true;
        link->target_profile_generation = profiles.generation;
        publish_profiles(link, profiles);
        if (!advance_failover(link, now, DECK_COMPANION_FAILOVER_ONLINE)) {
            return false;
        }
        if (link->client == nullptr) {
            return true;
        }
        {
            const std::lock_guard<std::mutex> lock(link->mutex);
            if (!deck_companion_transport_activate(
                    &link->transport_authority,
                    link->transport_generation
                )) {
                return false;
            }
        }
    }
    deck_companion_link_timing_server_heartbeat(
        &link->timing,
        now,
        kHeartbeatIntervalMs
    );
    {
        const std::lock_guard<std::mutex> lock(link->mutex);
        link->server_utc_ms = heartbeat.utc_unix_ms;
        link->server_monotonic_ms = heartbeat.monotonic_ms;
        link->has_server_monotonic = true;
        (void)deck_companion_trusted_clock_accept(
            &link->trusted_clock,
            heartbeat.utc_unix_ms,
            now
        );
        link->snapshot.state = DECK_COMPANION_LINK_ONLINE;
        link->snapshot.reconnect_attempts = 0;
        link->snapshot.last_heartbeat_monotonic_ms = now;
    }
    return true;
}

bool accept_data(deck_companion_link_t *link, const TransportEvent &event)
{
    if (event.payload_length > static_cast<int>(kMaximumMessageBytes) ||
        event.data_size > kFrameChunkBytes) {
        return false;
    }
    const deck_companion_link_frame_result_t assembled =
        deck_companion_link_frame_accept(
            &link->frame_assembler,
            event.payload_length,
            event.payload_offset,
            event.opcode,
            event.final,
            event.data,
            event.data_size
        );
    if (assembled == DECK_COMPANION_LINK_FRAME_INVALID) {
        return false;
    }
    if (assembled == DECK_COMPANION_LINK_FRAME_PARTIAL) {
        return true;
    }
    const size_t message_size = link->frame_assembler.message_size;
    const uint8_t message_opcode = link->frame_assembler.message_opcode;
    if (message_opcode == 2U) {
        const bool accepted =
            transport_allows(
                link,
                DECK_COMPANION_TRANSPORT_SERIAL_BINARY
            ) &&
            handle_serial_binary(
                link,
                reinterpret_cast<const uint8_t *>(link->frame.get()),
                message_size
            );
        secure_clear(link->frame.get(), message_size);
        deck_companion_link_frame_reset(&link->frame_assembler);
        return accepted;
    }
    if (message_opcode != 1U) {
        secure_clear(link->frame.get(), message_size);
        deck_companion_link_frame_reset(&link->frame_assembler);
        return false;
    }
    link->frame[message_size] = '\0';
    const uint64_t now = monotonic_ms();
    deck_device_heartbeat_t heartbeat{};
    if (deck_device_protocol_parse_heartbeat(
            link->frame.get(),
            message_size,
            link->server_monotonic_ms,
            link->has_server_monotonic,
            &heartbeat
        )) {
        secure_clear(link->frame.get(), message_size + 1);
        deck_companion_link_frame_reset(&link->frame_assembler);
        return accept_heartbeat(link, heartbeat, now);
    }
    if (!transport_allows(
            link,
            DECK_COMPANION_TRANSPORT_AI_SNAPSHOT
        )) {
        secure_clear(link->frame.get(), message_size + 1);
        deck_companion_link_frame_reset(&link->frame_assembler);
        return false;
    }
    bool serial_control_handled = false;
    const bool serial_control_accepted = handle_serial_control(
        link,
        link->frame.get(),
        message_size,
        &serial_control_handled
    );
    if (serial_control_handled) {
        secure_clear(link->frame.get(), message_size + 1);
        deck_companion_link_frame_reset(&link->frame_assembler);
        return serial_control_accepted;
    }
    deck_ota_protocol_command_t ota_command{};
    if (deck_ota_protocol_parse(
            link->frame.get(),
            message_size,
            &ota_command
        )) {
        const bool accepted =
            link->ota != nullptr &&
            (ota_command.kind == DECK_OTA_PROTOCOL_OFFER
                 ? deck_ota_service_offer(
                       link->ota,
                       ota_command.transaction_id,
                       &ota_command.manifest
                   )
                 : deck_ota_service_write(
                       link->ota,
                       ota_command.transaction_id,
                       ota_command.offset,
                       ota_command.data,
                       ota_command.data_size,
                       ota_command.final
                   ));
        deck_ota_protocol_command_clear(&ota_command);
        secure_clear(link->frame.get(), message_size + 1);
        deck_companion_link_frame_reset(&link->frame_assembler);
        return accepted;
    }
    uint64_t trusted_utc_ms = 0;
    if (link->has_server_monotonic &&
        now >= link->timing.last_server_heartbeat_ms &&
        UINT64_MAX - link->server_utc_ms >=
            now - link->timing.last_server_heartbeat_ms) {
        trusted_utc_ms = link->server_utc_ms +
                         (now - link->timing.last_server_heartbeat_ms);
    }
    const deck_companion_server_message_result_t message_result =
        deck_companion_link_accept_server_message(
            link->snapshots,
            link->frame.get(),
            message_size,
            trusted_utc_ms,
            link->server_monotonic_ms,
            link->has_server_monotonic,
            &heartbeat
        );
    secure_clear(link->frame.get(), message_size + 1);
    deck_companion_link_frame_reset(&link->frame_assembler);
    const bool accepted_snapshot =
        message_result == DECK_COMPANION_SERVER_AI_SNAPSHOT ||
        message_result ==
            DECK_COMPANION_SERVER_AI_SNAPSHOT_STORAGE_DEGRADED;
    if (!accepted_snapshot || message_result == DECK_COMPANION_SERVER_HEARTBEAT) {
        return false;
    }
    {
        const std::lock_guard<std::mutex> lock(link->mutex);
        if (!deck_companion_transport_accept_snapshot(
                &link->transport_authority,
                link->transport_generation
            )) {
            return false;
        }
    }
    if (message_result ==
        DECK_COMPANION_SERVER_AI_SNAPSHOT_STORAGE_DEGRADED) {
        increment_error(link);
    }
    return true;
}

void link_task(void *argument)
{
    auto *link = static_cast<deck_companion_link_t *>(argument);
    const EventBits_t startup = xEventGroupWaitBits(
        link->lifecycle,
        kStartBit | kAbortBit,
        pdTRUE,
        pdFALSE,
        portMAX_DELAY
    );
    if ((startup & kAbortBit) != 0) {
        xEventGroupSetBits(link->lifecycle, kStoppedBit);
        vTaskDelete(nullptr);
        return;
    }
    link->next_profile_poll_ms = 0;
    while (!link->stop_requested.load(std::memory_order_acquire)) {
        const uint64_t now = monotonic_ms();
        if (now >= link->next_profile_poll_ms) {
            (void)refresh_profile(link, now);
            link->next_profile_poll_ms = now + kProfilePollMs;
        }
        (void)advance_failover(link, now, DECK_COMPANION_FAILOVER_TICK);
        const bool serial_owner_safe =
            link->client != nullptr || ensure_serial_transport_revoked(link);
        if (link->secret != nullptr && link->client == nullptr &&
            serial_owner_safe &&
            now >= link->next_connect_ms && !start_transport(link)) {
            schedule_retry(link, now, true);
        }
        if (link->client != nullptr && deck_companion_link_timing_server_expired(
                                           &link->timing,
                                           now,
                                           kHeartbeatTimeoutMs
                                       )) {
            schedule_retry(link, now, true);
        }
        if (state_is_online(link) &&
            deck_companion_link_timing_client_due(&link->timing, now)) {
            if (!send_heartbeat(link, now)) {
                schedule_retry(link, now, true);
            } else {
                deck_companion_link_timing_client_sent(
                    &link->timing,
                    now,
                    kHeartbeatIntervalMs
                );
            }
        }
        if (state_is_online(link)) {
            if (link->serial_publication_dirty.exchange(
                    false,
                    std::memory_order_acq_rel
                )) {
                link->has_published_serial = false;
            }
            if (!send_pending_serial_owner_result(link) ||
                !send_serial_state_if_changed(link)) {
                schedule_retry(link, now, true);
            } else if (link->serial_stream_ready) {
                for (size_t index = 0; index < 8; ++index) {
                    bool progressed = false;
                    const bool accepted = link->serial_history_active
                                              ? send_one_history_serial_block(
                                                    link,
                                                    &progressed
                                                )
                                              : send_one_live_serial_block(
                                                    link,
                                                    &progressed
                                                );
                    if (!accepted) {
                        schedule_retry(link, monotonic_ms(), true);
                        break;
                    }
                    if (!progressed) {
                        break;
                    }
                }
            }
        }
        const uint32_t overflow_generation =
            link->queue_overflow_generation.exchange(0, std::memory_order_acq_rel);
        if (overflow_generation != 0 &&
            overflow_generation == link->transport_generation) {
            schedule_retry(link, now, true);
        }
        if (deck_ai_snapshot_store_take_storage_failure(link->snapshots)) {
            increment_error(link);
        }
        deck_ota_service_result_t ota_result{};
        if (link->ota != nullptr &&
            deck_ota_service_poll_result(link->ota, &ota_result)) {
            char message[320]{};
            size_t message_size = 0;
            const bool sent = deck_ota_protocol_format_result(
                                  &ota_result,
                                  message,
                                  sizeof(message),
                                  &message_size
                              ) &&
                              send_text(link, message, message_size);
            const bool reboot_required = sent && ota_result.reboot_required;
            secure_clear(message, sizeof(message));
            secure_clear(&ota_result, sizeof(ota_result));
            if (!sent) {
                schedule_retry(link, monotonic_ms(), true);
            } else if (reboot_required) {
                vTaskDelay(pdMS_TO_TICKS(100));
                esp_restart();
            }
        }

        TransportEvent event{};
        if (xQueueReceive(link->events, &event, kPollTicks) != pdTRUE) {
            continue;
        }
        if (event.type != TransportEventType::wake &&
            event.transport_generation != link->transport_generation) {
            secure_clear(&event, sizeof(event));
            continue;
        }
        if (event.type == TransportEventType::connected) {
            deck_companion_link_timing_begin_connection(
                &link->timing,
                monotonic_ms(),
                kHeartbeatTimeoutMs
            );
            link->has_server_monotonic = false;
            if (!send_hello(link)) {
                schedule_retry(link, monotonic_ms(), true);
            }
        } else if (event.type == TransportEventType::disconnected) {
            if (link->client != nullptr) {
                schedule_retry(link, monotonic_ms(), true);
            }
        } else if (event.type == TransportEventType::data &&
                   !accept_data(link, event)) {
            schedule_retry(link, monotonic_ms(), true);
        }
        secure_clear(&event, sizeof(event));
    }
    disconnect_transport(link);
    clear_secret(link);
    xEventGroupSetBits(link->lifecycle, kStoppedBit);
    vTaskDelete(nullptr);
}

}  // namespace

deck_companion_link_t *deck_companion_link_start(
    deck_companion_profiles_t *profiles,
    const char *firmware_version
)
{
    if (profiles == nullptr || !terminated(firmware_version, 33)) {
        return nullptr;
    }
    const size_t version_size = std::strlen(firmware_version);
    if (version_size == 0 || version_size >= 33) {
        return nullptr;
    }
    auto *link = new (std::nothrow) deck_companion_link_t{};
    if (link == nullptr) {
        return nullptr;
    }
    link->profiles = profiles;
    link->transport_callback.link = link;
    std::memcpy(link->firmware_version, firmware_version, version_size + 1);
    link->frame.reset(new (std::nothrow) char[kMaximumMessageBytes + 1]);
    deck_companion_link_frame_init(
        &link->frame_assembler,
        link->frame.get(),
        kMaximumMessageBytes
    );
    link->events = xQueueCreate(4, sizeof(TransportEvent));
    link->lifecycle = xEventGroupCreate();
    link->snapshot.state = DECK_COMPANION_LINK_UNPAIRED;
    if (link->frame == nullptr || link->events == nullptr ||
        link->lifecycle == nullptr ||
        !deck_companion_device_identity(
            link->device_id,
            sizeof(link->device_id),
            link->device_identity,
            sizeof(link->device_identity)
        ) ||
        xTaskCreate(
            link_task,
            "companion_link",
            kTaskStackBytes,
            link,
            kTaskPriority,
            &link->task
        ) != pdPASS) {
        if (link->events != nullptr) {
            vQueueDelete(link->events);
        }
        if (link->lifecycle != nullptr) {
            vEventGroupDelete(link->lifecycle);
        }
        secure_clear(link->device_identity, sizeof(link->device_identity));
        delete link;
        return nullptr;
    }

    deck_ai_snapshot_store_options_t snapshot_options{};
    const deck_ai_snapshot_store_options_t *snapshot_options_pointer = nullptr;
    if (deck_ai_snapshot_store_nvs_options(&snapshot_options)) {
        snapshot_options_pointer = &snapshot_options;
    }
    link->snapshots = deck_ai_snapshot_store_create(snapshot_options_pointer);
    link->ota = deck_ota_service_start(firmware_version);
    if (link->snapshots == nullptr || link->ota == nullptr) {
        xEventGroupSetBits(link->lifecycle, kAbortBit);
        const EventBits_t stopped = xEventGroupWaitBits(
            link->lifecycle,
            kStoppedBit,
            pdFALSE,
            pdTRUE,
            pdMS_TO_TICKS(2'000)
        );
        if ((stopped & kStoppedBit) == 0) {
            vTaskDelete(link->task);
        }
        if (link->snapshots != nullptr) {
            (void)deck_ai_snapshot_store_destroy(link->snapshots);
        }
        if (link->ota != nullptr) {
            (void)deck_ota_service_stop(link->ota);
        }
        vQueueDelete(link->events);
        vEventGroupDelete(link->lifecycle);
        secure_clear(link->device_identity, sizeof(link->device_identity));
        delete link;
        return nullptr;
    }
    xEventGroupSetBits(link->lifecycle, kStartBit);
    return link;
}

bool deck_companion_link_attach_serial(
    deck_companion_link_t *link,
    deck_serial_service_t *serial
)
{
    if (link == nullptr || serial == nullptr ||
        link->stop_requested.load(std::memory_order_acquire)) {
        return false;
    }
    {
        const std::lock_guard<std::mutex> lock(link->serial_mutex);
        if (link->serial != nullptr && link->serial != serial) {
            return false;
        }
        link->serial = serial;
        link->pending_serial_revoke_epoch = 0;
        link->serial_transport_fenced = true;
    }
    TransportEvent wake{};
    wake.type = TransportEventType::wake;
    link->serial_publication_dirty.store(true, std::memory_order_release);
    (void)xQueueSend(link->events, &wake, 0);
    return true;
}

bool deck_companion_link_detach_serial(
    deck_companion_link_t *link,
    deck_serial_service_t *serial
)
{
    if (link == nullptr || serial == nullptr) {
        return false;
    }
    const std::lock_guard<std::mutex> lock(link->serial_mutex);
    if (link->serial == nullptr) {
        return true;
    }
    if (link->serial != serial) {
        return false;
    }
    link->serial = nullptr;
    link->pending_serial_revoke_epoch = 0;
    link->serial_transport_fenced = true;
    link->serial_publication_dirty.store(true, std::memory_order_release);
    return true;
}

bool deck_companion_link_snapshot(
    const deck_companion_link_t *link,
    deck_companion_link_snapshot_t *snapshot
)
{
    if (link == nullptr || snapshot == nullptr) {
        return false;
    }
    const std::lock_guard<std::mutex> lock(link->mutex);
    *snapshot = link->snapshot;
    const uint64_t now = monotonic_ms();
    if (!deck_companion_trusted_clock_current(
            &link->trusted_clock,
            now,
            &snapshot->trusted_utc_ms
        )) {
        snapshot->has_trusted_utc = false;
        snapshot->trusted_utc_ms = 0;
    } else {
        snapshot->has_trusted_utc = true;
    }
    return true;
}

bool deck_companion_link_copy_ai_snapshot(
    const deck_companion_link_t *link,
    uint64_t now_utc_ms,
    char *document,
    size_t document_capacity,
    size_t *document_size,
    deck_ai_snapshot_store_snapshot_t *snapshot
)
{
    if (link == nullptr) {
        return false;
    }
    bool online = false;
    uint32_t transport_generation = 0;
    {
        const std::lock_guard<std::mutex> lock(link->mutex);
        transport_generation = link->transport_generation;
        online = link->snapshot.state == DECK_COMPANION_LINK_ONLINE &&
                 deck_companion_transport_snapshot_current(
                     &link->transport_authority,
                     link->transport_generation
                 );
    }
    if (!deck_ai_snapshot_store_copy(
        link->snapshots,
        now_utc_ms,
        online,
        document,
        document_capacity,
        document_size,
        snapshot
    )) {
        return false;
    }
    if (!online) {
        return true;
    }
    bool source_still_current = false;
    {
        const std::lock_guard<std::mutex> lock(link->mutex);
        source_still_current =
            transport_generation == link->transport_generation &&
            link->snapshot.state == DECK_COMPANION_LINK_ONLINE &&
            deck_companion_transport_snapshot_current(
                &link->transport_authority,
                transport_generation
            );
    }
    if (source_still_current) {
        return true;
    }
    // The transport changed while Store copy was in progress. Re-render the
    // same retained document through the offline path so this call cannot
    // publish a Fresh result from the retired source.
    return deck_ai_snapshot_store_copy(
        link->snapshots,
        now_utc_ms,
        false,
        document,
        document_capacity,
        document_size,
        snapshot
    );
}

bool deck_companion_link_stop(deck_companion_link_t *link)
{
    if (link == nullptr) {
        return true;
    }
    link->stop_requested.store(true, std::memory_order_release);
    TransportEvent wake{};
    wake.type = TransportEventType::wake;
    (void)xQueueSend(link->events, &wake, 0);
    const EventBits_t stopped = xEventGroupWaitBits(
        link->lifecycle,
        kStoppedBit,
        pdFALSE,
        pdTRUE,
        pdMS_TO_TICKS(8'000)
    );
    if ((stopped & kStoppedBit) == 0) {
        return false;
    }
    if (!deck_ota_service_stop(link->ota)) {
        return false;
    }
    link->ota = nullptr;
    if (!deck_ai_snapshot_store_destroy(link->snapshots)) {
        return false;
    }
    vQueueDelete(link->events);
    vEventGroupDelete(link->lifecycle);
    secure_clear(link->device_identity, sizeof(link->device_identity));
    if (link->frame != nullptr) {
        secure_clear(link->frame.get(), kMaximumMessageBytes + 1);
    }
    delete link;
    return true;
}
