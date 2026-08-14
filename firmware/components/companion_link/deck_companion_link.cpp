#include "deck_companion_link.h"

#include "deck_ai_snapshot_store_nvs.h"
#include "deck_companion_link_frame.h"
#include "deck_companion_link_message.h"
#include "deck_companion_link_timing.h"
#include "deck_companion_pairing_esp.h"
#include "deck_device_protocol.h"

#include <atomic>
#include <cstdio>
#include <cstring>
#include <ctime>
#include <memory>
#include <mutex>
#include <new>

#include "esp_timer.h"
#include "esp_websocket_client.h"
#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"
#include "freertos/queue.h"
#include "freertos/task.h"
#include "psa/crypto.h"

namespace {

constexpr uint32_t kTaskStackBytes = 8'192;
constexpr UBaseType_t kTaskPriority = 2;
constexpr TickType_t kPollTicks = pdMS_TO_TICKS(100);
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
    int payload_length = 0;
    int payload_offset = 0;
    uint8_t opcode = 0;
    bool final = false;
    size_t data_size = 0;
    char data[kFrameChunkBytes]{};
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
    uint32_t observed_profile_generation = 0;
    uint64_t next_profile_poll_ms = 0;
    uint64_t next_connect_ms = 0;
    deck_companion_link_timing_t timing{};
    uint64_t server_utc_ms = 0;
    uint64_t server_monotonic_ms = 0;
    bool has_server_monotonic = false;
    deck_companion_trusted_clock_t trusted_clock{};
    std::atomic<bool> stop_requested{false};
    std::atomic<bool> queue_overflow{false};
    mutable std::mutex mutex;
    deck_companion_link_snapshot_t snapshot{};
};

