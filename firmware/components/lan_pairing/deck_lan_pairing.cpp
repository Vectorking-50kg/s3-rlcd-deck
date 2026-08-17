#include "deck_lan_pairing.h"

#include <atomic>
#include <climits>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <inttypes.h>
#include <new>

#include "esp_srp.h"
#include "esp_mac.h"
#include "esp_random.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"
#include "freertos/queue.h"
#include "freertos/task.h"
#include "mdns.h"
#include "protocomm.h"
#include "protocomm_httpd.h"
#include "protocomm_security2.h"

namespace {

constexpr char kPairingUsername[] = "s3deck-pairing-v2";
constexpr char kSecurityEndpoint[] = "pairing/session";
constexpr char kProofEndpoint[] = "pairing/proof";
constexpr char kProofRequest[] = "pairing-v2-spike";
constexpr char kProofResponse[] = "proof-verified";
constexpr char kServiceType[] = "_s3rlcd-pair";
constexpr char kServiceProtocol[] = "_tcp";
constexpr uint16_t kPairingPort = 3232;
constexpr int64_t kWindowLifetimeUs = 120'000'000;
constexpr EventBits_t kOwnerStopped = BIT0;
constexpr TickType_t kQueuePoll = pdMS_TO_TICKS(100);
constexpr TickType_t kStopTimeout = pdMS_TO_TICKS(2'000);

enum class CommandType : uint8_t {
    open,
    cancel,
    proof_verified,
    stop,
};

struct Command {
    CommandType type;
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
    deck_lan_pairing_event_fn callback;
    void *callback_context;
    QueueHandle_t commands;
    EventGroupHandle_t lifecycle;
    TaskHandle_t task;
    protocomm_t *protocomm;
    protocomm_security2_params_t security;
    char *salt;
    char *verifier;
    int verifier_length;
    char code[7];
    char window_id[23];
    char hostname[40];
    char instance[48];
    int64_t expires_at_us;
    uint32_t proof_count;
    uint32_t last_remaining_seconds;
    bool mdns_initialized;
    bool advertised;
    bool transport_started;
    std::atomic<bool> stop_requested;
};

namespace {

void publish(deck_lan_pairing_t *pairing, deck_lan_pairing_state_t state, const char *error)
{
    if (pairing->callback == nullptr) {
        return;
    }
    uint32_t remaining = 0;
    if (state == DECK_LAN_PAIRING_ACTIVE || state == DECK_LAN_PAIRING_PROOF_VERIFIED) {
        const int64_t delta = pairing->expires_at_us - esp_timer_get_time();
        if (delta > 0) {
            remaining = static_cast<uint32_t>((delta + 999'999) / 1'000'000);
        }
    }
    deck_lan_pairing_event_t event{};
    event.state = state;
    if (state == DECK_LAN_PAIRING_ACTIVE || state == DECK_LAN_PAIRING_PROOF_VERIFIED) {
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
}

bool stop_window(deck_lan_pairing_t *pairing)
{
    bool success = true;
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
    if (pairing->protocomm != nullptr) {
        protocomm_delete(pairing->protocomm);
        pairing->protocomm = nullptr;
    }
    clear_window_secrets(pairing);
    return true;
}

esp_err_t proof_handler(
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
        output_length == nullptr || input_length != sizeof(kProofRequest) - 1 ||
        std::memcmp(input, kProofRequest, sizeof(kProofRequest) - 1) != 0) {
        return ESP_ERR_INVALID_ARG;
    }
    auto *response = static_cast<uint8_t *>(std::malloc(sizeof(kProofResponse) - 1));
    if (response == nullptr) {
        return ESP_ERR_NO_MEM;
    }
    std::memcpy(response, kProofResponse, sizeof(kProofResponse) - 1);
    *output = response;
    *output_length = sizeof(kProofResponse) - 1;
    const Command command{CommandType::proof_verified};
    if (xQueueSend(pairing->commands, &command, 0) != pdTRUE) {
        secure_clear(response, sizeof(kProofResponse) - 1);
        std::free(response);
        *output = nullptr;
        *output_length = 0;
        return ESP_ERR_TIMEOUT;
    }
    return ESP_OK;
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
    secure_clear(window_bytes, sizeof(window_bytes));
    if (!generated) {
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
            kProofEndpoint,
            proof_handler,
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
                case CommandType::proof_verified:
                    ++pairing->proof_count;
                    publish(pairing, DECK_LAN_PAIRING_PROOF_VERIFIED, nullptr);
                    break;
                case CommandType::stop:
                    pairing->stop_requested.store(true);
                    break;
            }
        }
        if (pairing->expires_at_us > 0) {
            const int64_t now = esp_timer_get_time();
            if (now >= pairing->expires_at_us) {
                if (stop_window(pairing)) {
                    publish(pairing, DECK_LAN_PAIRING_EXPIRED, nullptr);
                } else {
                    publish(pairing, DECK_LAN_PAIRING_ERROR, "expiry_stop");
                }
            } else {
                const uint32_t remaining =
                    static_cast<uint32_t>((pairing->expires_at_us - now + 999'999) / 1'000'000);
                if (remaining != pairing->last_remaining_seconds) {
                    pairing->last_remaining_seconds = remaining;
                    publish(pairing, DECK_LAN_PAIRING_ACTIVE, nullptr);
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
    deck_lan_pairing_event_fn callback,
    void *callback_context
)
{
    auto *pairing = new (std::nothrow) deck_lan_pairing_t{};
    if (pairing == nullptr) {
        return nullptr;
    }
    pairing->callback = callback;
    pairing->callback_context = callback_context;
    pairing->commands = xQueueCreate(4, sizeof(Command));
    pairing->lifecycle = xEventGroupCreate();
    if (pairing->commands == nullptr || pairing->lifecycle == nullptr ||
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
        if (pairing->lifecycle != nullptr) {
            vEventGroupDelete(pairing->lifecycle);
        }
        delete pairing;
        return nullptr;
    }
    return pairing;
}

bool deck_lan_pairing_open(deck_lan_pairing_t *pairing)
{
    return send_command(pairing, CommandType::open);
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
    vEventGroupDelete(pairing->lifecycle);
    secure_clear(pairing, sizeof(*pairing));
    delete pairing;
    return true;
}
