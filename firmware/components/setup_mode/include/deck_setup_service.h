#pragma once

#include "deck_setup_mode.h"
#include "deck_device_settings.h"
#include "deck_companion_profiles.h"
#include "deck_wifi_config.h"

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
    deck_wifi_config_snapshot_t wifi;
    deck_device_settings_snapshot_t settings;
    const char *error_stage;
} deck_setup_service_event_t;

typedef void (*deck_setup_service_event_fn)(
    void *context,
    const deck_setup_service_event_t *event
);

/* Starts the lifetime transactional device configuration and Setup Mode service. */
deck_setup_service_t *deck_setup_service_start(
    deck_setup_service_event_fn callback,
    void *callback_context
);

/* Queues a fresh Setup session. Existing Wi-Fi configuration is never deleted. */
bool deck_setup_service_enter_from_boot(deck_setup_service_t *service);

/* Queues credentials from an already authenticated/local control surface. */
bool deck_setup_service_submit_wifi(
    deck_setup_service_t *service,
    const deck_wifi_credentials_t *credentials
);

bool deck_setup_service_submit_temperature_offset(
    deck_setup_service_t *service,
    int16_t temperature_offset_tenths_c
);

bool deck_setup_service_select_companion(
    deck_setup_service_t *service,
    const char *profile_id
);
bool deck_setup_service_set_companion_priority(
    deck_setup_service_t *service,
    const char *profile_id,
    int32_t priority
);
bool deck_setup_service_revoke_companion(
    deck_setup_service_t *service,
    const char *profile_id
);

/* Borrowed lifetime Profiles interface for the Device Link owner. */
/*
 * Waits for the service-owned Profile module to finish initialization. A
 * nonzero timeout is required because deck_setup_service_start is asynchronous.
 */
deck_companion_profiles_t *deck_setup_service_wait_companion_profiles(
    deck_setup_service_t *service,
    uint32_t timeout_ms
);

bool deck_setup_service_request_wifi_clear(
    deck_setup_service_t *service,
    char *token,
    size_t token_capacity
);

typedef enum {
    DECK_SETUP_WIFI_CLEAR_QUEUED = 0,
    DECK_SETUP_WIFI_CLEAR_INACTIVE,
    DECK_SETUP_WIFI_CLEAR_NOT_ISSUED,
    DECK_SETUP_WIFI_CLEAR_MISMATCH,
    DECK_SETUP_WIFI_CLEAR_EXPIRED,
    DECK_SETUP_WIFI_CLEAR_BUSY,
} deck_setup_wifi_clear_confirm_result_t;

deck_setup_wifi_clear_confirm_result_t deck_setup_service_confirm_wifi_clear(
    deck_setup_service_t *service,
    const char *token
);

#ifdef __cplusplus
}
#endif
