#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "deck_transaction_store.h"

#ifdef __cplusplus
extern "C" {
#endif

#define DECK_WIFI_SSID_CAPACITY 33
#define DECK_WIFI_PASSWORD_CAPACITY 64

typedef struct deck_wifi_config deck_wifi_config_t;

typedef struct {
    char ssid[DECK_WIFI_SSID_CAPACITY];
    char password[DECK_WIFI_PASSWORD_CAPACITY];
} deck_wifi_credentials_t;

typedef deck_transaction_storage_key_t deck_wifi_storage_key_t;
typedef deck_transaction_storage_result_t deck_wifi_storage_result_t;
typedef deck_transaction_storage_read_fn deck_wifi_storage_read_fn;
typedef deck_transaction_storage_write_fn deck_wifi_storage_write_fn;
typedef deck_transaction_storage_erase_fn deck_wifi_storage_erase_fn;
typedef deck_transaction_storage_adapter_t deck_wifi_storage_adapter_t;

#define DECK_WIFI_STORAGE_CANDIDATE DECK_TRANSACTION_STORAGE_CANDIDATE
#define DECK_WIFI_STORAGE_SLOT_0 DECK_TRANSACTION_STORAGE_SLOT_0
#define DECK_WIFI_STORAGE_SLOT_1 DECK_TRANSACTION_STORAGE_SLOT_1
#define DECK_WIFI_STORAGE_ACTIVE_MARKER DECK_TRANSACTION_STORAGE_ACTIVE_MARKER
#define DECK_WIFI_STORAGE_KEY_COUNT DECK_TRANSACTION_STORAGE_KEY_COUNT
#define DECK_WIFI_STORAGE_OK DECK_TRANSACTION_STORAGE_OK
#define DECK_WIFI_STORAGE_NOT_FOUND DECK_TRANSACTION_STORAGE_NOT_FOUND
#define DECK_WIFI_STORAGE_ERROR DECK_TRANSACTION_STORAGE_ERROR

typedef bool (*deck_wifi_validation_begin_fn)(
    void *context,
    const deck_wifi_credentials_t *credentials
);
typedef void (*deck_wifi_validation_cancel_fn)(void *context);

typedef struct {
    deck_wifi_validation_begin_fn begin;
    deck_wifi_validation_cancel_fn cancel;
    void *context;
} deck_wifi_validation_adapter_t;

typedef struct {
    deck_wifi_storage_adapter_t storage;
    deck_wifi_validation_adapter_t validation;
    uint64_t validation_timeout_ms;
} deck_wifi_config_options_t;

typedef enum {
    DECK_WIFI_CONFIG_NO_ACTIVE = 0,
    DECK_WIFI_CONFIG_ACTIVE,
    DECK_WIFI_CONFIG_VALIDATING,
    DECK_WIFI_CONFIG_AUTH_FAILED,
    DECK_WIFI_CONFIG_TIMED_OUT,
    DECK_WIFI_CONFIG_CONNECTION_FAILED,
    DECK_WIFI_CONFIG_STORAGE_ERROR,
} deck_wifi_config_state_t;

typedef enum {
    DECK_WIFI_RECORD_EMPTY = 0,
    DECK_WIFI_RECORD_VALID,
    DECK_WIFI_RECORD_RECOVERED_PREVIOUS,
    DECK_WIFI_RECORD_CORRUPT,
    DECK_WIFI_RECORD_UNSUPPORTED_SCHEMA,
    DECK_WIFI_RECORD_MIGRATION_FAILED,
    DECK_WIFI_RECORD_IO_ERROR,
} deck_wifi_record_status_t;

const char *deck_wifi_config_state_name(deck_wifi_config_state_t state);
const char *deck_wifi_record_status_name(deck_wifi_record_status_t status);
void deck_wifi_credentials_clear(deck_wifi_credentials_t *credentials);

typedef struct {
    deck_wifi_config_state_t state;
    deck_wifi_record_status_t record_status;
    deck_wifi_record_status_t candidate_record_status;
    bool has_active;
    bool has_candidate;
    uint32_t generation;
    char active_ssid[DECK_WIFI_SSID_CAPACITY];
    char candidate_ssid[DECK_WIFI_SSID_CAPACITY];
} deck_wifi_config_snapshot_t;

typedef enum {
    DECK_WIFI_SUBMIT_ACCEPTED = 0,
    DECK_WIFI_SUBMIT_INVALID_SSID,
    DECK_WIFI_SUBMIT_INVALID_PASSWORD,
    DECK_WIFI_SUBMIT_BUSY,
    DECK_WIFI_SUBMIT_STORAGE_ERROR,
    DECK_WIFI_SUBMIT_WIFI_ERROR,
} deck_wifi_submit_result_t;

typedef enum {
    DECK_WIFI_VALIDATION_CONNECTED = 0,
    DECK_WIFI_VALIDATION_AUTH_FAILED,
    DECK_WIFI_VALIDATION_CONNECTION_FAILED,
} deck_wifi_validation_result_t;

typedef enum {
    DECK_WIFI_CLEAR_CLEARED = 0,
    DECK_WIFI_CLEAR_STORAGE_ERROR,
} deck_wifi_clear_result_t;

deck_wifi_config_t *deck_wifi_config_create(const deck_wifi_config_options_t *options);
void deck_wifi_config_destroy(deck_wifi_config_t *config);

deck_wifi_submit_result_t deck_wifi_config_submit(
    deck_wifi_config_t *config,
    const deck_wifi_credentials_t *credentials,
    uint64_t now_ms
);
bool deck_wifi_config_validation_result(
    deck_wifi_config_t *config,
    deck_wifi_validation_result_t result
);
bool deck_wifi_config_tick(deck_wifi_config_t *config, uint64_t now_ms);
bool deck_wifi_config_active_connection(deck_wifi_config_t *config, bool connected);
deck_wifi_clear_result_t deck_wifi_config_clear(deck_wifi_config_t *config);
bool deck_wifi_config_snapshot(
    const deck_wifi_config_t *config,
    deck_wifi_config_snapshot_t *snapshot
);
bool deck_wifi_config_recovery_required(const deck_wifi_config_snapshot_t *snapshot);
bool deck_wifi_config_active_credentials(
    const deck_wifi_config_t *config,
    deck_wifi_credentials_t *credentials
);

#ifdef __cplusplus
}
#endif
