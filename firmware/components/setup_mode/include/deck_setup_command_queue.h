#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "deck_companion_profiles.h"
#include "deck_wifi_config.h"

#ifdef __cplusplus
extern "C" {
#endif

#define DECK_SETUP_COMMAND_QUEUE_CAPACITY 8

typedef enum {
    DECK_SETUP_COMMAND_ENTER_FROM_BOOT = 0,
    DECK_SETUP_COMMAND_SUBMIT_WIFI,
    DECK_SETUP_COMMAND_VALIDATION_CONNECTED,
    DECK_SETUP_COMMAND_VALIDATION_AUTH_FAILED,
    DECK_SETUP_COMMAND_VALIDATION_CONNECTION_FAILED,
    DECK_SETUP_COMMAND_SUBMIT_TEMPERATURE_OFFSET,
    DECK_SETUP_COMMAND_CLEAR_WIFI,
    DECK_SETUP_COMMAND_PAIR_COMPANION,
    DECK_SETUP_COMMAND_SELECT_COMPANION,
    DECK_SETUP_COMMAND_SET_COMPANION_PRIORITY,
    DECK_SETUP_COMMAND_REVOKE_COMPANION,
} deck_setup_command_type_t;

typedef struct {
    deck_setup_command_type_t type;
    deck_wifi_credentials_t credentials;
    int16_t temperature_offset_tenths_c;
    deck_companion_pair_request_t companion_pair;
    uint32_t response_generation;
    char companion_profile_id[DECK_COMPANION_PROFILE_ID_CAPACITY];
    int32_t companion_priority;
} deck_setup_command_t;

typedef struct deck_setup_command_queue deck_setup_command_queue_t;

deck_setup_command_queue_t *deck_setup_command_queue_create(void);
void deck_setup_command_queue_destroy(deck_setup_command_queue_t *queue);
bool deck_setup_command_queue_try_send(
    deck_setup_command_queue_t *queue,
    const deck_setup_command_t *command
);
bool deck_setup_command_queue_try_receive(
    deck_setup_command_queue_t *queue,
    deck_setup_command_t *command
);
void deck_setup_command_clear(deck_setup_command_t *command);

#ifdef __cplusplus
}
#endif
