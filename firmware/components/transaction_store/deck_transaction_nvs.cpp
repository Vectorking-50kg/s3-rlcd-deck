#include "deck_transaction_store.h"

#include <new>

#include "nvs.h"

namespace {

const char *key_name(deck_transaction_storage_key_t key)
{
    switch (key) {
        case DECK_TRANSACTION_STORAGE_CANDIDATE:
            return "candidate";
        case DECK_TRANSACTION_STORAGE_SLOT_0:
            return "slot0";
        case DECK_TRANSACTION_STORAGE_SLOT_1:
            return "slot1";
        case DECK_TRANSACTION_STORAGE_ACTIVE_MARKER:
            return "active";
        case DECK_TRANSACTION_STORAGE_METADATA:
            return "metadata";
        case DECK_TRANSACTION_STORAGE_KEY_COUNT:
        default:
            return nullptr;
    }
}

}  // namespace

struct deck_transaction_nvs_storage {
    nvs_handle_t handle;
};

namespace {

deck_transaction_storage_result_t read_value(
    void *context,
    deck_transaction_storage_key_t key,
    uint8_t *output,
    size_t capacity,
    size_t *size
)
{
    auto *storage = static_cast<deck_transaction_nvs_storage_t *>(context);
    const char *name = key_name(key);
    if (storage == nullptr || name == nullptr || output == nullptr || size == nullptr) {
        return DECK_TRANSACTION_STORAGE_ERROR;
    }
    size_t available = capacity;
    const esp_err_t result = nvs_get_blob(storage->handle, name, output, &available);
    if (result == ESP_ERR_NVS_NOT_FOUND) {
        return DECK_TRANSACTION_STORAGE_NOT_FOUND;
    }
    if (result != ESP_OK) {
        return DECK_TRANSACTION_STORAGE_ERROR;
    }
    *size = available;
    return DECK_TRANSACTION_STORAGE_OK;
}

bool write_value(
    void *context,
    deck_transaction_storage_key_t key,
    const uint8_t *data,
    size_t size
)
{
    auto *storage = static_cast<deck_transaction_nvs_storage_t *>(context);
    const char *name = key_name(key);
    return storage != nullptr && name != nullptr && data != nullptr && size != 0 &&
           nvs_set_blob(storage->handle, name, data, size) == ESP_OK &&
           nvs_commit(storage->handle) == ESP_OK;
}

bool erase_value(void *context, deck_transaction_storage_key_t key)
{
    auto *storage = static_cast<deck_transaction_nvs_storage_t *>(context);
    const char *name = key_name(key);
    if (storage == nullptr || name == nullptr) {
        return false;
    }
    const esp_err_t result = nvs_erase_key(storage->handle, name);
    return result == ESP_ERR_NVS_NOT_FOUND ||
           (result == ESP_OK && nvs_commit(storage->handle) == ESP_OK);
}

}  // namespace

deck_transaction_nvs_storage_t *deck_transaction_nvs_storage_open(
    const char *namespace_name
)
{
    if (namespace_name == nullptr) {
        return nullptr;
    }
    auto *storage = new (std::nothrow) deck_transaction_nvs_storage_t{};
    if (storage == nullptr) {
        return nullptr;
    }
    if (nvs_open(namespace_name, NVS_READWRITE, &storage->handle) != ESP_OK) {
        delete storage;
        return nullptr;
    }
    return storage;
}

deck_transaction_nvs_storage_t *deck_transaction_nvs_storage_open_from_partition(
    const char *partition_name,
    const char *namespace_name
)
{
    if (partition_name == nullptr || namespace_name == nullptr) {
        return nullptr;
    }
    auto *storage = new (std::nothrow) deck_transaction_nvs_storage_t{};
    if (storage == nullptr) {
        return nullptr;
    }
    if (nvs_open_from_partition(
            partition_name,
            namespace_name,
            NVS_READWRITE,
            &storage->handle
        ) != ESP_OK) {
        delete storage;
        return nullptr;
    }
    return storage;
}

void deck_transaction_nvs_storage_close(deck_transaction_nvs_storage_t *storage)
{
    if (storage != nullptr) {
        nvs_close(storage->handle);
        delete storage;
    }
}

bool deck_transaction_nvs_storage_adapter(
    deck_transaction_nvs_storage_t *storage,
    deck_transaction_storage_adapter_t *adapter
)
{
    if (storage == nullptr || adapter == nullptr) {
        return false;
    }
    *adapter = {read_value, write_value, erase_value, storage};
    return true;
}
