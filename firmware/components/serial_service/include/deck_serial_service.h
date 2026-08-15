#pragma once

#include <stdbool.h>
#include <stdint.h>

#include "deck_serial_session.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef struct deck_serial_service deck_serial_service_t;

typedef struct {
    deck_serial_session_snapshot_t snapshot;
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
bool deck_serial_service_snapshot(
    deck_serial_service_t *service,
    deck_serial_session_snapshot_t *snapshot
);

#ifdef __cplusplus
}
#endif
