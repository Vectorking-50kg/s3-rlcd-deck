#pragma once

#include <stdbool.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct {
    bool pending;
    uint64_t external_request_id;
    uint64_t service_request_id;
    uint64_t next_service_request_id;
} deck_serial_request_tracker_t;

typedef enum {
    DECK_SERIAL_REQUEST_INVALID = 0,
    DECK_SERIAL_REQUEST_NEW,
    DECK_SERIAL_REQUEST_REPLAY,
    DECK_SERIAL_REQUEST_BUSY,
} deck_serial_request_begin_result_t;

deck_serial_request_begin_result_t deck_serial_request_begin(
    deck_serial_request_tracker_t *tracker,
    uint64_t external_request_id,
    uint64_t *service_request_id
);

bool deck_serial_request_complete(
    deck_serial_request_tracker_t *tracker,
    uint64_t service_request_id,
    uint64_t *external_request_id
);

// A transport-generation reset forgets only the response correlation. The
// service-facing request sequence deliberately survives Companion reconnects.
void deck_serial_request_transport_reset(
    deck_serial_request_tracker_t *tracker
);

#ifdef __cplusplus
}
#endif
