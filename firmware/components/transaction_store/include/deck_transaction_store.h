#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define DECK_TRANSACTION_PAYLOAD_CAPACITY 128
#define DECK_TRANSACTION_MAX_PAYLOAD_CAPACITY 2048

typedef struct deck_transaction_store deck_transaction_store_t;
typedef struct deck_transaction_nvs_storage deck_transaction_nvs_storage_t;

typedef enum {
    DECK_TRANSACTION_STORAGE_CANDIDATE = 0,
    DECK_TRANSACTION_STORAGE_SLOT_0,
    DECK_TRANSACTION_STORAGE_SLOT_1,
    DECK_TRANSACTION_STORAGE_ACTIVE_MARKER,
    DECK_TRANSACTION_STORAGE_KEY_COUNT,
} deck_transaction_storage_key_t;

typedef enum {
    DECK_TRANSACTION_STORAGE_OK = 0,
    DECK_TRANSACTION_STORAGE_NOT_FOUND,
    DECK_TRANSACTION_STORAGE_ERROR,
} deck_transaction_storage_result_t;

typedef deck_transaction_storage_result_t (*deck_transaction_storage_read_fn)(
    void *context,
    deck_transaction_storage_key_t key,
    uint8_t *output,
    size_t capacity,
    size_t *size
);
typedef bool (*deck_transaction_storage_write_fn)(
    void *context,
    deck_transaction_storage_key_t key,
    const uint8_t *data,
    size_t size
);
typedef bool (*deck_transaction_storage_erase_fn)(
    void *context,
    deck_transaction_storage_key_t key
);

typedef struct {
    deck_transaction_storage_read_fn read;
    deck_transaction_storage_write_fn write;
    deck_transaction_storage_erase_fn erase;
    void *context;
} deck_transaction_storage_adapter_t;

typedef bool (*deck_transaction_validate_payload_fn)(
    void *context,
    const uint8_t *payload,
    size_t size
);

typedef struct {
    deck_transaction_storage_adapter_t storage;
    uint8_t record_magic[4];
    uint8_t marker_magic[4];
    uint8_t schema_version;
    /* Zero selects DECK_TRANSACTION_PAYLOAD_CAPACITY. */
    size_t payload_capacity;
    /* Leading payload bytes omitted from the schema's encoded length field. */
    uint8_t payload_length_excluded_prefix;
    deck_transaction_validate_payload_fn validate_payload;
    void *payload_context;
} deck_transaction_store_options_t;

typedef enum {
    DECK_TRANSACTION_RECORD_EMPTY = 0,
    DECK_TRANSACTION_RECORD_VALID,
    DECK_TRANSACTION_RECORD_RECOVERED_PREVIOUS,
    DECK_TRANSACTION_RECORD_CORRUPT,
    DECK_TRANSACTION_RECORD_UNSUPPORTED_SCHEMA,
    DECK_TRANSACTION_RECORD_MIGRATION_FAILED,
    DECK_TRANSACTION_RECORD_IO_ERROR,
} deck_transaction_record_status_t;

typedef struct {
    /* Borrowed from the store; valid until its next mutation or destruction. */
    const uint8_t *payload;
    size_t payload_size;
    uint32_t generation;
} deck_transaction_record_t;

typedef struct {
    deck_transaction_record_status_t record_status;
    deck_transaction_record_status_t candidate_record_status;
    deck_transaction_record_t active;
    deck_transaction_record_t candidate;
    uint8_t active_slot;
    bool has_active;
    bool has_candidate;
    bool storage_faulted;
} deck_transaction_store_snapshot_t;

typedef enum {
    DECK_TRANSACTION_UPDATED = 0,
    DECK_TRANSACTION_INVALID_PAYLOAD,
    DECK_TRANSACTION_STORAGE_FAILURE,
    DECK_TRANSACTION_NO_CANDIDATE,
} deck_transaction_update_result_t;

deck_transaction_store_t *deck_transaction_store_create(
    const deck_transaction_store_options_t *options
);
void deck_transaction_store_destroy(deck_transaction_store_t *store);
deck_transaction_update_result_t deck_transaction_store_stage(
    deck_transaction_store_t *store,
    const uint8_t *payload,
    size_t size
);
deck_transaction_update_result_t deck_transaction_store_commit(
    deck_transaction_store_t *store
);
bool deck_transaction_store_clear(deck_transaction_store_t *store);
bool deck_transaction_store_snapshot(
    const deck_transaction_store_t *store,
    deck_transaction_store_snapshot_t *snapshot
);

/* nvs_flash_init() must succeed before opening the namespaced adapter. */
deck_transaction_nvs_storage_t *deck_transaction_nvs_storage_open(
    const char *namespace_name
);
void deck_transaction_nvs_storage_close(deck_transaction_nvs_storage_t *storage);
bool deck_transaction_nvs_storage_adapter(
    deck_transaction_nvs_storage_t *storage,
    deck_transaction_storage_adapter_t *adapter
);

#ifdef __cplusplus
}
#endif
