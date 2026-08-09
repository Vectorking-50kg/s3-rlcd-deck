#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define DECK_SETUP_SSID_CAPACITY 13
#define DECK_SETUP_PASSWORD_CAPACITY 15
#define DECK_SETUP_ADDRESS_CAPACITY 16

typedef struct deck_setup_mode deck_setup_mode_t;
typedef bool (*deck_setup_random_fn)(void *context, uint8_t *output, size_t size);

typedef enum {
    DECK_SETUP_REASON_NONE = 0,
    DECK_SETUP_REASON_NO_WIFI,
    DECK_SETUP_REASON_BOOT_LONG_PRESS,
} deck_setup_reason_t;

typedef enum {
    DECK_SETUP_MODE_UNCHANGED = 0,
    DECK_SETUP_MODE_STARTED,
    DECK_SETUP_MODE_RESTARTED,
    DECK_SETUP_MODE_STOPPED,
    DECK_SETUP_MODE_ERROR,
} deck_setup_mode_result_t;

typedef struct {
    uint64_t inactivity_timeout_ms;
    deck_setup_random_fn random;
    void *random_context;
} deck_setup_mode_config_t;

typedef struct {
    bool active;
    deck_setup_reason_t reason;
    uint32_t session_id;
    uint64_t started_at_ms;
    uint64_t last_activity_ms;
    char ssid[DECK_SETUP_SSID_CAPACITY];
    char password[DECK_SETUP_PASSWORD_CAPACITY];
    char address[DECK_SETUP_ADDRESS_CAPACITY];
} deck_setup_snapshot_t;

deck_setup_mode_t *deck_setup_mode_create(const deck_setup_mode_config_t *config);
void deck_setup_mode_destroy(deck_setup_mode_t *setup);

deck_setup_mode_result_t deck_setup_mode_boot(
    deck_setup_mode_t *setup,
    bool has_valid_wifi_config,
    uint64_t now_ms
);
deck_setup_mode_result_t deck_setup_mode_enter(
    deck_setup_mode_t *setup,
    deck_setup_reason_t reason,
    uint64_t now_ms
);
bool deck_setup_mode_activity(deck_setup_mode_t *setup, uint64_t now_ms);
deck_setup_mode_result_t deck_setup_mode_tick(deck_setup_mode_t *setup, uint64_t now_ms);
deck_setup_mode_result_t deck_setup_mode_stop(deck_setup_mode_t *setup);
bool deck_setup_mode_snapshot(const deck_setup_mode_t *setup, deck_setup_snapshot_t *snapshot);

#ifdef __cplusplus
}
#endif
