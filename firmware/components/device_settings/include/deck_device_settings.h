#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "deck_transaction_store.h"

#ifdef __cplusplus
extern "C" {
#endif

#define DECK_DEVICE_SETTINGS_DEFAULT_TEMPERATURE_OFFSET_TENTHS_C (-40)
#define DECK_DEVICE_SETTINGS_MINIMUM_TEMPERATURE_OFFSET_TENTHS_C (-150)
#define DECK_DEVICE_SETTINGS_MAXIMUM_TEMPERATURE_OFFSET_TENTHS_C 150

typedef struct deck_device_settings deck_device_settings_t;

typedef deck_transaction_storage_key_t deck_device_settings_storage_key_t;
typedef deck_transaction_storage_result_t deck_device_settings_storage_result_t;
typedef deck_transaction_storage_read_fn deck_device_settings_storage_read_fn;
typedef deck_transaction_storage_write_fn deck_device_settings_storage_write_fn;
typedef deck_transaction_storage_erase_fn deck_device_settings_storage_erase_fn;
typedef deck_transaction_storage_adapter_t deck_device_settings_storage_adapter_t;

#define DECK_DEVICE_SETTINGS_STORAGE_CANDIDATE DECK_TRANSACTION_STORAGE_CANDIDATE
#define DECK_DEVICE_SETTINGS_STORAGE_SLOT_0 DECK_TRANSACTION_STORAGE_SLOT_0
#define DECK_DEVICE_SETTINGS_STORAGE_SLOT_1 DECK_TRANSACTION_STORAGE_SLOT_1
#define DECK_DEVICE_SETTINGS_STORAGE_ACTIVE_MARKER DECK_TRANSACTION_STORAGE_ACTIVE_MARKER
#define DECK_DEVICE_SETTINGS_STORAGE_KEY_COUNT DECK_TRANSACTION_STORAGE_KEY_COUNT
#define DECK_DEVICE_SETTINGS_STORAGE_OK DECK_TRANSACTION_STORAGE_OK
#define DECK_DEVICE_SETTINGS_STORAGE_NOT_FOUND DECK_TRANSACTION_STORAGE_NOT_FOUND
#define DECK_DEVICE_SETTINGS_STORAGE_ERROR DECK_TRANSACTION_STORAGE_ERROR

typedef struct {
    deck_device_settings_storage_adapter_t storage;
} deck_device_settings_options_t;

typedef enum {
    DECK_DEVICE_SETTINGS_DEFAULT = 0,
    DECK_DEVICE_SETTINGS_ACTIVE,
    DECK_DEVICE_SETTINGS_STATE_STORAGE_ERROR,
} deck_device_settings_state_t;

typedef enum {
    DECK_DEVICE_SETTINGS_RECORD_EMPTY = 0,
    DECK_DEVICE_SETTINGS_RECORD_VALID,
    DECK_DEVICE_SETTINGS_RECORD_RECOVERED_PREVIOUS,
    DECK_DEVICE_SETTINGS_RECORD_CORRUPT,
    DECK_DEVICE_SETTINGS_RECORD_UNSUPPORTED_SCHEMA,
    DECK_DEVICE_SETTINGS_RECORD_MIGRATION_FAILED,
    DECK_DEVICE_SETTINGS_RECORD_IO_ERROR,
} deck_device_settings_record_status_t;

typedef struct {
    deck_device_settings_state_t state;
    deck_device_settings_record_status_t record_status;
    deck_device_settings_record_status_t candidate_record_status;
    bool has_active;
    bool has_candidate;
    uint32_t generation;
    int16_t temperature_offset_tenths_c;
} deck_device_settings_snapshot_t;

typedef enum {
    DECK_DEVICE_SETTINGS_UPDATED = 0,
    DECK_DEVICE_SETTINGS_INVALID_OFFSET,
    DECK_DEVICE_SETTINGS_STORAGE_FAILURE,
} deck_device_settings_update_result_t;

const char *deck_device_settings_state_name(deck_device_settings_state_t state);
const char *deck_device_settings_record_status_name(
    deck_device_settings_record_status_t status
);

deck_device_settings_t *deck_device_settings_create(
    const deck_device_settings_options_t *options
);
void deck_device_settings_destroy(deck_device_settings_t *settings);
deck_device_settings_update_result_t deck_device_settings_submit_temperature_offset(
    deck_device_settings_t *settings,
    int16_t temperature_offset_tenths_c
);
bool deck_device_settings_snapshot(
    const deck_device_settings_t *settings,
    deck_device_settings_snapshot_t *snapshot
);

#ifdef __cplusplus
}
#endif
