#include "deck_device_settings_nvs.h"

namespace {

constexpr char kNamespace[] = "deck_settings";

}  // namespace

deck_device_settings_nvs_storage_t *deck_device_settings_nvs_storage_open(void)
{
    return deck_transaction_nvs_storage_open(kNamespace);
}

void deck_device_settings_nvs_storage_close(deck_device_settings_nvs_storage_t *storage)
{
    deck_transaction_nvs_storage_close(storage);
}

bool deck_device_settings_nvs_storage_adapter(
    deck_device_settings_nvs_storage_t *storage,
    deck_device_settings_storage_adapter_t *adapter
)
{
    return deck_transaction_nvs_storage_adapter(storage, adapter);
}