namespace {

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

void clear_secret(deck_companion_link_t *link)
{
    if (link->secret != nullptr) {
        deck_companion_profile_secret_clear(link->secret.get());
        link->secret.reset();
    }
}

void disconnect_transport(deck_companion_link_t *link)
{
    esp_websocket_client_handle_t client = link->client;
    link->client = nullptr;
    if (client != nullptr) {
        (void)esp_websocket_client_stop(client);
        (void)esp_websocket_client_destroy(client);
    }
    if (link->frame != nullptr && link->frame_assembler.message_size != 0) {
        secure_clear(link->frame.get(), link->frame_assembler.message_size);
    }
    deck_companion_link_frame_reset(&link->frame_assembler);
    link->has_server_monotonic = false;
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
}

void websocket_event(
    void *argument,
    esp_event_base_t,
    int32_t event_id,
    void *event_data
)
{
    auto *link = static_cast<deck_companion_link_t *>(argument);
    if (link == nullptr || link->stop_requested.load(std::memory_order_acquire)) {
        return;
    }
    TransportEvent event{};
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
            link->queue_overflow.store(true, std::memory_order_release);
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
        link->queue_overflow.store(true, std::memory_order_release);
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
    bool started = client != nullptr &&
                   esp_websocket_register_events(
                       client,
                       WEBSOCKET_EVENT_ANY,
                       websocket_event,
                       link
                   ) == ESP_OK &&
                   esp_websocket_client_start(client) == ESP_OK;
    secure_clear(headers, sizeof(headers));
    if (!started) {
        if (client != nullptr) {
            (void)esp_websocket_client_destroy(client);
        }
        return false;
    }
    link->client = client;
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

bool send_hello(deck_companion_link_t *link)
{
    char message[320]{};
    const int size = std::snprintf(
        message,
        sizeof(message),
        "{\"type\":\"device.hello\",\"protocol_version\":1,\"device_id\":\"%s\",\"firmware_version\":\"%s\",\"board\":\"%s\",\"capabilities\":[\"display\"],\"serial_state\":\"disarmed\"}",
        link->device_id,
        link->firmware_version,
        kBoard
    );
    return size > 0 && static_cast<size_t>(size) < sizeof(message) &&
           send_text(link, message, static_cast<size_t>(size));
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

bool refresh_profile(deck_companion_link_t *link, uint64_t now)
{
    deck_companion_profiles_snapshot_t profiles{};
    if (!deck_companion_profiles_snapshot(link->profiles, &profiles)) {
        increment_error(link);
        return false;
    }
    const bool changed = profiles.generation != link->observed_profile_generation ||
                         profiles.has_active !=
                             (link->secret != nullptr);
    if (!changed) {
        return true;
    }
    disconnect_transport(link);
    clear_secret(link);
    link->observed_profile_generation = profiles.generation;
    {
        const std::lock_guard<std::mutex> lock(link->mutex);
        link->snapshot.has_active_profile = profiles.has_active;
        link->snapshot.profile_generation = profiles.generation;
        link->snapshot.reconnect_attempts = 0;
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
            link->snapshot.state = DECK_COMPANION_LINK_OFFLINE;
        } else {
            link->snapshot.state = DECK_COMPANION_LINK_UNPAIRED;
        }
    }
    if (!profiles.has_active) {
        return true;
    }
    std::unique_ptr<deck_companion_profile_secret_t> secret(
        new (std::nothrow) deck_companion_profile_secret_t{}
    );
    if (secret == nullptr ||
        !deck_companion_profiles_active_secret(link->profiles, secret.get())) {
        if (secret != nullptr) {
            deck_companion_profile_secret_clear(secret.get());
        }
        increment_error(link);
        return false;
    }
    link->secret = std::move(secret);
    link->next_connect_ms = now;
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
    link->frame[message_size] = '\0';
    deck_device_heartbeat_t heartbeat{};
    const uint64_t now = monotonic_ms();
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
    if (message_result != DECK_COMPANION_SERVER_HEARTBEAT) {
        secure_clear(link->frame.get(), message_size + 1);
        deck_companion_link_frame_reset(&link->frame_assembler);
        if (message_result ==
            DECK_COMPANION_SERVER_AI_SNAPSHOT_STORAGE_DEGRADED) {
            increment_error(link);
            return true;
        }
        return message_result == DECK_COMPANION_SERVER_AI_SNAPSHOT;
    }
    secure_clear(link->frame.get(), message_size + 1);
    deck_companion_link_frame_reset(&link->frame_assembler);
    const bool first_valid_heartbeat =
        link->timing.last_server_heartbeat_ms == 0 || !state_is_online(link);
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
    if (first_valid_heartbeat && link->secret != nullptr) {
        (void)deck_companion_profiles_record_success(
            link->profiles,
            link->secret->profile_id,
            heartbeat.utc_unix_ms
        );
        deck_companion_profiles_snapshot_t profiles{};
        if (deck_companion_profiles_snapshot(link->profiles, &profiles)) {
            link->observed_profile_generation = profiles.generation;
            const std::lock_guard<std::mutex> lock(link->mutex);
            link->snapshot.profile_generation = profiles.generation;
        }
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
        if (link->secret != nullptr && link->client == nullptr &&
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
        if (link->queue_overflow.exchange(false, std::memory_order_acq_rel)) {
            schedule_retry(link, now, true);
        }
        if (deck_ai_snapshot_store_take_storage_failure(link->snapshots)) {
            increment_error(link);
        }

        TransportEvent event{};
        if (xQueueReceive(link->events, &event, kPollTicks) != pdTRUE) {
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
    if (link->snapshots == nullptr) {
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
        vQueueDelete(link->events);
        vEventGroupDelete(link->lifecycle);
        secure_clear(link->device_identity, sizeof(link->device_identity));
        delete link;
        return nullptr;
    }
    xEventGroupSetBits(link->lifecycle, kStartBit);
    return link;
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
    {
        const std::lock_guard<std::mutex> lock(link->mutex);
        online = link->snapshot.state == DECK_COMPANION_LINK_ONLINE;
    }
    return deck_ai_snapshot_store_copy(
        link->snapshots,
        now_utc_ms,
        online,
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
