#pragma once

#include "deck_companion_profiles.h"

#ifdef __cplusplus
extern "C" {
#endif

/*
 * Production Pairing adapter. pairing_address must be the current Setup-AP
 * client's address; hub_address is the location persisted for normal WSS.
 */
bool deck_companion_pairing_esp_redeem(
    void *context,
    const char *hub_address,
    const char *pairing_address,
    const char *pairing_code,
    deck_companion_pairing_credential_t *credential
);

bool deck_companion_device_identity(
    char *device_id,
    size_t device_id_capacity,
    char *device_identity,
    size_t device_identity_capacity
);

#ifdef __cplusplus
}
#endif
