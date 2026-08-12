#include "deck_wifi_config_nvs.h"

namespace {

constexpr char kNamespace[] = "deck_wifi";

}  // namespace

deck_wifi_nvs_storage_t *deck_wifi_nvs_storage_open(void)
{
    return deck_transaction_nvs_storage_open(kNamespace);
}

void deck_wifi_nvs_storage_close(deck_wifi_nvs_storage_t *storage)
{
    deck_transaction_nvs_storage_close(storage);
}

bool deck_wifi_nvs_storage_adapter(
    deck_wifi_nvs_storage_t *storage,
    deck_wifi_storage_adapter_t *adapter
)
{
    return deck_transaction_nvs_storage_adapter(storage, adapter);
}
