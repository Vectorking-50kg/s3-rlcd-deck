#pragma once

#include "deck_wifi_config.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef deck_transaction_nvs_storage_t deck_wifi_nvs_storage_t;

/* nvs_flash_init() must succeed before opening this namespaced store. */
deck_wifi_nvs_storage_t *deck_wifi_nvs_storage_open(void);
void deck_wifi_nvs_storage_close(deck_wifi_nvs_storage_t *storage);
bool deck_wifi_nvs_storage_adapter(
    deck_wifi_nvs_storage_t *storage,
    deck_wifi_storage_adapter_t *adapter
);

#ifdef __cplusplus
}
#endif
