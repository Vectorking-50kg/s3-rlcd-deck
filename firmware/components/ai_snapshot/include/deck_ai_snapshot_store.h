#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "deck_ai_snapshot.h"
#include "deck_transaction_store.h"

#ifdef __cplusplus
extern "C" {
#endif

#define DECK_AI_SNAPSHOT_FLASH_INTERVAL_MS (30ULL * 60ULL * 1000ULL)
#define DECK_AI_SNAPSHOT_OFFLINE_LIMIT_MS (24ULL * 60ULL * 60ULL * 1000ULL)

typedef struct deck_ai_snapshot_store deck_ai_snapshot_store_t;

typedef bool (*deck_ai_snapshot_store_storage_open_fn)(
    void *context,
    deck_transaction_storage_adapter_t *storage,
    void **handle
);
typedef void (*deck_ai_snapshot_store_storage_close_fn)(
    void *context,
    void *handle
);

typedef struct {
    deck_ai_snapshot_store_storage_open_fn open;
    deck_ai_snapshot_store_storage_close_fn close;
    void *context;
} deck_ai_snapshot_store_storage_provider_t;

typedef struct {
    /*
     * A configured adapter is borrowed until destroy succeeds. Alternatively,
     * provider open/close lets the private worker own delayed storage setup.
     * All-zero options select a volatile store and report storage_faulted.
     */
    deck_transaction_storage_adapter_t storage;
    deck_ai_snapshot_store_storage_provider_t provider;
} deck_ai_snapshot_store_options_t;

typedef enum {
    DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY = 0,
    DECK_AI_SNAPSHOT_STORE_ACCEPTED_PERSISTED,
    DECK_AI_SNAPSHOT_STORE_ACCEPTED_STORAGE_FAILURE,
    DECK_AI_SNAPSHOT_STORE_UNCHANGED,
    DECK_AI_SNAPSHOT_STORE_MALFORMED,
    DECK_AI_SNAPSHOT_STORE_UNSUPPORTED_VERSION,
    DECK_AI_SNAPSHOT_STORE_PRIVATE_DATA,
    DECK_AI_SNAPSHOT_STORE_INVALID_TIME,
    DECK_AI_SNAPSHOT_STORE_TIME_ROLLBACK,
} deck_ai_snapshot_store_update_result_t;

typedef enum {
    DECK_AI_SNAPSHOT_STORE_EMPTY = 0,
    DECK_AI_SNAPSHOT_STORE_FRESH,
    DECK_AI_SNAPSHOT_STORE_STALE,
    DECK_AI_SNAPSHOT_STORE_UNAVAILABLE,
} deck_ai_snapshot_store_state_t;

typedef struct {
    deck_ai_snapshot_store_state_t state;
    deck_transaction_record_status_t record_status;
    deck_transaction_record_status_t candidate_record_status;
    bool has_snapshot;
    bool document_visible;
    bool quotas_visible;
    bool storage_faulted;
    size_t document_size;
    uint32_t persisted_generation;
    deck_ai_snapshot_metadata_t metadata;
} deck_ai_snapshot_store_snapshot_t;

deck_ai_snapshot_store_t *deck_ai_snapshot_store_create(
    const deck_ai_snapshot_store_options_t *options
);
/*
 * Returns false after a bounded wait if a storage driver is stuck. In that
 * fail-safe case the Store and its adapter must remain alive and may be passed
 * to destroy again after the driver returns.
 */
bool deck_ai_snapshot_store_destroy(deck_ai_snapshot_store_t *store);

/*
 * trusted_utc_ms comes from the authenticated Companion heartbeat. A valid
 * document always replaces the in-memory value immediately; Flash attempts
 * remain independently limited to one per 30 minutes.
 */
deck_ai_snapshot_store_update_result_t deck_ai_snapshot_store_apply(
    deck_ai_snapshot_store_t *store,
    const char *document,
    size_t document_size,
    uint64_t trusted_utc_ms
);

/*
 * Checkpoints run on a private worker so apply/copy never wait for Flash.
 * The wait seam is used by deterministic shutdown and Host power-point tests.
 */
bool deck_ai_snapshot_store_wait_for_idle(
    deck_ai_snapshot_store_t *store,
    uint32_t timeout_ms
);

/* Returns each asynchronous storage failure at most once to Link diagnostics. */
bool deck_ai_snapshot_store_take_storage_failure(
    deck_ai_snapshot_store_t *store
);

/*
 * Copies only display-authorized data. At or beyond 24 hours offline the raw
 * cached document remains internal and document_size is returned as zero.
 */
bool deck_ai_snapshot_store_copy(
    const deck_ai_snapshot_store_t *store,
    uint64_t now_utc_ms,
    bool companion_online,
    char *document,
    size_t document_capacity,
    size_t *document_size,
    deck_ai_snapshot_store_snapshot_t *snapshot
);

#ifdef __cplusplus
}
#endif
