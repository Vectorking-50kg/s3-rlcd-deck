#pragma once

#include "deck_device_settings.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef deck_transaction_nvs_storage_t deck_device_settings_nvs_storage_t;

/* nvs_flash_init() must succeed before opening this namespaced store. */
deck_device_settings_nvs_storage_t *deck_device_settings_nvs_storage_open(void);
void deck_device_settings_nvs_storage_close(deck_device_settings_nvs_storage_t *storage);
bool deck_device_settings_nvs_storage_adapter(
    deck_device_settings_nvs_storage_t *storage,
    deck_device_settings_storage_adapter_t *adapter
);

#ifdef __cplusplus
}
#endif
