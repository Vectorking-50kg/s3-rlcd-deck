#include "deck_ai_snapshot_store_nvs.h"

#include "nvs_flash.h"

namespace {

constexpr char kPartition[] = "snapshot_nvs";
constexpr char kNamespace[] = "ai_snapshot";

bool open_storage(
    void *,
    deck_transaction_storage_adapter_t *adapter,
    void **handle
)
{
    if (adapter == nullptr || handle == nullptr ||
        nvs_flash_init_partition(kPartition) != ESP_OK) {
        return false;
    }
    auto *storage = deck_transaction_nvs_storage_open_from_partition(
        kPartition,
        kNamespace
    );
    if (storage == nullptr ||
        !deck_transaction_nvs_storage_adapter(storage, adapter)) {
        deck_transaction_nvs_storage_close(storage);
        return false;
    }
    *handle = storage;
    return true;
}

void close_storage(void *, void *handle)
{
    deck_transaction_nvs_storage_close(
        static_cast<deck_transaction_nvs_storage_t *>(handle)
    );
}

}  // namespace

bool deck_ai_snapshot_store_nvs_options(
    deck_ai_snapshot_store_options_t *options
)
{
    if (options == nullptr) {
        return false;
    }
    *options = deck_ai_snapshot_store_options_t{};
    options->provider = {open_storage, close_storage, nullptr};
    return true;
}
