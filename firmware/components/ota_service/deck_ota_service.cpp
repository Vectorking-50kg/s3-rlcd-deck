#include "deck_ota_service.h"

#include "deck_ota_boot_health.h"
#include "deck_ota_signing_keys.h"

#include <atomic>
#include <cstring>
#include <new>

#include "esp_app_desc.h"
#include "esp_ota_ops.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"
#include "freertos/queue.h"
#include "freertos/task.h"
#include "psa/crypto.h"

namespace {

constexpr char kBoard[] = "esp32-s3-rlcd-4.2";
constexpr uint32_t kProtocolVersion = 1;
constexpr uint64_t kInactivityTimeoutMs = 30'000;
constexpr uint64_t kTransactionTimeoutMs = 10 * 60'000;
constexpr TickType_t kQueueTicks = pdMS_TO_TICKS(2'000);
constexpr EventBits_t kStoppedBit = BIT0;
constexpr EventBits_t kBootGuardStoppedBit = BIT0;
constexpr uint8_t kManifestMagic[] = {'S', '3', 'R', 'L', 'C', 'D', 'O', 'T', 'A', '1'};

enum class CommandKind : uint8_t { offer, chunk, stop };

struct Command {
    CommandKind kind = CommandKind::stop;
    char transaction_id[DECK_OTA_TRANSACTION_ID_CAPACITY]{};
    deck_ota_manifest_t manifest{};
    uint32_t offset = 0;
    size_t size = 0;
    bool final = false;
    uint8_t data[DECK_OTA_MAX_CHUNK_BYTES]{};
};

void secure_clear(void *value, size_t size)
{
    auto *bytes = static_cast<volatile uint8_t *>(value);
    while (size != 0) {
        *bytes++ = 0;
        --size;
    }
}

bool valid_transaction_id(const char *value)
{
    if (value == nullptr || std::strlen(value) != 32) {
        return false;
    }
    for (size_t index = 0; index < 32; ++index) {
        if (!((value[index] >= '0' && value[index] <= '9') ||
              (value[index] >= 'a' && value[index] <= 'f'))) {
            return false;
        }
    }
    return true;
}

void put_u32(uint8_t *output, uint32_t value)
{
    output[0] = static_cast<uint8_t>(value >> 24U);
    output[1] = static_cast<uint8_t>(value >> 16U);
    output[2] = static_cast<uint8_t>(value >> 8U);
    output[3] = static_cast<uint8_t>(value);
}

}  // namespace

struct deck_ota_service {
    QueueHandle_t commands = nullptr;
    QueueHandle_t results = nullptr;
    EventGroupHandle_t lifecycle = nullptr;
    TaskHandle_t task = nullptr;
    deck_ota_transaction_t *transaction = nullptr;
    deck_ota_transaction_options_t transaction_options{};
    deck_ota_manifest_t adapter_manifest{};
    esp_ota_handle_t ota_handle = 0;
    const esp_partition_t *update_partition = nullptr;
    psa_hash_operation_t hash = PSA_HASH_OPERATION_INIT;
    bool hash_active = false;
    char running_version[DECK_OTA_VERSION_CAPACITY]{};
    char active_transaction_id[DECK_OTA_TRANSACTION_ID_CAPACITY]{};
};

struct deck_ota_boot_guard {
    EventGroupHandle_t lifecycle = nullptr;
    TaskHandle_t task = nullptr;
    uint64_t deadline_ms = 0;
    std::atomic<bool> confirmed{false};
};

namespace {

bool flash_begin(void *context, size_t image_size)
{
    auto *service = static_cast<deck_ota_service_t *>(context);
    service->update_partition = esp_ota_get_next_update_partition(nullptr);
    if (service->update_partition == nullptr ||
        image_size > service->update_partition->size) {
        service->update_partition = nullptr;
        return false;
    }
    service->ota_handle = 0;
    return esp_ota_begin(
               service->update_partition,
               image_size,
               &service->ota_handle
           ) == ESP_OK;
}

bool flash_write(void *context, const uint8_t *data, size_t size)
{
    auto *service = static_cast<deck_ota_service_t *>(context);
    return service->ota_handle != 0 &&
           esp_ota_write(service->ota_handle, data, size) == ESP_OK;
}

bool flash_finish(void *context)
{
    auto *service = static_cast<deck_ota_service_t *>(context);
    if (service->ota_handle == 0 || esp_ota_end(service->ota_handle) != ESP_OK) {
        service->ota_handle = 0;
        return false;
    }
    service->ota_handle = 0;
    esp_app_desc_t description{};
    return service->update_partition != nullptr &&
           esp_ota_get_partition_description(
               service->update_partition,
               &description
           ) == ESP_OK &&
           std::strncmp(
               description.version,
               service->adapter_manifest.version,
               sizeof(description.version)
           ) == 0;
}

void flash_abort(void *context)
{
    auto *service = static_cast<deck_ota_service_t *>(context);
    if (service->ota_handle != 0) {
        (void)esp_ota_abort(service->ota_handle);
        service->ota_handle = 0;
    }
    service->update_partition = nullptr;
}

bool flash_select_boot(void *context)
{
    auto *service = static_cast<deck_ota_service_t *>(context);
    return service->update_partition != nullptr &&
           esp_ota_set_boot_partition(service->update_partition) == ESP_OK;
}

bool hash_begin(void *context)
{
    auto *service = static_cast<deck_ota_service_t *>(context);
    if (service->hash_active) {
        (void)psa_hash_abort(&service->hash);
    }
    service->hash = PSA_HASH_OPERATION_INIT;
    service->hash_active =
        psa_hash_setup(&service->hash, PSA_ALG_SHA_256) == PSA_SUCCESS;
    return service->hash_active;
}

bool hash_update(void *context, const uint8_t *data, size_t size)
{
    auto *service = static_cast<deck_ota_service_t *>(context);
    return service->hash_active &&
           psa_hash_update(&service->hash, data, size) == PSA_SUCCESS;
}

bool hash_finish(void *context, uint8_t output[DECK_OTA_DIGEST_BYTES])
{
    auto *service = static_cast<deck_ota_service_t *>(context);
    size_t output_size = 0;
    const bool finished = service->hash_active &&
                          psa_hash_finish(
                              &service->hash,
                              output,
                              DECK_OTA_DIGEST_BYTES,
                              &output_size
                          ) == PSA_SUCCESS &&
                          output_size == DECK_OTA_DIGEST_BYTES;
    if (!finished && service->hash_active) {
        (void)psa_hash_abort(&service->hash);
    }
    service->hash_active = false;
    service->hash = PSA_HASH_OPERATION_INIT;
    return finished;
}

void hash_abort(void *context)
{
    auto *service = static_cast<deck_ota_service_t *>(context);
    if (service->hash_active) {
        (void)psa_hash_abort(&service->hash);
        service->hash_active = false;
        service->hash = PSA_HASH_OPERATION_INIT;
    }
}

bool verify_manifest(void *, const deck_ota_manifest_t *manifest)
{
    const uint8_t *public_key = nullptr;
    size_t public_key_size = 0;
    if (manifest == nullptr ||
        !deck_ota_signing_public_key(
            manifest->signing_key_id,
            &public_key,
            &public_key_size
        )) {
        return false;
    }
    constexpr size_t kCanonicalSize = sizeof(kManifestMagic) + 12 + 48 + 32 + 32;
    uint8_t canonical[kCanonicalSize]{};
    size_t offset = 0;
    std::memcpy(canonical + offset, kManifestMagic, sizeof(kManifestMagic));
    offset += sizeof(kManifestMagic);
    put_u32(canonical + offset, manifest->signing_key_id);
    offset += 4;
    put_u32(canonical + offset, manifest->minimum_protocol_version);
    offset += 4;
    put_u32(canonical + offset, manifest->image_length);
    offset += 4;
    std::memcpy(canonical + offset, manifest->board, std::strlen(manifest->board));
    offset += 48;
    std::memcpy(canonical + offset, manifest->version, std::strlen(manifest->version));
    offset += 32;
    std::memcpy(canonical + offset, manifest->image_sha256, DECK_OTA_DIGEST_BYTES);

    psa_key_attributes_t attributes = PSA_KEY_ATTRIBUTES_INIT;
    psa_set_key_type(
        &attributes,
        PSA_KEY_TYPE_ECC_PUBLIC_KEY(PSA_ECC_FAMILY_SECP_R1)
    );
    psa_set_key_bits(&attributes, 256);
    psa_set_key_usage_flags(&attributes, PSA_KEY_USAGE_VERIFY_HASH);
    psa_set_key_algorithm(&attributes, PSA_ALG_ECDSA(PSA_ALG_SHA_256));
    psa_key_id_t key = 0;
    uint8_t digest[DECK_OTA_DIGEST_BYTES]{};
    size_t digest_size = 0;
    const bool valid =
        psa_import_key(
            &attributes,
            public_key,
            public_key_size,
            &key
        ) == PSA_SUCCESS &&
        psa_hash_compute(
            PSA_ALG_SHA_256,
            canonical,
            sizeof(canonical),
            digest,
            sizeof(digest),
            &digest_size
        ) == PSA_SUCCESS &&
        digest_size == sizeof(digest) &&
        psa_verify_hash(
            key,
            PSA_ALG_ECDSA(PSA_ALG_SHA_256),
            digest,
            sizeof(digest),
            manifest->signature,
            sizeof(manifest->signature)
        ) == PSA_SUCCESS;
    if (key != 0) {
        (void)psa_destroy_key(key);
    }
    psa_reset_key_attributes(&attributes);
    secure_clear(digest, sizeof(digest));
    secure_clear(canonical, sizeof(canonical));
    return valid;
}

deck_ota_transaction_t *create_transaction(deck_ota_service_t *service)
{
    return deck_ota_transaction_create(&service->transaction_options);
}

void publish_result(
    deck_ota_service_t *service,
    const char *transaction_id,
    deck_ota_result_t result
)
{
    deck_ota_service_result_t event{};
    std::memcpy(
        event.transaction_id,
        transaction_id,
        DECK_OTA_TRANSACTION_ID_CAPACITY
    );
    if (!deck_ota_transaction_snapshot(service->transaction, &event.transaction)) {
        event.transaction = {DECK_OTA_FAILED, result, 0, 0};
    }
    event.transaction.result = result;
    event.reboot_required = event.transaction.state == DECK_OTA_READY_TO_REBOOT;
    (void)xQueueSend(service->results, &event, kQueueTicks);
    secure_clear(&event, sizeof(event));
}

void publish_rejection(
    deck_ota_service_t *service,
    const char *transaction_id,
    deck_ota_result_t result
)
{
    deck_ota_service_result_t event{};
    std::memcpy(
        event.transaction_id,
        transaction_id,
        DECK_OTA_TRANSACTION_ID_CAPACITY
    );
    event.transaction = {DECK_OTA_FAILED, result, 0, 0};
    (void)xQueueSend(service->results, &event, kQueueTicks);
    secure_clear(&event, sizeof(event));
}

void ota_task(void *context)
{
    auto *service = static_cast<deck_ota_service_t *>(context);
    while (true) {
        Command command{};
        if (xQueueReceive(service->commands, &command, pdMS_TO_TICKS(250)) !=
            pdTRUE) {
            if (service->transaction != nullptr) {
                deck_ota_transaction_snapshot_t snapshot{};
                if (deck_ota_transaction_snapshot(service->transaction, &snapshot) &&
                    snapshot.state == DECK_OTA_RECEIVING) {
                    const deck_ota_result_t result = deck_ota_transaction_tick(
                        service->transaction,
                        static_cast<uint64_t>(esp_timer_get_time() / 1'000)
                    );
                    if (result == DECK_OTA_TIMED_OUT) {
                        publish_result(
                            service,
                            service->active_transaction_id,
                            result
                        );
                        secure_clear(
                            service->active_transaction_id,
                            sizeof(service->active_transaction_id)
                        );
                    }
                }
            }
            continue;
        }
        if (command.kind == CommandKind::stop) {
            secure_clear(&command, sizeof(command));
            break;
        }
        if (command.kind == CommandKind::offer) {
            if (service->transaction != nullptr) {
                deck_ota_transaction_snapshot_t active{};
                if (deck_ota_transaction_snapshot(service->transaction, &active) &&
                    active.state == DECK_OTA_RECEIVING) {
                    publish_rejection(
                        service,
                        command.transaction_id,
                        DECK_OTA_BUSY
                    );
                    secure_clear(&command, sizeof(command));
                    continue;
                }
                deck_ota_transaction_destroy(service->transaction);
            }
            service->transaction = create_transaction(service);
            service->adapter_manifest = command.manifest;
            std::memcpy(
                service->active_transaction_id,
                command.transaction_id,
                sizeof(service->active_transaction_id)
            );
            const deck_ota_result_t result = service->transaction == nullptr
                                                 ? DECK_OTA_FLASH_FAILURE
                                                 : deck_ota_transaction_offer(
                                                       service->transaction,
                                                       &command.manifest,
                                                       static_cast<uint64_t>(
                                                           esp_timer_get_time() / 1'000
                                                       )
                                                   );
            publish_result(service, command.transaction_id, result);
            if (result != DECK_OTA_OK) {
                secure_clear(
                    service->active_transaction_id,
                    sizeof(service->active_transaction_id)
                );
            }
        } else {
            if (service->transaction == nullptr ||
                std::strcmp(
                    service->active_transaction_id,
                    command.transaction_id
                ) != 0) {
                publish_rejection(
                    service,
                    command.transaction_id,
                    DECK_OTA_INVALID_MANIFEST
                );
                secure_clear(&command, sizeof(command));
                continue;
            }
            const deck_ota_result_t result =
                deck_ota_transaction_write(
                    service->transaction,
                    command.offset,
                    command.data,
                    command.size,
                    command.final,
                    static_cast<uint64_t>(esp_timer_get_time() / 1'000)
                );
            publish_result(service, command.transaction_id, result);
            if (result != DECK_OTA_OK || command.final) {
                secure_clear(
                    service->active_transaction_id,
                    sizeof(service->active_transaction_id)
                );
            }
        }
        secure_clear(&command, sizeof(command));
    }
    if (service->transaction != nullptr) {
        deck_ota_transaction_destroy(service->transaction);
        service->transaction = nullptr;
    }
    xEventGroupSetBits(service->lifecycle, kStoppedBit);
    vTaskSuspend(nullptr);
}

void boot_guard_task(void *context)
{
    auto *guard = static_cast<deck_ota_boot_guard_t *>(context);
    (void)ulTaskNotifyTake(pdTRUE, pdMS_TO_TICKS(guard->deadline_ms));
    if (guard->confirmed.load(std::memory_order_acquire)) {
        if (esp_ota_mark_app_valid_cancel_rollback() != ESP_OK) {
            (void)esp_ota_mark_app_invalid_rollback_and_reboot();
        }
    } else {
        (void)esp_ota_mark_app_invalid_rollback_and_reboot();
    }
    xEventGroupSetBits(guard->lifecycle, kBootGuardStoppedBit);
    vTaskSuspend(nullptr);
}

}  // namespace

deck_ota_service_t *deck_ota_service_start(const char *running_version)
{
    if (running_version == nullptr ||
        ::strnlen(running_version, DECK_OTA_VERSION_CAPACITY) >=
            DECK_OTA_VERSION_CAPACITY ||
        psa_crypto_init() != PSA_SUCCESS) {
        return nullptr;
    }
    auto *service = new (std::nothrow) deck_ota_service_t{};
    if (service == nullptr) {
        return nullptr;
    }
    std::strcpy(service->running_version, running_version);
    const esp_partition_t *partition = esp_ota_get_next_update_partition(nullptr);
    if (partition == nullptr) {
        delete service;
        return nullptr;
    }
    service->transaction_options.flash = {
        flash_begin,
        flash_write,
        flash_finish,
        flash_abort,
        flash_select_boot,
        service,
    };
    service->transaction_options.crypto = {
        hash_begin,
        hash_update,
        hash_finish,
        hash_abort,
        verify_manifest,
        service,
    };
    service->transaction_options.running_version = service->running_version;
    service->transaction_options.board = kBoard;
    service->transaction_options.protocol_version = kProtocolVersion;
    service->transaction_options.partition_capacity = partition->size;
    service->transaction_options.inactivity_timeout_ms = kInactivityTimeoutMs;
    service->transaction_options.maximum_duration_ms = kTransactionTimeoutMs;
    service->commands = xQueueCreate(2, sizeof(Command));
    service->results = xQueueCreate(2, sizeof(deck_ota_service_result_t));
    service->lifecycle = xEventGroupCreate();
    if (service->commands == nullptr || service->results == nullptr ||
        service->lifecycle == nullptr ||
        xTaskCreatePinnedToCore(
            ota_task,
            "ota_service",
            8'192,
            service,
            2,
            &service->task,
            0
        ) != pdPASS) {
        if (service->commands != nullptr) {
            vQueueDelete(service->commands);
        }
        if (service->results != nullptr) {
            vQueueDelete(service->results);
        }
        if (service->lifecycle != nullptr) {
            vEventGroupDelete(service->lifecycle);
        }
        delete service;
        return nullptr;
    }
    return service;
}

bool deck_ota_service_stop(deck_ota_service_t *service)
{
    if (service == nullptr) {
        return true;
    }
    Command command{};
    command.kind = CommandKind::stop;
    if (xQueueSendToFront(service->commands, &command, kQueueTicks) != pdTRUE ||
        (xEventGroupWaitBits(
             service->lifecycle,
             kStoppedBit,
             pdFALSE,
             pdTRUE,
             kQueueTicks
         ) &
         kStoppedBit) == 0) {
        return false;
    }
    vTaskDelete(service->task);
    vQueueDelete(service->commands);
    vQueueDelete(service->results);
    vEventGroupDelete(service->lifecycle);
    secure_clear(service->adapter_manifest.signature, sizeof(service->adapter_manifest.signature));
    secure_clear(service->running_version, sizeof(service->running_version));
    secure_clear(service->active_transaction_id, sizeof(service->active_transaction_id));
    delete service;
    return true;
}

bool deck_ota_service_offer(
    deck_ota_service_t *service,
    const char *transaction_id,
    const deck_ota_manifest_t *manifest
)
{
    if (service == nullptr || !valid_transaction_id(transaction_id) ||
        manifest == nullptr) {
        return false;
    }
    Command command{};
    command.kind = CommandKind::offer;
    std::strcpy(command.transaction_id, transaction_id);
    command.manifest = *manifest;
    const bool queued = xQueueSend(service->commands, &command, kQueueTicks) == pdTRUE;
    secure_clear(&command, sizeof(command));
    return queued;
}

bool deck_ota_service_write(
    deck_ota_service_t *service,
    const char *transaction_id,
    uint32_t offset,
    const uint8_t *data,
    size_t size,
    bool final
)
{
    if (service == nullptr || !valid_transaction_id(transaction_id) ||
        data == nullptr || size == 0 || size > DECK_OTA_MAX_CHUNK_BYTES) {
        return false;
    }
    Command command{};
    command.kind = CommandKind::chunk;
    std::strcpy(command.transaction_id, transaction_id);
    command.offset = offset;
    command.size = size;
    command.final = final;
    std::memcpy(command.data, data, size);
    const bool queued = xQueueSend(service->commands, &command, kQueueTicks) == pdTRUE;
    secure_clear(&command, sizeof(command));
    return queued;
}

bool deck_ota_service_poll_result(
    deck_ota_service_t *service,
    deck_ota_service_result_t *result
)
{
    return service != nullptr && result != nullptr &&
           xQueueReceive(service->results, result, 0) == pdTRUE;
}

deck_ota_boot_guard_t *deck_ota_boot_guard_start(uint64_t deadline_ms)
{
    const esp_partition_t *running = esp_ota_get_running_partition();
    esp_ota_img_states_t state = ESP_OTA_IMG_UNDEFINED;
    if (running == nullptr ||
        esp_ota_get_state_partition(running, &state) != ESP_OK ||
        state != ESP_OTA_IMG_PENDING_VERIFY) {
        return nullptr;
    }
    if (deadline_ms == 0 || deadline_ms > UINT32_MAX) {
        return nullptr;
    }
    auto *guard = new (std::nothrow) deck_ota_boot_guard_t{};
    if (guard == nullptr) {
        (void)esp_ota_mark_app_invalid_rollback_and_reboot();
        return nullptr;
    }
    guard->deadline_ms = deadline_ms;
    guard->lifecycle = xEventGroupCreate();
    if (guard->lifecycle == nullptr ||
        xTaskCreatePinnedToCore(
            boot_guard_task,
            "ota_boot_guard",
            3'072,
            guard,
            4,
            &guard->task,
            0
        ) != pdPASS) {
        if (guard->lifecycle != nullptr) {
            vEventGroupDelete(guard->lifecycle);
        }
        delete guard;
        (void)esp_ota_mark_app_invalid_rollback_and_reboot();
        return nullptr;
    }
    return guard;
}

bool deck_ota_boot_guard_confirm(
    deck_ota_boot_guard_t *guard,
    bool display_ready,
    bool peripherals_ready,
    bool wifi_subsystem_ready,
    bool companion_subsystem_ready
)
{
    if (guard == nullptr) {
        return true;
    }
    const deck_ota_boot_health_t health = {
        true,
        display_ready,
        peripherals_ready,
        wifi_subsystem_ready,
        companion_subsystem_ready,
        false,
        0,
        guard->deadline_ms,
    };
    if (deck_ota_boot_health_decide(&health) != DECK_OTA_BOOT_MARK_VALID) {
        return false;
    }
    guard->confirmed.store(true, std::memory_order_release);
    xTaskNotifyGive(guard->task);
    const EventBits_t stopped = xEventGroupWaitBits(
        guard->lifecycle,
        kBootGuardStoppedBit,
        pdFALSE,
        pdTRUE,
        pdMS_TO_TICKS(2'000)
    );
    if ((stopped & kBootGuardStoppedBit) == 0) {
        return false;
    }
    vTaskDelete(guard->task);
    vEventGroupDelete(guard->lifecycle);
    delete guard;
    return true;
}
