#pragma once

#include "deck_ai_snapshot_store.h"

#ifdef __cplusplus
extern "C" {
#endif

/* Configures delayed open/close of snapshot_nvs on the Store private worker. */
bool deck_ai_snapshot_store_nvs_options(
    deck_ai_snapshot_store_options_t *options
);

#ifdef __cplusplus
}
#endif
