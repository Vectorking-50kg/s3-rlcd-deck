#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "deck_transaction_store.h"

#ifdef __cplusplus
extern "C" {
#endif

#define DECK_COMPANION_PROFILE_VERSION 1
#define DECK_COMPANION_MAX_PROFILES 5
#define DECK_COMPANION_PROFILE_ID_CAPACITY 72
#define DECK_COMPANION_DISPLAY_NAME_CAPACITY 96
#define DECK_COMPANION_HUB_ADDRESS_CAPACITY 96
#define DECK_COMPANION_TOKEN_CAPACITY 44
#define DECK_COMPANION_FINGERPRINT_CAPACITY 72
#define DECK_COMPANION_CERTIFICATE_DER_CAPACITY 1024
#define DECK_COMPANION_PAIRING_CODE_CAPACITY 7

typedef struct deck_companion_profiles deck_companion_profiles_t;

typedef deck_transaction_storage_key_t deck_companion_storage_key_t;
typedef deck_transaction_storage_result_t deck_companion_storage_result_t;
typedef deck_transaction_storage_read_fn deck_companion_storage_read_fn;
typedef deck_transaction_storage_write_fn deck_companion_storage_write_fn;
typedef deck_transaction_storage_erase_fn deck_companion_storage_erase_fn;
typedef deck_transaction_storage_adapter_t deck_companion_storage_adapter_t;

#define DECK_COMPANION_STORAGE_CANDIDATE DECK_TRANSACTION_STORAGE_CANDIDATE
#define DECK_COMPANION_STORAGE_SLOT_0 DECK_TRANSACTION_STORAGE_SLOT_0
#define DECK_COMPANION_STORAGE_SLOT_1 DECK_TRANSACTION_STORAGE_SLOT_1
#define DECK_COMPANION_STORAGE_ACTIVE_MARKER DECK_TRANSACTION_STORAGE_ACTIVE_MARKER
#define DECK_COMPANION_STORAGE_KEY_COUNT DECK_TRANSACTION_STORAGE_KEY_COUNT
#define DECK_COMPANION_STORAGE_OK DECK_TRANSACTION_STORAGE_OK
#define DECK_COMPANION_STORAGE_NOT_FOUND DECK_TRANSACTION_STORAGE_NOT_FOUND
#define DECK_COMPANION_STORAGE_ERROR DECK_TRANSACTION_STORAGE_ERROR

typedef struct {
    char token[DECK_COMPANION_TOKEN_CAPACITY];
    char certificate_fingerprint[DECK_COMPANION_FINGERPRINT_CAPACITY];
    uint8_t certificate_der[DECK_COMPANION_CERTIFICATE_DER_CAPACITY];
    size_t certificate_der_size;
    uint8_t protocol_version;
} deck_companion_pairing_credential_t;

typedef bool (*deck_companion_pairing_redeem_fn)(
    void *context,
    const char *hub_address,
    const char *pairing_address,
    const char *pairing_code,
    deck_companion_pairing_credential_t *credential
);

typedef struct {
    deck_companion_pairing_redeem_fn redeem;
    void *context;
} deck_companion_pairing_adapter_t;

typedef struct {
    deck_companion_storage_adapter_t storage;
    deck_companion_pairing_adapter_t pairing;
} deck_companion_profiles_options_t;

typedef struct {
    char hub_address[DECK_COMPANION_HUB_ADDRESS_CAPACITY];
    /* Setup-AP peer address used only for the one-time trust bootstrap. */
    char pairing_address[DECK_COMPANION_HUB_ADDRESS_CAPACITY];
    char code[DECK_COMPANION_PAIRING_CODE_CAPACITY];
} deck_companion_pair_request_t;

typedef struct {
    uint8_t profile_version;
    char profile_id[DECK_COMPANION_PROFILE_ID_CAPACITY];
    char display_name[DECK_COMPANION_DISPLAY_NAME_CAPACITY];
    char hub_address[DECK_COMPANION_HUB_ADDRESS_CAPACITY];
    char certificate_fingerprint[DECK_COMPANION_FINGERPRINT_CAPACITY];
    int32_t priority;
    uint64_t last_success_unix_ms;
} deck_companion_profile_view_t;

typedef struct {
    char profile_id[DECK_COMPANION_PROFILE_ID_CAPACITY];
    char hub_address[DECK_COMPANION_HUB_ADDRESS_CAPACITY];
    char token[DECK_COMPANION_TOKEN_CAPACITY];
    char certificate_fingerprint[DECK_COMPANION_FINGERPRINT_CAPACITY];
    uint8_t certificate_der[DECK_COMPANION_CERTIFICATE_DER_CAPACITY];
    size_t certificate_der_size;
    uint8_t protocol_version;
} deck_companion_profile_secret_t;

typedef enum {
    DECK_COMPANION_RECORD_EMPTY = 0,
    DECK_COMPANION_RECORD_VALID,
    DECK_COMPANION_RECORD_RECOVERED_PREVIOUS,
    DECK_COMPANION_RECORD_CORRUPT,
    DECK_COMPANION_RECORD_UNSUPPORTED_SCHEMA,
    DECK_COMPANION_RECORD_MIGRATION_FAILED,
    DECK_COMPANION_RECORD_IO_ERROR,
} deck_companion_record_status_t;

typedef struct {
    deck_companion_record_status_t record_status;
    deck_companion_record_status_t candidate_record_status;
    bool storage_faulted;
    bool has_active;
    uint32_t generation;
    size_t count;
    char active_profile_id[DECK_COMPANION_PROFILE_ID_CAPACITY];
    deck_companion_profile_view_t profiles[DECK_COMPANION_MAX_PROFILES];
} deck_companion_profiles_snapshot_t;

typedef enum {
    DECK_COMPANION_PAIR_PAIRED = 0,
    DECK_COMPANION_PAIR_INVALID_ADDRESS,
    DECK_COMPANION_PAIR_INVALID_CODE,
    DECK_COMPANION_PAIR_REDEEM_FAILED,
    DECK_COMPANION_PAIR_INVALID_CREDENTIAL,
    DECK_COMPANION_PAIR_CAPACITY_REACHED,
    DECK_COMPANION_PAIR_STORAGE_FAILURE,
} deck_companion_pair_result_t;

typedef enum {
    DECK_COMPANION_PROFILE_UPDATED = 0,
    DECK_COMPANION_PROFILE_NOT_FOUND,
    DECK_COMPANION_PROFILE_INVALID_ARGUMENT,
    DECK_COMPANION_PROFILE_STORAGE_FAILURE,
} deck_companion_profile_update_result_t;

deck_companion_profiles_t *deck_companion_profiles_create(
    const deck_companion_profiles_options_t *options
);
void deck_companion_profiles_destroy(deck_companion_profiles_t *profiles);
deck_companion_pair_result_t deck_companion_profiles_pair(
    deck_companion_profiles_t *profiles,
    const deck_companion_pair_request_t *request
);
deck_companion_profile_update_result_t deck_companion_profiles_select_active(
    deck_companion_profiles_t *profiles,
    const char *profile_id
);
deck_companion_profile_update_result_t deck_companion_profiles_revoke(
    deck_companion_profiles_t *profiles,
    const char *profile_id
);
deck_companion_profile_update_result_t deck_companion_profiles_set_priority(
    deck_companion_profiles_t *profiles,
    const char *profile_id,
    int32_t priority
);
deck_companion_profile_update_result_t deck_companion_profiles_record_success(
    deck_companion_profiles_t *profiles,
    const char *profile_id,
    uint64_t unix_ms
);
bool deck_companion_profiles_snapshot(
    const deck_companion_profiles_t *profiles,
    deck_companion_profiles_snapshot_t *snapshot
);
bool deck_companion_profiles_active_secret(
    const deck_companion_profiles_t *profiles,
    deck_companion_profile_secret_t *secret
);
void deck_companion_profile_secret_clear(deck_companion_profile_secret_t *secret);
bool deck_companion_hub_address_valid(const char *hub_address);
bool deck_companion_hub_address_port(
    const char *hub_address,
    uint16_t *port
);
bool deck_companion_pairing_code_valid(const char *pairing_code);

#ifdef __cplusplus
}
#endif
