#pragma once

#include <stdbool.h>
#include <stdint.h>

#include "deck_companion_profiles.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef struct deck_lan_pairing deck_lan_pairing_t;

typedef enum {
    DECK_LAN_PAIRING_IDLE = 0,
    DECK_LAN_PAIRING_ACTIVE,
    DECK_LAN_PAIRING_AUTHENTICATING,
    DECK_LAN_PAIRING_PROOF_VERIFIED,
    DECK_LAN_PAIRING_PAIRED,
    DECK_LAN_PAIRING_EXPIRED,
    DECK_LAN_PAIRING_ERROR,
} deck_lan_pairing_state_t;

typedef struct {
    deck_lan_pairing_state_t state;
    char code[7];
    uint32_t remaining_seconds;
    uint32_t proof_count;
    const char *error_stage;
} deck_lan_pairing_event_t;

typedef void (*deck_lan_pairing_event_fn)(
    void *context,
    const deck_lan_pairing_event_t *event
);

/*
 * Starts the bounded Pairing v2 spike owner. No service is advertised until a
 * window is opened. The callback is invoked from the owner task and must not
 * retain the event or block.
 */
deck_lan_pairing_t *deck_lan_pairing_start(
    deck_companion_profiles_t *profiles,
    const char *firmware_version,
    deck_lan_pairing_event_fn callback,
    void *callback_context
);

/* Opens a fresh 120-second random-code window, replacing any active window. */
bool deck_lan_pairing_open(deck_lan_pairing_t *pairing);

/* Opens the first bounded window once when no Companion Profile exists. */
bool deck_lan_pairing_open_if_unpaired(deck_lan_pairing_t *pairing);

/* Cancels the current window without destroying the owner. */
bool deck_lan_pairing_cancel(deck_lan_pairing_t *pairing);

/* Bounded stop. A false result retains ownership so the caller can retry. */
bool deck_lan_pairing_stop(deck_lan_pairing_t *pairing);

#ifdef __cplusplus
}
#endif
