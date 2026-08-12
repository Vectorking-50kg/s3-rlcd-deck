#include "deck_companion_profiles_nvs.h"

namespace {

constexpr char kNamespace[] = "deck_companion";

}  // namespace

deck_companion_profiles_nvs_storage_t *deck_companion_profiles_nvs_storage_open(void)
{
    return deck_transaction_nvs_storage_open(kNamespace);
}

void deck_companion_profiles_nvs_storage_close(
    deck_companion_profiles_nvs_storage_t *storage
)
{
    deck_transaction_nvs_storage_close(storage);
}

bool deck_companion_profiles_nvs_storage_adapter(
    deck_companion_profiles_nvs_storage_t *storage,
    deck_companion_storage_adapter_t *adapter
)
{
    return deck_transaction_nvs_storage_adapter(storage, adapter);
}
