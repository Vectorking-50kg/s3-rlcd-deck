#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define DECK_OTA_BOARD_CAPACITY 49
#define DECK_OTA_VERSION_CAPACITY 33
#define DECK_OTA_DIGEST_BYTES 32
#define DECK_OTA_SIGNATURE_BYTES 64
#define DECK_OTA_MAX_CHUNK_BYTES 3072

typedef struct deck_ota_transaction deck_ota_transaction_t;

typedef struct {
    char version[DECK_OTA_VERSION_CAPACITY];
    char board[DECK_OTA_BOARD_CAPACITY];
    uint32_t image_length;
    uint8_t image_sha256[DECK_OTA_DIGEST_BYTES];
    uint8_t signature[DECK_OTA_SIGNATURE_BYTES];
    uint32_t signing_key_id;
    uint32_t minimum_protocol_version;
} deck_ota_manifest_t;

typedef enum {
    DECK_OTA_IDLE = 0,
    DECK_OTA_RECEIVING,
    DECK_OTA_READY_TO_REBOOT,
    DECK_OTA_FAILED,
} deck_ota_state_t;

typedef enum {
    DECK_OTA_OK = 0,
    DECK_OTA_INVALID_MANIFEST,
    DECK_OTA_WRONG_BOARD,
    DECK_OTA_INCOMPATIBLE_PROTOCOL,
    DECK_OTA_DOWNGRADE_REJECTED,
    DECK_OTA_IMAGE_TOO_LARGE,
    DECK_OTA_SIGNATURE_REJECTED,
    DECK_OTA_BUSY,
    DECK_OTA_STALE_OFFSET,
    DECK_OTA_FLASH_FAILURE,
    DECK_OTA_HASH_MISMATCH,
    DECK_OTA_IMAGE_INVALID,
    DECK_OTA_TIMED_OUT,
} deck_ota_result_t;

typedef bool (*deck_ota_flash_begin_fn)(void *context, size_t image_size);
typedef bool (*deck_ota_flash_write_fn)(
    void *context,
    const uint8_t *data,
    size_t size
);
typedef bool (*deck_ota_flash_finish_fn)(void *context);
typedef void (*deck_ota_flash_abort_fn)(void *context);
typedef bool (*deck_ota_flash_select_boot_fn)(void *context);

typedef struct {
    deck_ota_flash_begin_fn begin;
    deck_ota_flash_write_fn write;
    deck_ota_flash_finish_fn finish;
    deck_ota_flash_abort_fn abort;
    deck_ota_flash_select_boot_fn select_boot;
    void *context;
} deck_ota_flash_adapter_t;

typedef bool (*deck_ota_hash_begin_fn)(void *context);
typedef bool (*deck_ota_hash_update_fn)(
    void *context,
    const uint8_t *data,
    size_t size
);
typedef bool (*deck_ota_hash_finish_fn)(
    void *context,
    uint8_t output[DECK_OTA_DIGEST_BYTES]
);
typedef void (*deck_ota_hash_abort_fn)(void *context);
typedef bool (*deck_ota_verify_manifest_fn)(
    void *context,
    const deck_ota_manifest_t *manifest
);

typedef struct {
    deck_ota_hash_begin_fn hash_begin;
    deck_ota_hash_update_fn hash_update;
    deck_ota_hash_finish_fn hash_finish;
    deck_ota_hash_abort_fn hash_abort;
    deck_ota_verify_manifest_fn verify_manifest;
    void *context;
} deck_ota_crypto_adapter_t;

typedef struct {
    deck_ota_flash_adapter_t flash;
    deck_ota_crypto_adapter_t crypto;
    const char *running_version;
    const char *board;
    uint32_t protocol_version;
    size_t partition_capacity;
    uint64_t inactivity_timeout_ms;
    uint64_t maximum_duration_ms;
} deck_ota_transaction_options_t;

typedef struct {
    deck_ota_state_t state;
    deck_ota_result_t result;
    uint32_t received_bytes;
    uint32_t image_length;
} deck_ota_transaction_snapshot_t;

deck_ota_transaction_t *deck_ota_transaction_create(
    const deck_ota_transaction_options_t *options
);
void deck_ota_transaction_destroy(deck_ota_transaction_t *transaction);
deck_ota_result_t deck_ota_transaction_offer(
    deck_ota_transaction_t *transaction,
    const deck_ota_manifest_t *manifest,
    uint64_t now_ms
);
deck_ota_result_t deck_ota_transaction_write(
    deck_ota_transaction_t *transaction,
    uint32_t offset,
    const uint8_t *data,
    size_t size,
    bool final,
    uint64_t now_ms
);
deck_ota_result_t deck_ota_transaction_tick(
    deck_ota_transaction_t *transaction,
    uint64_t now_ms
);
bool deck_ota_transaction_snapshot(
    const deck_ota_transaction_t *transaction,
    deck_ota_transaction_snapshot_t *snapshot
);

#ifdef __cplusplus
}
#endif
