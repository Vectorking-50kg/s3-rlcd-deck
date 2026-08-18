#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "deck_companion_pairing_esp.h"
#include "deck_companion_profiles.h"
#include "deck_pairing_v2_contract.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef struct deck_pairing_v2_transaction deck_pairing_v2_transaction_t;

typedef bool (*deck_pairing_v2_random_fn)(void *context, uint8_t *output, size_t size);
typedef bool (*deck_pairing_v2_identity_fn)(
    void *context,
    char *device_id,
    size_t device_id_capacity,
    char *device_identity,
    size_t device_identity_capacity
);

typedef struct {
    deck_companion_profiles_t *profiles;
    deck_pairing_v2_crypto_t crypto;
    deck_pairing_v2_random_fn random;
    deck_pairing_v2_identity_fn identity;
    void *context;
} deck_pairing_v2_transaction_options_t;

typedef enum {
    DECK_PAIRING_V2_TRANSACTION_OK = 0,
    DECK_PAIRING_V2_TRANSACTION_INVALID,
    DECK_PAIRING_V2_TRANSACTION_CONFLICT,
    DECK_PAIRING_V2_TRANSACTION_LINK_REQUIRED,
    DECK_PAIRING_V2_TRANSACTION_CAPACITY_REACHED,
    DECK_PAIRING_V2_TRANSACTION_STORAGE_FAILURE,
} deck_pairing_v2_transaction_result_t;

typedef enum {
    DECK_PAIRING_V2_ACTION_NONE = 0,
    DECK_PAIRING_V2_ACTION_START_LINK_PROOF,
    DECK_PAIRING_V2_ACTION_PROFILE_COMMITTED,
} deck_pairing_v2_transaction_action_t;

typedef struct {
    char session_id[DECK_PAIRING_V2_ID_CAPACITY];
    char transaction_id[DECK_PAIRING_V2_ID_CAPACITY];
    char device_id[DECK_PAIRING_V2_DEVICE_ID_CAPACITY];
    char device_identity[DECK_PAIRING_V2_DEVICE_IDENTITY_CAPACITY];
    deck_companion_profile_secret_t secret;
} deck_pairing_v2_link_request_t;

deck_pairing_v2_transaction_t *deck_pairing_v2_transaction_create(
    const deck_pairing_v2_transaction_options_t *options
);

/* Starts a fresh window and cancels any prior uncommitted staged Profile. */
bool deck_pairing_v2_transaction_begin_window(
    deck_pairing_v2_transaction_t *transaction,
    const char *window_nonce
);

deck_pairing_v2_transaction_result_t deck_pairing_v2_transaction_exchange(
    deck_pairing_v2_transaction_t *transaction,
    const char *document,
    size_t document_size,
    char *response,
    size_t response_capacity,
    size_t *response_size,
    deck_pairing_v2_transaction_action_t *action
);

/* Copies the exact volatile secret for the restricted first WSS proof. */
bool deck_pairing_v2_transaction_link_request(
    const deck_pairing_v2_transaction_t *transaction,
    deck_pairing_v2_link_request_t *request
);

/* Marks proof only for the exact staged identity and records trusted server UTC. */
bool deck_pairing_v2_transaction_mark_link_proven(
    deck_pairing_v2_transaction_t *transaction,
    const char *session_id,
    const char *transaction_id,
    uint64_t server_utc_ms
);

void deck_pairing_v2_link_request_clear(deck_pairing_v2_link_request_t *request);
void deck_pairing_v2_transaction_reset(deck_pairing_v2_transaction_t *transaction);
void deck_pairing_v2_transaction_destroy(deck_pairing_v2_transaction_t *transaction);

#ifdef __cplusplus
}
#endif
