#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Public-key projection of protocol/catalog/ota-signing-keys-v1.json. */
bool deck_ota_signing_public_key(
    uint32_t key_id,
    const uint8_t **sec1,
    size_t *sec1_size
);

#ifdef __cplusplus
}
#endif
