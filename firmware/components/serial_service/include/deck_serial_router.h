#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define DECK_SERIAL_ROUTER_BLOCK_BYTES 256U
#define DECK_SERIAL_ROUTER_MAX_SINKS 4U
#define DECK_SERIAL_HISTORY_DEFAULT_BYTES (512U * 1024U)
#define DECK_SERIAL_HISTORY_MIN_BYTES (64U * 1024U)
#define DECK_SERIAL_HISTORY_MAX_BYTES (2048U * 1024U)

typedef struct deck_serial_router deck_serial_router_t;

typedef enum {
    DECK_SERIAL_SINK_USB = 0,
    DECK_SERIAL_SINK_WSS,
    DECK_SERIAL_SINK_HISTORY,
    DECK_SERIAL_SINK_STATS,
} deck_serial_sink_id_t;

typedef enum {
    DECK_SERIAL_ROUTER_COPY_OK = 0,
    DECK_SERIAL_ROUTER_COPY_EMPTY,
    DECK_SERIAL_ROUTER_COPY_GAP,
    DECK_SERIAL_ROUTER_COPY_INVALID,
} deck_serial_router_copy_result_t;

typedef enum {
    DECK_SERIAL_UART_FIFO_OVERFLOW = 0,
    DECK_SERIAL_UART_DRIVER_BUFFER_FULL,
    DECK_SERIAL_UART_ROUTER_STARVED,
} deck_serial_uart_error_t;

typedef void *(*deck_serial_router_allocate_fn)(
    void *context,
    size_t size,
    bool external_memory
);
typedef void (*deck_serial_router_deallocate_fn)(void *context, void *memory);

typedef struct {
    deck_serial_router_allocate_fn allocate;
    deck_serial_router_deallocate_fn deallocate;
    void *context;
} deck_serial_router_memory_t;

typedef struct {
    uint64_t session_id;
    size_t pool_capacity_bytes;
    uint64_t initial_sequence;
    deck_serial_router_memory_t memory;
} deck_serial_router_config_t;

typedef struct {
    deck_serial_sink_id_t id;
    size_t capacity_bytes;
    bool retained;
} deck_serial_sink_config_t;

typedef struct {
    uint64_t monotonic_ms;
    uint16_t length;
    uint8_t bytes[DECK_SERIAL_ROUTER_BLOCK_BYTES];
} deck_serial_input_block_t;

typedef struct {
    uint64_t session_id;
    uint64_t sequence;
    uint64_t monotonic_ms;
    uint16_t length;
    uint8_t bytes[DECK_SERIAL_ROUTER_BLOCK_BYTES];
} deck_serial_routed_block_t;

typedef struct {
    bool registered;
    bool retained;
    size_t capacity_bytes;
    size_t queued_bytes;
    size_t queued_blocks;
    uint64_t oldest_sequence;
    uint64_t newest_sequence;
    uint64_t overwritten_bytes;
    uint64_t overwritten_blocks;
    uint64_t delivered_bytes;
    uint64_t delivered_blocks;
    uint64_t cleared_bytes;
    uint64_t cleared_blocks;
} deck_serial_sink_stats_t;

typedef struct {
    uint64_t accepted_bytes;
    uint64_t accepted_blocks;
    uint64_t last_sequence;
    uint64_t pool_exhaustions;
    uint64_t uart_fifo_overflows;
    uint64_t uart_driver_buffer_full;
    uint64_t router_starvations;
} deck_serial_router_stats_t;

/*
 * The Router owns every allocation and never exposes a queue or internal
 * block. submit() copies one fixed input block into the session pool and
 * performs bounded fan-out; no sink callback or I/O runs on that path.
 */
deck_serial_router_t *deck_serial_router_create(
    const deck_serial_router_config_t *config
);
void deck_serial_router_destroy(deck_serial_router_t *router);

bool deck_serial_router_register_sink(
    deck_serial_router_t *router,
    const deck_serial_sink_config_t *config
);
bool deck_serial_router_unregister_sink(
    deck_serial_router_t *router,
    deck_serial_sink_id_t sink
);

bool deck_serial_router_submit(
    deck_serial_router_t *router,
    const deck_serial_input_block_t *block,
    uint64_t *sequence
);

/* Destructive delivery for live USB/WSS/stats sinks. */
deck_serial_router_copy_result_t deck_serial_router_take(
    deck_serial_router_t *router,
    deck_serial_sink_id_t sink,
    deck_serial_routed_block_t *block
);

/* Non-destructive reconnect/history copy after a previously seen sequence. */
deck_serial_router_copy_result_t deck_serial_router_copy_after(
    deck_serial_router_t *router,
    deck_serial_sink_id_t sink,
    uint64_t after_sequence,
    deck_serial_routed_block_t *block
);

bool deck_serial_router_clear_sink(
    deck_serial_router_t *router,
    deck_serial_sink_id_t sink
);
void deck_serial_router_note_uart_error(
    deck_serial_router_t *router,
    deck_serial_uart_error_t error
);
bool deck_serial_router_sink_stats(
    deck_serial_router_t *router,
    deck_serial_sink_id_t sink,
    deck_serial_sink_stats_t *stats
);
bool deck_serial_router_stats(
    deck_serial_router_t *router,
    deck_serial_router_stats_t *stats
);

#ifdef __cplusplus
}
#endif
