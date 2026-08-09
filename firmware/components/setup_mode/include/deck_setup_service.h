#pragma once

#include "deck_setup_mode.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef struct deck_setup_service deck_setup_service_t;

typedef enum {
    DECK_SETUP_SERVICE_ACTIVE = 0,
    DECK_SETUP_SERVICE_INACTIVE,
    DECK_SETUP_SERVICE_ERROR,
} deck_setup_service_state_t;

typedef struct {
    deck_setup_service_state_t state;
    deck_setup_snapshot_t setup;
    const char *error_stage;
} deck_setup_service_event_t;

typedef void (*deck_setup_service_event_fn)(
    void *context,
    const deck_setup_service_event_t *event
);

/* Starts a lifetime Setup Mode service. No Wi-Fi credentials are persisted by this API. */
deck_setup_service_t *deck_setup_service_start(
    bool has_valid_wifi_config,
    deck_setup_service_event_fn callback,
    void *callback_context
);

/* Queues a fresh Setup session. Existing Wi-Fi configuration is never deleted. */
bool deck_setup_service_enter_from_boot(deck_setup_service_t *service);

#ifdef __cplusplus
}
#endif
