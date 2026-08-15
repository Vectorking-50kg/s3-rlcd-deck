#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "deck_serial_session.h"
#include "deck_serial_router.h"
#include "deck_serial_usb_bridge.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef struct deck_serial_service deck_serial_service_t;

typedef struct {
    deck_serial_session_snapshot_t snapshot;
    bool has_router_stats;
    deck_serial_router_stats_t router_stats;
    bool has_command_result;
    deck_serial_command_result_t command_result;
} deck_serial_service_event_t;

typedef void (*deck_serial_service_event_fn)(
    void *context,
    const deck_serial_service_event_t *event
);

/* Event callbacks run on the owner task and must return after a nonblocking copy/enqueue. */

/* Safety preflight used before any UI or recoverable service allocation. */
bool deck_serial_service_prepare_disarmed(void);

/*
 * Starts the sole Serial Session owner task. The task remains dormant while
 * DISARMED; UART1 and all target data-path tasks remain uninstalled/stopped.
 */
deck_serial_service_t *deck_serial_service_start(
    deck_serial_service_event_fn callback,
    void *callback_context
);

/* Bounded, retryable stop. A false return preserves ownership for a retry. */
bool deck_serial_service_stop(deck_serial_service_t *service);

bool deck_serial_service_enter(deck_serial_service_t *service);
bool deck_serial_service_exit(deck_serial_service_t *service);
bool deck_serial_service_request_web(
    deck_serial_service_t *service,
    uint64_t session_id,
    uint64_t request_id,
    bool enable
);
bool deck_serial_service_web_activity(
    deck_serial_service_t *service,
    uint64_t session_id,
    uint64_t lease_id
);
bool deck_serial_service_web_disconnect(
    deck_serial_service_t *service,
    uint64_t session_id,
    uint64_t lease_id
);
/*
 * Fences every owner request from the disconnected Device Link generation,
 * queues an unconditional Web -> USB revoke, and returns its acknowledgement
 * epoch. The caller must not open a replacement transport until completion.
 */
bool deck_serial_service_revoke_web_transport(
    deck_serial_service_t *service,
    uint64_t *revoke_epoch
);
bool deck_serial_service_web_transport_revoked(
    deck_serial_service_t *service,
    uint64_t revoke_epoch
);
/*
 * Copies one bounded Web -> target block into the owner queue. The Session
 * owner revalidates the exact session and lease immediately before UART TX.
 */
bool deck_serial_service_submit_web(
    deck_serial_service_t *service,
    uint64_t session_id,
    uint64_t lease_id,
    const uint8_t *bytes,
    size_t size
);
bool deck_serial_service_snapshot(
    deck_serial_service_t *service,
    deck_serial_session_snapshot_t *snapshot
);
/* Returns the most recent exact command result when its request ID matches. */
bool deck_serial_service_command_result(
    deck_serial_service_t *service,
    uint64_t request_id,
    deck_serial_command_result_t *result
);

/*
 * Transport adapters copy blocks through this boundary. They never receive
 * Router queues, pool addresses, or block ownership.
 */
deck_serial_router_copy_result_t deck_serial_service_take(
    deck_serial_service_t *service,
    deck_serial_sink_id_t sink,
    deck_serial_routed_block_t *block
);
deck_serial_router_copy_result_t deck_serial_service_copy_history_after(
    deck_serial_service_t *service,
    uint64_t after_sequence,
    deck_serial_routed_block_t *block
);
bool deck_serial_service_sink_stats(
    deck_serial_service_t *service,
    deck_serial_sink_id_t sink,
    deck_serial_sink_stats_t *stats
);
bool deck_serial_service_router_stats(
    deck_serial_service_t *service,
    deck_serial_router_stats_t *stats
);
bool deck_serial_service_usb_stats(
    deck_serial_service_t *service,
    deck_serial_usb_bridge_stats_t *stats
);

#ifdef __cplusplus
}
#endif
