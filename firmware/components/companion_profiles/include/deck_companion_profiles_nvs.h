#pragma once

#include "deck_companion_profiles.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef deck_transaction_nvs_storage_t deck_companion_profiles_nvs_storage_t;

/* nvs_flash_init_partition("companion_nvs") must succeed before opening. */
deck_companion_profiles_nvs_storage_t *deck_companion_profiles_nvs_storage_open(void);
void deck_companion_profiles_nvs_storage_close(
    deck_companion_profiles_nvs_storage_t *storage
);
bool deck_companion_profiles_nvs_storage_adapter(
    deck_companion_profiles_nvs_storage_t *storage,
    deck_companion_storage_adapter_t *adapter
);

#ifdef __cplusplus
}
#endif
